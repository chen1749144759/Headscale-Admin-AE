package hscontrol

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/metrics"
	"github.com/juanfont/headscale/hscontrol/capver"
	hsdb "github.com/juanfont/headscale/hscontrol/db"
	"github.com/juanfont/headscale/hscontrol/state"
	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/juanfont/headscale/hscontrol/util"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/net/http2"
	"tailscale.com/control/controlbase"
	"tailscale.com/control/controlhttp/controlhttpserver"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

// ErrUnsupportedClientVersion is returned when a client connects with an unsupported protocol version.
var ErrUnsupportedClientVersion = errors.New("unsupported client version")

// ErrMissingURLParameter is returned when a required URL parameter is not provided.
var ErrMissingURLParameter = errors.New("missing URL parameter")

// ErrUnsupportedURLParameterType is returned when a URL parameter has an unsupported type.
var ErrUnsupportedURLParameterType = errors.New("unsupported URL parameter type")

// ErrSSHDstNodeNotFound is returned when the dst node id on a Noise SSH
// action request does not match any registered node.
var ErrSSHDstNodeNotFound = errors.New("ssh action: unknown dst node id")

// ErrSSHMachineKeyMismatch is returned when the Noise session's machine
// key does not match the dst node referenced in the SSH action URL.
var ErrSSHMachineKeyMismatch = errors.New(
	"ssh action: noise session machine key does not match dst node",
)

const (
	// ts2021UpgradePath is the path that the server listens on for the WebSockets upgrade.
	ts2021UpgradePath = "/ts2021"

	// The first 9 bytes from the server to client over Noise are either an HTTP/2
	// settings frame (a normal HTTP/2 setup) or, as Tailscale added later, an "early payload"
	// header that's also 9 bytes long: 5 bytes (earlyPayloadMagic) followed by 4 bytes
	// of length. Then that many bytes of JSON-encoded tailcfg.EarlyNoise.
	// The early payload is optional. Some servers may not send it... But we do!
	earlyPayloadMagic = "\xff\xff\xffTS"

	// noiseBodyLimit is the maximum allowed request body size for Noise protocol
	// handlers. This prevents unauthenticated OOM attacks via unbounded io.ReadAll.
	// No legitimate Noise request (MapRequest, RegisterRequest, etc.) comes close
	// to this limit; typical payloads are a few KB.
	noiseBodyLimit int64 = 262144 // 256 KiB

	accountUsernameHeader = "X-ScaleTail-Account"
	accountPasswordHeader = "X-ScaleTail-Password"
)

type noiseServer struct {
	headscale *Headscale

	httpBaseConfig *http.Server
	http2Server    *http2.Server
	conn           *controlbase.Conn
	machineKey     key.MachinePublic
	nodeKey        key.NodePublic

	// EarlyNoise-related stuff
	challenge       key.ChallengePrivate
	protocolVersion int
	authSource      string

	accountAuthMu      sync.Mutex
	accountAuthNodeKey key.NodePublic
	accountAuthUserID  types.UserID
	accountAuthID      uint
	accountAuthVersion uint64
	accountAuthExpiry  time.Time
}

// NoiseUpgradeHandler is to upgrade the connection and hijack the net.Conn
// in order to use the Noise-based TS2021 protocol. Listens in /ts2021.
func (h *Headscale) NoiseUpgradeHandler(
	writer http.ResponseWriter,
	req *http.Request,
) {
	log.Trace().Caller().Msgf("noise upgrade handler for client %s", req.RemoteAddr)

	upgrade := req.Header.Get("Upgrade")
	if upgrade == "" {
		// This probably means that the user is running Headscale behind an
		// improperly configured reverse proxy. TS2021 requires WebSockets to
		// be passed to Headscale. Let's give them a hint.
		log.Warn().
			Caller().
			Msg("no upgrade header in TS2021 request. If headscale is behind a reverse proxy, make sure it is configured to pass WebSockets through.")
		http.Error(writer, "Internal error", http.StatusInternalServerError)

		return
	}

	ns := noiseServer{
		headscale:  h,
		challenge:  key.NewChallenge(),
		authSource: noiseAuthSource(req, h.cfg.ScaleForge.TrustedProxyCIDRs),
	}

	noiseConn, err := controlhttpserver.AcceptHTTP(
		req.Context(),
		writer,
		req,
		*h.noisePrivateKey,
		ns.earlyNoise,
	)
	if err != nil {
		httpError(writer, fmt.Errorf("upgrading noise connection: %w", err))
		return
	}

	ns.conn = noiseConn
	ns.machineKey = ns.conn.Peer()
	ns.protocolVersion = ns.conn.ProtocolVersion()

	// This router is served only over the Noise connection, and exposes only the new API.
	//
	// The HTTP2 server that exposes this router is created for
	// a single hijacked connection from /ts2021, using netutil.NewOneConnListener

	r := chi.NewRouter()

	// Limit request body size to prevent unauthenticated OOM attacks.
	// The Noise handshake accepts any machine key without checking
	// registration, so all endpoints behind this router are reachable
	// without credentials.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, noiseBodyLimit)
			next.ServeHTTP(w, r)
		})
	})
	r.Use(metrics.Collector(metrics.CollectorOpts{
		Host:  false,
		Proto: true,
		Skip: func(r *http.Request) bool {
			return r.Method != http.MethodOptions
		},
	}))
	r.Use(middleware.RequestID)
	r.Use(middleware.RequestLogger(&zerologRequestLogger{}))
	r.Use(middleware.Recoverer)

	r.Route("/machine", func(r chi.Router) {
		r.Post("/register", ns.RegistrationHandler)
		r.Post("/auth/password", ns.PasswordAuthHandler)
		r.Post("/map", ns.PollNetMapHandler)
		r.Get("/scaleforge/client-update", ns.ScaleForgeClientHandler)
		r.Post("/scaleforge/traffic", ns.ScaleForgeClientHandler)
		r.Get("/scaleforge/policy", ns.ScaleForgeClientHandler)
		r.Post("/scaleforge/policy-state", ns.ScaleForgeClientHandler)

		// SSH Check mode endpoint, consulted to validate if a given SSH connection should be accepted or rejected.
		r.Get("/ssh/action/{src_node_id}/to/{dst_node_id}", ns.SSHActionHandler)

		// Not implemented yet
		//
		// /whoami is a debug endpoint to validate that the client can communicate over the connection,
		// not clear if there is a specific response, it looks like it is just logged.
		// https://github.com/tailscale/tailscale/blob/dfba01ca9bd8c4df02c3c32f400d9aeb897c5fc7/cmd/tailscale/cli/debug.go#L1138
		r.Get("/whoami", ns.NotImplementedHandler)

		// client sends a [tailcfg.SetDNSRequest] to this endpoints and expect
		// the server to create or update this DNS record "somewhere".
		// It is typically a TXT record for an ACME challenge.
		r.Post("/set-dns", ns.NotImplementedHandler)

		// A patch of [tailcfg.SetDeviceAttributesRequest] to update device attributes.
		// We currently do not support device attributes.
		r.Patch("/set-device-attr", ns.NotImplementedHandler)

		// A [tailcfg.AuditLogRequest] to send audit log entries to the server.
		// The server is expected to store them "somewhere".
		// We currently do not support device attributes.
		r.Post("/audit-log", ns.NotImplementedHandler)

		// handles requests to get an OIDC ID token. Receives a [tailcfg.TokenRequest].
		r.Post("/id-token", ns.NotImplementedHandler)

		// Asks the server if a feature is available and receive information about how to enable it.
		// Gets a [tailcfg.QueryFeatureRequest] and returns a [tailcfg.QueryFeatureResponse].
		r.Post("/feature/query", ns.NotImplementedHandler)

		r.Post("/update-health", ns.NotImplementedHandler)

		r.Route("/webclient", func(r chi.Router) {})

		r.Post("/c2n", ns.NotImplementedHandler)
	})

	ns.httpBaseConfig = &http.Server{
		Handler:           r,
		ReadHeaderTimeout: types.HTTPTimeout,
	}
	ns.http2Server = &http2.Server{}

	ns.http2Server.ServeConn(
		noiseConn,
		&http2.ServeConnOpts{
			BaseConfig: ns.httpBaseConfig,
		},
	)
}

func unsupportedClientError(version tailcfg.CapabilityVersion) error {
	return fmt.Errorf("%w: %s (%d)", ErrUnsupportedClientVersion, capver.TailscaleVersion(version), version)
}

func (ns *noiseServer) earlyNoise(protocolVersion int, writer io.Writer) error {
	if !isSupportedVersion(tailcfg.CapabilityVersion(protocolVersion)) {
		return unsupportedClientError(tailcfg.CapabilityVersion(protocolVersion))
	}

	earlyJSON, err := json.Marshal(&tailcfg.EarlyNoise{
		NodeKeyChallenge: ns.challenge.Public(),
	})
	if err != nil {
		return err
	}

	// 5 bytes that won't be mistaken for an HTTP/2 frame:
	// https://httpwg.org/specs/rfc7540.html#rfc.section.4.1 (Especially not
	// an HTTP/2 settings frame, which isn't of type 'T')
	var notH2Frame [5]byte
	copy(notH2Frame[:], earlyPayloadMagic)

	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(earlyJSON))) //nolint:gosec // JSON length is bounded
	// These writes are all buffered by caller, so fine to do them
	// separately:
	if _, err := writer.Write(notH2Frame[:]); err != nil { //nolint:noinlineerr
		return err
	}

	if _, err := writer.Write(lenBuf[:]); err != nil { //nolint:noinlineerr
		return err
	}

	if _, err := writer.Write(earlyJSON); err != nil { //nolint:noinlineerr
		return err
	}

	return nil
}

func isSupportedVersion(version tailcfg.CapabilityVersion) bool {
	return version >= capver.MinSupportedCapabilityVersion
}

func rejectUnsupported(
	writer http.ResponseWriter,
	version tailcfg.CapabilityVersion,
	mkey key.MachinePublic,
	nkey key.NodePublic,
) bool {
	// Reject unsupported versions
	if !isSupportedVersion(version) {
		log.Error().
			Caller().
			Int("minimum_cap_ver", int(capver.MinSupportedCapabilityVersion)).
			Int("client_cap_ver", int(version)).
			Str("minimum_version", capver.TailscaleVersion(capver.MinSupportedCapabilityVersion)).
			Str("client_version", capver.TailscaleVersion(version)).
			Str("node.key", nkey.ShortString()).
			Str("machine.key", mkey.ShortString()).
			Msg("unsupported client connected")
		http.Error(writer, unsupportedClientError(version).Error(), http.StatusBadRequest)

		return true
	}

	return false
}

func (ns *noiseServer) NotImplementedHandler(writer http.ResponseWriter, req *http.Request) {
	log.Trace().Caller().Str("path", req.URL.String()).Msg("not implemented handler hit")
	http.Error(writer, "Not implemented yet", http.StatusNotImplemented)
}

// PingResponseHandler handles HEAD requests from clients responding to a
// PingRequest. The client calls this endpoint to prove connectivity.
// The unguessable ping ID serves as authentication.
func (h *Headscale) PingResponseHandler(
	writer http.ResponseWriter,
	req *http.Request,
) {
	if req.Method != http.MethodHead {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	pingID := req.URL.Query().Get("id")
	if pingID == "" {
		http.Error(writer, "missing ping ID", http.StatusBadRequest)
		return
	}

	if h.state.CompletePing(pingID) {
		writer.WriteHeader(http.StatusOK)
	} else {
		http.Error(writer, "unknown or expired ping", http.StatusNotFound)
	}
}

func urlParam[T any](req *http.Request, key string) (T, error) {
	var zero T

	param := chi.URLParam(req, key)
	if param == "" {
		return zero, fmt.Errorf("%w: %s", ErrMissingURLParameter, key)
	}

	var value T
	switch any(value).(type) {
	case string:
		v, ok := any(param).(T)
		if !ok {
			return zero, fmt.Errorf("%w: %T", ErrUnsupportedURLParameterType, value)
		}

		value = v
	case types.NodeID:
		id, err := types.ParseNodeID(param)
		if err != nil {
			return zero, fmt.Errorf("parsing %s: %w", key, err)
		}

		v, ok := any(id).(T)
		if !ok {
			return zero, fmt.Errorf("%w: %T", ErrUnsupportedURLParameterType, value)
		}

		value = v
	default:
		return zero, fmt.Errorf("%w: %T", ErrUnsupportedURLParameterType, value)
	}

	return value, nil
}

// SSHActionHandler handles the /ssh-action endpoint, returning a
// [tailcfg.SSHAction] to the client with the verdict of an SSH access
// request.
func (ns *noiseServer) SSHActionHandler(
	writer http.ResponseWriter,
	req *http.Request,
) {
	srcNodeID, err := urlParam[types.NodeID](req, "src_node_id")
	if err != nil {
		httpError(writer, NewHTTPError(
			http.StatusBadRequest,
			"Invalid src_node_id",
			err,
		))

		return
	}

	dstNodeID, err := urlParam[types.NodeID](req, "dst_node_id")
	if err != nil {
		httpError(writer, NewHTTPError(
			http.StatusBadRequest,
			"Invalid dst_node_id",
			err,
		))

		return
	}

	// Authenticate the Noise session: the destination node is the
	// tailscaled instance asking us whether to permit an incoming SSH
	// connection, so its Noise session must belong to dst. Without this
	// check any unauthenticated client could open a Noise tunnel with a
	// throwaway machine key and ask for policy decisions about arbitrary
	// (src, dst) pairs.
	dstNode, ok := ns.headscale.state.GetNodeByID(dstNodeID)
	if !ok {
		httpError(writer, NewHTTPError(
			http.StatusNotFound,
			"dst node not found",
			fmt.Errorf("%w: %d", ErrSSHDstNodeNotFound, dstNodeID),
		))

		return
	}

	if dstNode.MachineKey() != ns.machineKey {
		httpError(writer, NewHTTPError(
			http.StatusUnauthorized,
			"machine key does not match dst node",
			fmt.Errorf(
				"%w: machine key %s, dst node %d",
				ErrSSHMachineKeyMismatch, ns.machineKey.ShortString(), dstNodeID,
			),
		))

		return
	}
	if !ns.hasAccountProof(dstNode.NodeKey(), dstNode.TypedUserID()) {
		httpError(writer, NewHTTPError(
			http.StatusUnauthorized,
			"account password proof required for this Noise session",
			nil,
		))

		return
	}

	reqLog := log.With().
		Uint64("src_node_id", srcNodeID.Uint64()).
		Uint64("dst_node_id", dstNodeID.Uint64()).
		Str("local_user", req.URL.Query().Get("local_user")).
		Logger()

	reqLog.Trace().Caller().Msg("SSH action request")

	action, err := ns.sshAction(
		req.Context(),
		reqLog,
		srcNodeID, dstNodeID,
		req.URL.Query().Get("auth_id"),
	)
	if err != nil {
		httpError(writer, err)

		return
	}

	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(http.StatusOK)

	err = json.NewEncoder(writer).Encode(action)
	if err != nil {
		reqLog.Error().Caller().Err(err).
			Msg("failed to encode SSH action response")

		return
	}

	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

// sshAction rejects policy check mode explicitly. ScaleTail has no separate
// browser approval identity. Treating a control-session password proof as an
// SSH step-up would erase the distinction between "accept" and "check".
func (ns *noiseServer) sshAction(
	_ context.Context,
	reqLog zerolog.Logger,
	srcNodeID, dstNodeID types.NodeID,
	_ string,
) (*tailcfg.SSHAction, error) {
	action := tailcfg.SSHAction{
		Reject:                    true,
		AllowAgentForwarding:      true,
		AllowLocalPortForwarding:  true,
		AllowRemotePortForwarding: true,
		Message:                   "ScaleTail SSH check mode is unavailable in account-password mode; use an explicit accept rule.",
	}
	reqLog.Warn().
		Uint64("src_node_id", srcNodeID.Uint64()).
		Uint64("dst_node_id", dstNodeID.Uint64()).
		Msg("rejected unsupported SSH check-mode request")

	return &action, nil
}

// PollNetMapHandler takes care of /machine/:id/map using the Noise protocol
//
// This is the busiest endpoint, as it keeps the HTTP long poll that updates
// the clients when something in the network changes.
//
// The clients POST stuff like HostInfo and their Endpoints here, but
// only after their first request (marked with the ReadOnly field).
//
// At this moment the updates are sent in a quite horrendous way, but they kinda work.
func (ns *noiseServer) PollNetMapHandler(
	writer http.ResponseWriter,
	req *http.Request,
) {
	var mapRequest tailcfg.MapRequest

	err := json.NewDecoder(req.Body).Decode(&mapRequest)
	if err != nil {
		httpError(writer, err)
		return
	}

	// Reject unsupported versions
	if rejectUnsupported(writer, mapRequest.Version, ns.machineKey, mapRequest.NodeKey) {
		return
	}

	nv, err := ns.getAndValidateNode(mapRequest)
	if err != nil {
		httpError(writer, err)
		return
	}
	if err := ns.requireMapAccountProof(req, nv); err != nil {
		httpError(writer, err)
		return
	}

	ns.nodeKey = nv.NodeKey()

	sess := ns.headscale.newMapSession(req.Context(), mapRequest, writer, nv.AsStruct())
	sess.log.Trace().Caller().Msg("a node sending a MapRequest with Noise protocol")

	if !sess.isStreaming() {
		sess.serve()
	} else {
		sess.serveLongPoll()
	}
}

func regErr(err error) *tailcfg.RegisterResponse {
	return &tailcfg.RegisterResponse{Error: err.Error()}
}

type passwordAuthRequest struct {
	AuthID   string `json:"authId"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type passwordAuthResponse struct {
	Status  string `json:"status,omitempty"`
	Code    string `json:"code,omitempty"`
	Error   string `json:"error,omitempty"`
	Warning string `json:"warning,omitempty"`
}

const passwordAuthRequestBodyLimit = 8 << 10

func writePasswordAuthResponse(
	writer http.ResponseWriter,
	status int,
	response passwordAuthResponse,
) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(response); err != nil {
		log.Error().Err(err).Msg("failed to encode password authentication response")
	}
}

func passwordAuthServerURLIsTrusted(serverURL string) bool {
	return types.AccountPasswordServerURLIsTrusted(serverURL)
}

func trustedProxyAddress(address netip.Addr, prefixes []netip.Prefix) bool {
	address = address.Unmap()
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}

	return false
}

func noiseAuthSource(req *http.Request, trustedProxyCIDRs []netip.Prefix) string {
	direct := passwordAuthSource(req.RemoteAddr)
	directAddress, err := netip.ParseAddr(direct)
	if err != nil || !trustedProxyAddress(directAddress, trustedProxyCIDRs) {
		return direct
	}

	if value := strings.TrimSpace(req.Header.Get("X-Real-IP")); value != "" {
		if ip := net.ParseIP(value); ip != nil {
			return ip.String()
		}
	}

	forwarded := strings.Split(req.Header.Get("X-Forwarded-For"), ",")
	for idx := len(forwarded) - 1; idx >= 0; idx-- {
		if address, err := netip.ParseAddr(strings.TrimSpace(forwarded[idx])); err == nil {
			if !trustedProxyAddress(address, trustedProxyCIDRs) {
				return address.Unmap().String()
			}
		}
	}

	return direct
}

// PasswordAuthHandler authenticates a pending registration using the single
// account credential. It is only reachable inside the machine's Noise session;
// the auth ID is additionally bound to that session's machine key.
func (ns *noiseServer) PasswordAuthHandler(writer http.ResponseWriter, req *http.Request) {
	if !passwordAuthServerURLIsTrusted(ns.headscale.cfg.ServerURL) {
		writePasswordAuthResponse(writer, http.StatusUpgradeRequired, passwordAuthResponse{
			Code:  "https_required",
			Error: "password authentication requires a trusted HTTPS control server",
		})
		return
	}

	req.Body = http.MaxBytesReader(writer, req.Body, passwordAuthRequestBodyLimit)
	decoder := json.NewDecoder(req.Body)
	decoder.DisallowUnknownFields()
	var authReq passwordAuthRequest
	if err := decoder.Decode(&authReq); err != nil {
		writePasswordAuthResponse(writer, http.StatusBadRequest, passwordAuthResponse{
			Code:  "invalid_request",
			Error: "invalid password authentication request",
		})
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writePasswordAuthResponse(writer, http.StatusBadRequest, passwordAuthResponse{
			Code:  "invalid_request",
			Error: "authentication request must contain one JSON value",
		})
		return
	}

	if strings.TrimSpace(authReq.Username) == "" || authReq.Password == "" ||
		len(authReq.Username) > 255 || len([]byte(authReq.Password)) > 72 {
		writePasswordAuthResponse(writer, http.StatusBadRequest, passwordAuthResponse{
			Code:  "invalid_request",
			Error: "invalid username or password length",
		})
		return
	}
	if authReq.AuthID == "" {
		ns.reauthenticateExistingNode(req.Context(), writer, authReq)
		return
	}

	authID, err := types.AuthIDFromString(authReq.AuthID)
	if err != nil {
		writePasswordAuthResponse(writer, http.StatusBadRequest, passwordAuthResponse{
			Code:  "invalid_auth_session",
			Error: "invalid registration session",
		})
		return
	}

	entry, ok := ns.headscale.state.GetAuthCacheEntry(authID)
	if !ok {
		writePasswordAuthResponse(writer, http.StatusNotFound, passwordAuthResponse{
			Code:  "auth_session_expired",
			Error: "registration session has expired",
		})
		return
	}
	regData, ok := entry.RegistrationDataOK()
	if !ok {
		writePasswordAuthResponse(writer, http.StatusBadRequest, passwordAuthResponse{
			Code:  "invalid_auth_session",
			Error: "authentication session is not a node registration",
		})
		return
	}
	if regData.MachineKey != ns.machineKey {
		writePasswordAuthResponse(writer, http.StatusUnauthorized, passwordAuthResponse{
			Code:  "machine_mismatch",
			Error: "registration session belongs to another machine",
		})
		return
	}
	if regData.Hostinfo != nil && len(regData.Hostinfo.RequestTags) > 0 {
		writePasswordAuthResponse(writer, http.StatusBadRequest, passwordAuthResponse{
			Code:  "tags_not_supported",
			Error: "account-authenticated nodes cannot use identity tags",
		})
		return
	}

	now := time.Now().UTC()
	account, err := ns.authenticateAccount(
		req.Context(),
		authReq.Username,
		authReq.Password,
		now,
	)
	if err != nil {
		status, code, message := passwordAuthFailure(err)
		writePasswordAuthResponse(writer, status, passwordAuthResponse{Code: code, Error: message})
		return
	}
	if account == nil || account.UserID == nil {
		writePasswordAuthResponse(writer, http.StatusForbidden, passwordAuthResponse{
			Code:  "network_not_assigned",
			Error: "account is not assigned to a network",
		})
		return
	}
	account, unlockAccount, err := ns.headscale.state.BeginAccountAuthentication(
		account.ID,
		account.PasswordVersion,
		now,
	)
	if err != nil {
		status, code, message := passwordAuthFailure(err)
		writePasswordAuthResponse(writer, status, passwordAuthResponse{Code: code, Error: message})
		return
	}
	defer unlockAccount()

	nodeExpiry := account.PasswordChangedAt.Add(types.AccountPasswordMaxAge)
	if account.ExpiresAt != nil && account.ExpiresAt.Before(nodeExpiry) {
		nodeExpiry = *account.ExpiresAt
	}

	node, nodeChange, err := ns.headscale.state.HandleNodeFromAuthPath(
		authID,
		types.UserID(*account.UserID),
		&nodeExpiry,
		util.RegisterMethodPassword,
	)
	if err != nil {
		status, code, message := http.StatusInternalServerError, "registration_failed", "node registration failed"
		switch {
		case errors.Is(err, state.ErrAuthRequestAlreadyClaimed):
			status, code, message = http.StatusConflict, "auth_session_consumed", "registration session was already used"
		case errors.Is(err, state.ErrAuthRequestNotRegistration):
			status, code, message = http.StatusBadRequest, "invalid_auth_session", "authentication session is not a node registration"
		case errors.Is(err, state.ErrAccountNodeLimitReached):
			status, code, message = http.StatusConflict, "node_limit_reached", "this account has reached its node limit"
		case errors.Is(err, hsdb.ErrNodeNotFoundRegistrationCache):
			status, code, message = http.StatusNotFound, "auth_session_expired", "registration session has expired"
		default:
			log.Error().Err(err).Msg("registering password-authenticated node")
		}

		writePasswordAuthResponse(writer, status, passwordAuthResponse{Code: code, Error: message})
		return
	}

	routesChange, err := ns.headscale.state.AutoApproveRoutes(node)
	if err != nil {
		ns.headscale.Change(nodeChange)
		log.Error().Err(err).Msg("auto approving routes for password-authenticated node")
		ns.rememberAccountProof(account, node.NodeKey(), types.UserID(*account.UserID))
		writePasswordAuthResponse(writer, http.StatusOK, passwordAuthResponse{
			Status:  "authenticated",
			Warning: "node connected but automatic route approval failed",
		})
		return
	}

	ns.headscale.Change(nodeChange, routesChange)
	ns.rememberAccountProof(account, node.NodeKey(), types.UserID(*account.UserID))
	writePasswordAuthResponse(writer, http.StatusOK, passwordAuthResponse{Status: "authenticated"})
}

func accountNodeExpiry(account *types.Account) time.Time {
	expiry := account.PasswordChangedAt.Add(types.AccountPasswordMaxAge)
	if account.ExpiresAt != nil && account.ExpiresAt.Before(expiry) {
		expiry = *account.ExpiresAt
	}
	return expiry
}

func passwordAuthFailure(err error) (int, string, string) {
	status, code, message := http.StatusUnauthorized, "invalid_credentials", "invalid username or password"
	switch {
	case errors.Is(err, errPasswordAuthRateLimited):
		status, code, message = http.StatusTooManyRequests, "too_many_attempts", "too many authentication attempts"
	case errors.Is(err, hsdb.ErrAccountDisabled):
		status, code, message = http.StatusForbidden, "account_disabled", "account is disabled"
	case errors.Is(err, hsdb.ErrAccountExpired):
		status, code, message = http.StatusForbidden, "account_expired", "account is expired"
	case errors.Is(err, hsdb.ErrAccountPasswordExpired):
		status, code, message = http.StatusForbidden, "password_expired", "password must be changed"
	case errors.Is(err, hsdb.ErrAccountConcurrentUpdate):
		status, code, message = http.StatusConflict, "account_changed", "account changed during authentication; retry"
	case !errors.Is(err, hsdb.ErrAccountInvalidCredentials):
		log.Error().Err(err).Msg("password authentication failed")
		status, code, message = http.StatusInternalServerError, "internal_error", "authentication failed"
	}
	return status, code, message
}

func (ns *noiseServer) reauthenticateExistingNode(
	ctx context.Context,
	writer http.ResponseWriter,
	authReq passwordAuthRequest,
) {
	now := time.Now().UTC()
	account, err := ns.authenticateAccount(ctx, authReq.Username, authReq.Password, now)
	if err != nil {
		status, code, message := passwordAuthFailure(err)
		writePasswordAuthResponse(writer, status, passwordAuthResponse{Code: code, Error: message})
		return
	}
	if account == nil || account.UserID == nil {
		writePasswordAuthResponse(writer, http.StatusForbidden, passwordAuthResponse{
			Code: "network_not_assigned", Error: "account is not assigned to a network",
		})
		return
	}
	account, unlockAccount, err := ns.headscale.state.BeginAccountAuthentication(
		account.ID,
		account.PasswordVersion,
		now,
	)
	if err != nil {
		status, code, message := passwordAuthFailure(err)
		writePasswordAuthResponse(writer, status, passwordAuthResponse{Code: code, Error: message})
		return
	}
	defer unlockAccount()
	node, ok := ns.headscale.state.GetNodeByMachineKey(ns.machineKey, types.UserID(*account.UserID))
	if !ok || node.IsTagged() || node.RegisterMethod() != util.RegisterMethodPassword {
		writePasswordAuthResponse(writer, http.StatusNotFound, passwordAuthResponse{
			Code: "auth_session_expired", Error: "device must complete account registration",
		})
		return
	}
	expiry := accountNodeExpiry(account)
	updated, nodeChange, err := ns.headscale.state.SetNodeExpiry(node.ID(), &expiry)
	if err != nil {
		writePasswordAuthResponse(writer, http.StatusInternalServerError, passwordAuthResponse{
			Code: "internal_error", Error: "failed to renew device authentication",
		})
		return
	}
	ns.headscale.Change(nodeChange)
	ns.rememberAccountProof(account, updated.NodeKey(), types.UserID(*account.UserID))
	writePasswordAuthResponse(writer, http.StatusOK, passwordAuthResponse{Status: "authenticated"})
}

func (ns *noiseServer) rememberAccountProof(
	account *types.Account,
	nodeKey key.NodePublic,
	userID types.UserID,
) {
	if account == nil {
		return
	}
	ns.accountAuthMu.Lock()
	defer ns.accountAuthMu.Unlock()
	ns.accountAuthNodeKey = nodeKey
	ns.accountAuthUserID = userID
	ns.accountAuthID = account.ID
	ns.accountAuthVersion = account.PasswordVersion
	ns.accountAuthExpiry = accountNodeExpiry(account)
}

func (ns *noiseServer) hasAccountProof(nodeKey key.NodePublic, userID types.UserID) bool {
	if nodeKey.IsZero() || userID == 0 {
		return false
	}
	ns.accountAuthMu.Lock()
	proofNodeKey := ns.accountAuthNodeKey
	proofUserID := ns.accountAuthUserID
	proofAccountID := ns.accountAuthID
	proofVersion := ns.accountAuthVersion
	proofExpiry := ns.accountAuthExpiry
	ns.accountAuthMu.Unlock()

	now := time.Now().UTC()
	if proofNodeKey != nodeKey || proofUserID != userID || proofAccountID == 0 ||
		proofVersion == 0 || !proofExpiry.After(now) {
		return false
	}
	account, err := ns.headscale.state.GetAccountByID(proofAccountID)
	if err != nil || account == nil || !account.Enabled || account.PasswordExpired(now) ||
		account.PasswordVersion != proofVersion || account.UserID == nil ||
		types.UserID(*account.UserID) != proofUserID {
		return false
	}
	if account.ExpiresAt != nil && !account.ExpiresAt.After(now) {
		return false
	}

	return true
}

func (ns *noiseServer) requireMapAccountProof(req *http.Request, node types.NodeView) error {
	userID := node.TypedUserID()
	if userID == 0 || node.IsTagged() || node.RegisterMethod() != util.RegisterMethodPassword {
		return NewHTTPError(http.StatusUnauthorized, "account-authenticated node required", nil)
	}

	username := strings.TrimSpace(req.Header.Get(accountUsernameHeader))
	password := req.Header.Get(accountPasswordHeader)
	req.Header.Del(accountUsernameHeader)
	req.Header.Del(accountPasswordHeader)
	if ns.hasAccountProof(node.NodeKey(), userID) {
		return nil
	}
	if username == "" || password == "" || len(username) > 255 || len([]byte(password)) > 72 {
		return NewHTTPError(http.StatusUnauthorized, "account password proof required", nil)
	}

	account, err := ns.authenticateAccount(req.Context(), username, password, time.Now().UTC())
	if errors.Is(err, errPasswordAuthRateLimited) {
		return NewHTTPError(http.StatusTooManyRequests, "password authentication rate limited", nil)
	}
	if err != nil || account == nil || account.UserID == nil || types.UserID(*account.UserID) != userID {
		return NewHTTPError(http.StatusUnauthorized, "account password proof rejected", nil)
	}
	account, unlockAccount, err := ns.headscale.state.BeginAccountAuthentication(
		account.ID,
		account.PasswordVersion,
		time.Now().UTC(),
	)
	if err != nil {
		return NewHTTPError(http.StatusUnauthorized, "account password proof rejected", nil)
	}
	defer unlockAccount()
	if account.UserID == nil || types.UserID(*account.UserID) != userID {
		return NewHTTPError(http.StatusUnauthorized, "account password proof rejected", nil)
	}
	ns.rememberAccountProof(account, node.NodeKey(), userID)
	return nil
}

// RegistrationHandler handles the actual registration process of a node.
func (ns *noiseServer) RegistrationHandler(
	writer http.ResponseWriter,
	req *http.Request,
) {
	if req.Method != http.MethodPost {
		httpError(writer, errMethodNotAllowed)

		return
	}

	registerRequest, registerResponse := func() (*tailcfg.RegisterRequest, *tailcfg.RegisterResponse) { //nolint:contextcheck
		var resp *tailcfg.RegisterResponse

		var regReq tailcfg.RegisterRequest

		err := json.NewDecoder(req.Body).Decode(&regReq)
		if err != nil {
			return &regReq, regErr(err)
		}

		ns.nodeKey = regReq.NodeKey

		resp, err = ns.headscale.handleRegister(req.Context(), regReq, ns.conn.Peer(), ns.authSource)
		if err != nil {
			if httpErr, ok := errors.AsType[HTTPError](err); ok {
				resp = &tailcfg.RegisterResponse{
					Error: httpErr.Msg,
				}

				return &regReq, resp
			}

			return &regReq, regErr(err)
		}

		return &regReq, resp
	}()

	// Reject unsupported versions
	if rejectUnsupported(writer, registerRequest.Version, ns.machineKey, registerRequest.NodeKey) {
		return
	}

	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(http.StatusOK)

	err := json.NewEncoder(writer).Encode(registerResponse)
	if err != nil {
		log.Error().Caller().Err(err).Msg("noise registration handler: failed to encode RegisterResponse")
		return
	}

	// Ensure response is flushed to client
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

// getAndValidateNode retrieves the node from the database using the NodeKey
// and validates that it matches the MachineKey from the Noise session.
func (ns *noiseServer) getAndValidateNode(mapRequest tailcfg.MapRequest) (types.NodeView, error) {
	nv, ok := ns.headscale.state.GetNodeByNodeKey(mapRequest.NodeKey)
	if !ok {
		return types.NodeView{}, NewHTTPError(http.StatusNotFound, "node not found", nil)
	}

	// Validate that the MachineKey in the Noise session matches the one associated with the NodeKey.
	if ns.machineKey != nv.MachineKey() {
		return types.NodeView{}, NewHTTPError(http.StatusNotFound, "node key in request does not match the one associated with this machine key", nil)
	}
	if nv.IsExpired() {
		return types.NodeView{}, NewHTTPError(http.StatusUnauthorized, "node authentication has expired", nil)
	}

	return nv, nil
}
