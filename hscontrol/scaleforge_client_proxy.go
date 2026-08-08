package hscontrol

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

const (
	scaleForgeProxyTimeout       = 15 * time.Second
	scaleForgeProxyRequestLimit  = 256 << 10
	scaleForgeProxyResponseLimit = 1 << 20
	publicUpdateBurst            = 60
	publicUpdateRefillInterval   = time.Second
	publicUpdateEntryTTL         = 10 * time.Minute
	publicUpdateMaxEntries       = 4096
	clientOperationBurst         = 6
	clientOperationRefill        = 5 * time.Second
	clientOperationEntryTTL      = 30 * time.Minute
	clientOperationMaxEntries    = 16_384
)

var errScaleForgeProxyRequestTooLarge = errors.New("ScaleForge client request is too large")

type boundedTokenBucketEntry struct {
	key        string
	tokens     float64
	lastRefill time.Time
	lastSeen   time.Time
	element    *list.Element
}

type boundedTokenBucketLimiter struct {
	mu             sync.Mutex
	entries        map[string]*boundedTokenBucketEntry
	lru            *list.List
	capacity       float64
	refillInterval time.Duration
	entryTTL       time.Duration
	maxEntries     int
}

func newBoundedTokenBucketLimiter(
	capacity uint,
	refillInterval time.Duration,
	entryTTL time.Duration,
	maxEntries int,
) *boundedTokenBucketLimiter {
	if capacity == 0 || refillInterval <= 0 || entryTTL <= 0 || maxEntries <= 0 {
		panic("invalid bounded token bucket configuration")
	}
	return &boundedTokenBucketLimiter{
		entries:        make(map[string]*boundedTokenBucketEntry),
		lru:            list.New(),
		capacity:       float64(capacity),
		refillInterval: refillInterval,
		entryTTL:       entryTTL,
		maxEntries:     maxEntries,
	}
}

func (limiter *boundedTokenBucketLimiter) allow(key string, now time.Time) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		key = "<unknown>"
	}

	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	limiter.evictExpiredLocked(now)
	entry, ok := limiter.entries[key]
	if !ok {
		for len(limiter.entries) >= limiter.maxEntries {
			limiter.evictOldestLocked()
		}
		entry = &boundedTokenBucketEntry{
			key:        key,
			tokens:     limiter.capacity,
			lastRefill: now,
			lastSeen:   now,
		}
		entry.element = limiter.lru.PushFront(entry)
		limiter.entries[key] = entry
	} else {
		if elapsed := now.Sub(entry.lastRefill); elapsed > 0 {
			entry.tokens = min(limiter.capacity, entry.tokens+float64(elapsed)/float64(limiter.refillInterval))
			entry.lastRefill = now
		}
		entry.lastSeen = now
		limiter.lru.MoveToFront(entry.element)
	}

	if entry.tokens < 1 {
		return false
	}
	entry.tokens--

	return true
}

func (limiter *boundedTokenBucketLimiter) evictExpiredLocked(now time.Time) {
	cutoff := now.Add(-limiter.entryTTL)
	for element := limiter.lru.Back(); element != nil; {
		entry := element.Value.(*boundedTokenBucketEntry)
		if !entry.lastSeen.Before(cutoff) {
			return
		}
		previous := element.Prev()
		delete(limiter.entries, entry.key)
		limiter.lru.Remove(element)
		element = previous
	}
}

func (limiter *boundedTokenBucketLimiter) evictOldestLocked() {
	element := limiter.lru.Back()
	if element == nil {
		return
	}
	entry := element.Value.(*boundedTokenBucketEntry)
	delete(limiter.entries, entry.key)
	limiter.lru.Remove(element)
}

var (
	publicUpdateGuard = newBoundedTokenBucketLimiter(
		publicUpdateBurst,
		publicUpdateRefillInterval,
		publicUpdateEntryTTL,
		publicUpdateMaxEntries,
	)
	clientOperationGuard = newBoundedTokenBucketLimiter(
		clientOperationBurst,
		clientOperationRefill,
		clientOperationEntryTTL,
		clientOperationMaxEntries,
	)
)

var scaleForgeProxyOperations = map[string]string{
	"client-update": http.MethodGet,
	"traffic":       http.MethodPost,
	"policy":        http.MethodGet,
	"policy-state":  http.MethodPost,
}

func newScaleForgeBackendClient(socketPath string) *http.Client {
	if strings.TrimSpace(socketPath) == "" {
		return nil
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return new(net.Dialer).DialContext(ctx, "unix", socketPath)
		},
		DisableCompression: true,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   scaleForgeProxyTimeout,
	}
}

func scaleForgeProxyOperation(path, method string) (string, error) {
	operation := strings.TrimPrefix(path, "/machine/scaleforge/")
	wantMethod, ok := scaleForgeProxyOperations[operation]
	if !ok || operation == path {
		return "", errors.New("unsupported ScaleForge client operation")
	}
	if method != wantMethod {
		return "", errors.New("method not allowed")
	}
	return operation, nil
}

func scaleForgeProxyQuery(operation string, values url.Values) (string, error) {
	if operation != "client-update" {
		if len(values) != 0 {
			return "", errors.New("query parameters are not allowed")
		}
		return "", nil
	}
	for key, entries := range values {
		if (key != "current_version" && key != "platform" && key != "current_revision") || len(entries) != 1 {
			return "", errors.New("invalid update query")
		}
	}

	currentVersion := strings.TrimSpace(values.Get("current_version"))
	platform := strings.TrimSpace(values.Get("platform"))
	currentRevision := strings.TrimSpace(values.Get("current_revision"))
	if len(currentVersion) > 64 || len(platform) > 64 || len(currentRevision) > 20 {
		return "", errors.New("invalid update query")
	}
	if currentRevision != "" {
		revision, err := strconv.ParseUint(currentRevision, 10, 64)
		if err != nil || revision > 1<<53-1 {
			return "", errors.New("invalid update query")
		}
	}
	clean := make(url.Values)
	if currentVersion != "" {
		clean.Set("current_version", currentVersion)
	}
	if platform != "" {
		clean.Set("platform", platform)
	}
	if currentRevision != "" {
		clean.Set("current_revision", currentRevision)
	}
	return clean.Encode(), nil
}

func readScaleForgeProxyRequestBody(body io.Reader) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(body, scaleForgeProxyRequestLimit+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > scaleForgeProxyRequestLimit {
		return nil, errScaleForgeProxyRequestTooLarge
	}
	return payload, nil
}

func allowPublicClientUpdate(source string, now time.Time) bool {
	return publicUpdateGuard.allow(source, now)
}

func scaleForgeClientOperationRateLimitKey(nodeID uint64, operation string) string {
	return operation + ":" + strconv.FormatUint(nodeID, 10)
}

func allowScaleForgeClientOperation(nodeID uint64, operation string, now time.Time) bool {
	key := scaleForgeClientOperationRateLimitKey(nodeID, operation)
	return clientOperationGuard.allow(key, now)
}

// ScaleTailClientUpdateHandler exposes only signed, non-secret release
// metadata. It deliberately has no account or node authentication so a logged
// out client can still install a mandatory security update.
func (h *Headscale) ScaleTailClientUpdateHandler(writer http.ResponseWriter, req *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if h.scaleForgeHTTP == nil {
		http.Error(writer, "ScaleForge integration is unavailable", http.StatusServiceUnavailable)
		return
	}
	source := noiseAuthSource(req, h.cfg.ScaleForge.TrustedProxyCIDRs)
	if !allowPublicClientUpdate(source, time.Now().UTC()) {
		writer.Header().Set("Retry-After", "60")
		http.Error(writer, "too many update checks", http.StatusTooManyRequests)
		return
	}
	rawQuery, err := scaleForgeProxyQuery("client-update", req.URL.Query())
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	target := &url.URL{
		Scheme:   "http",
		Host:     "scaleforge.internal",
		Path:     "/internal/v1/client/public-client-update",
		RawQuery: rawQuery,
	}
	upstream, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target.String(), http.NoBody)
	if err != nil {
		http.Error(writer, "failed to create internal request", http.StatusInternalServerError)
		return
	}
	upstream.Header.Set("Accept", "application/json")
	if err := signScaleForgeInternalRequest(
		upstream,
		nil,
		h.scaleForgeAuthKey,
		time.Now().UTC(),
	); err != nil {
		http.Error(writer, "failed to authenticate internal request", http.StatusServiceUnavailable)
		return
	}
	response, err := h.scaleForgeHTTP.Do(upstream)
	if err != nil {
		http.Error(writer, "ScaleForge service is unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, scaleForgeProxyResponseLimit+1))
	if err != nil || len(body) > scaleForgeProxyResponseLimit {
		http.Error(writer, "invalid ScaleForge response", http.StatusBadGateway)
		return
	}

	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(response.StatusCode)
	_, _ = writer.Write(body)
}

func (ns *noiseServer) authenticatedScaleForgeNode(req *http.Request) (nodeID, userID uint64, err error) {
	value := strings.TrimSpace(req.Header.Get(tailcfg.LBHeader))
	if value == "" {
		return 0, 0, errors.New("missing node identity")
	}

	var nodeKey key.NodePublic
	if err := nodeKey.UnmarshalText([]byte(value)); err != nil {
		return 0, 0, errors.New("invalid node identity")
	}
	node, ok := ns.headscale.state.GetNodeByNodeKey(nodeKey)
	if !ok || node.IsExpired() {
		return 0, 0, errors.New("node is not active")
	}
	if node.MachineKey() != ns.machineKey {
		return 0, 0, errors.New("node does not belong to this Noise session")
	}
	uid := node.TypedUserID()
	if uid == 0 {
		return 0, 0, errors.New("node has no account network")
	}
	if !ns.hasAccountProof(node.NodeKey(), uid) {
		return 0, 0, errors.New("account password proof required for this Noise session")
	}

	return node.ID().Uint64(), uint64(uid), nil
}

// ScaleForgeClientHandler forwards authenticated client-only operations to
// ScaleForge's private Unix socket. Identity always comes from the Noise
// session and current NodeKey; request bodies cannot select another node.
func (ns *noiseServer) ScaleForgeClientHandler(writer http.ResponseWriter, req *http.Request) {
	if ns.headscale.scaleForgeHTTP == nil {
		http.Error(writer, "ScaleForge integration is unavailable", http.StatusServiceUnavailable)
		return
	}
	operation, err := scaleForgeProxyOperation(req.URL.Path, req.Method)
	if err != nil {
		status := http.StatusNotFound
		if err.Error() == "method not allowed" {
			status = http.StatusMethodNotAllowed
		}
		http.Error(writer, err.Error(), status)
		return
	}
	rawQuery, err := scaleForgeProxyQuery(operation, req.URL.Query())
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	nodeID, userID, err := ns.authenticatedScaleForgeNode(req)
	if err != nil {
		http.Error(writer, "authenticated node required", http.StatusUnauthorized)
		return
	}
	if !allowScaleForgeClientOperation(nodeID, operation, time.Now().UTC()) {
		writer.Header().Set("Retry-After", strconv.Itoa(int(clientOperationRefill/time.Second)))
		http.Error(writer, "too many ScaleForge client requests", http.StatusTooManyRequests)
		return
	}
	requestBody := io.Reader(http.NoBody)
	var payload []byte
	if req.Method == http.MethodPost {
		payload, err = readScaleForgeProxyRequestBody(req.Body)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errScaleForgeProxyRequestTooLarge) {
				status = http.StatusRequestEntityTooLarge
			}
			http.Error(writer, "invalid ScaleForge client request", status)
			return
		}
		requestBody = bytes.NewReader(payload)
	}

	target := &url.URL{
		Scheme:   "http",
		Host:     "scaleforge.internal",
		Path:     "/internal/v1/client/" + operation,
		RawQuery: rawQuery,
	}
	upstream, err := http.NewRequestWithContext(req.Context(), req.Method, target.String(), requestBody)
	if err != nil {
		http.Error(writer, "failed to create internal request", http.StatusInternalServerError)
		return
	}
	upstream.Header.Set("Accept", "application/json")
	upstream.Header.Set("X-ScaleForge-Node-ID", strconv.FormatUint(nodeID, 10))
	upstream.Header.Set("X-ScaleForge-User-ID", strconv.FormatUint(userID, 10))
	if sourceIP := net.ParseIP(ns.authSource); sourceIP != nil {
		upstream.Header.Set("X-ScaleForge-Source-IP", sourceIP.String())
	}
	if req.Method == http.MethodPost {
		upstream.Header.Set("Content-Type", "application/json")
	}
	if err := signScaleForgeInternalRequest(
		upstream,
		payload,
		ns.headscale.scaleForgeAuthKey,
		time.Now().UTC(),
	); err != nil {
		http.Error(writer, "failed to authenticate internal request", http.StatusServiceUnavailable)
		return
	}

	response, err := ns.headscale.scaleForgeHTTP.Do(upstream)
	if err != nil {
		http.Error(writer, "ScaleForge service is unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, scaleForgeProxyResponseLimit+1))
	if err != nil || len(body) > scaleForgeProxyResponseLimit {
		http.Error(writer, "invalid ScaleForge response", http.StatusBadGateway)
		return
	}

	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(response.StatusCode)
	_, _ = writer.Write(body)
}
