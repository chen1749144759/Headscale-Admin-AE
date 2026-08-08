package hscontrol

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	internalAuthVersion     = "1"
	internalAuthClockSkew   = 60 * time.Second
	internalAuthReplayLimit = 8192
	internalAuthBodyLimit   = 1 << 20
)

var internalAuthContextHeaders = []string{
	"Authorization",
	"X-ScaleForge-Node-ID",
	"X-ScaleForge-Source",
	"X-ScaleForge-Source-IP",
	"X-ScaleForge-User-ID",
}

var errInternalAuthentication = errors.New("invalid internal request authentication")

type internalAuthReplayCache struct {
	mu      sync.Mutex
	entries map[string]time.Time
	max     int
}

func newInternalAuthReplayCache(maxEntries int) *internalAuthReplayCache {
	return &internalAuthReplayCache{entries: make(map[string]time.Time), max: maxEntries}
}

func (cache *internalAuthReplayCache) accept(nonce string, now time.Time) bool {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cutoff := now.Add(-internalAuthClockSkew)
	for key, seenAt := range cache.entries {
		if seenAt.Before(cutoff) {
			delete(cache.entries, key)
		}
	}
	if _, exists := cache.entries[nonce]; exists {
		return false
	}
	for len(cache.entries) >= cache.max {
		var oldestKey string
		var oldestTime time.Time
		for key, seenAt := range cache.entries {
			if oldestKey == "" || seenAt.Before(oldestTime) {
				oldestKey, oldestTime = key, seenAt
			}
		}
		delete(cache.entries, oldestKey)
	}
	cache.entries[nonce] = now

	return true
}

func readInternalAuthKey(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("scaleforge.internal_auth_key_file is required")
	}
	value, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading internal authentication key: %w", err)
	}
	value = bytes.TrimSpace(value)
	if len(value) < 32 || len(value) > 4096 {
		return nil, errors.New("internal authentication key must contain 32 to 4096 bytes")
	}

	return value, nil
}

func internalAuthCanonical(req *http.Request, body []byte, timestamp, nonce string) string {
	digest := sha256.Sum256(body)
	contextMAC := sha256.New()
	for _, name := range internalAuthContextHeaders {
		_, _ = io.WriteString(contextMAC, strings.ToLower(name)+":"+strings.TrimSpace(req.Header.Get(name))+"\n")
	}
	return strings.Join([]string{
		strings.ToUpper(req.Method),
		req.URL.EscapedPath(),
		req.URL.RawQuery,
		timestamp,
		nonce,
		hex.EncodeToString(digest[:]),
		hex.EncodeToString(contextMAC.Sum(nil)),
	}, "\n")
}

func signScaleForgeInternalRequest(req *http.Request, body, key []byte, now time.Time) error {
	if len(key) < 32 {
		return errInternalAuthentication
	}
	var nonceBytes [16]byte
	if _, err := rand.Read(nonceBytes[:]); err != nil {
		return fmt.Errorf("generating internal request nonce: %w", err)
	}
	timestamp := strconv.FormatInt(now.Unix(), 10)
	nonce := hex.EncodeToString(nonceBytes[:])
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, internalAuthCanonical(req, body, timestamp, nonce))
	req.Header.Set("X-ScaleForge-Auth-Version", internalAuthVersion)
	req.Header.Set("X-ScaleForge-Auth-Timestamp", timestamp)
	req.Header.Set("X-ScaleForge-Auth-Nonce", nonce)
	req.Header.Set("X-ScaleForge-Auth-Signature", hex.EncodeToString(mac.Sum(nil)))

	return nil
}

func verifyScaleForgeInternalRequest(
	req *http.Request,
	key []byte,
	replay *internalAuthReplayCache,
	now time.Time,
) error {
	if len(key) < 32 || replay == nil ||
		req.Header.Get("X-ScaleForge-Auth-Version") != internalAuthVersion {
		return errInternalAuthentication
	}
	timestamp := strings.TrimSpace(req.Header.Get("X-ScaleForge-Auth-Timestamp"))
	nonce := strings.TrimSpace(req.Header.Get("X-ScaleForge-Auth-Nonce"))
	signature := strings.TrimSpace(req.Header.Get("X-ScaleForge-Auth-Signature"))
	unixTime, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || len(nonce) != 32 || len(signature) != sha256.Size*2 {
		return errInternalAuthentication
	}
	if _, err := hex.DecodeString(nonce); err != nil {
		return errInternalAuthentication
	}
	signedAt := time.Unix(unixTime, 0)
	if now.Sub(signedAt) > internalAuthClockSkew || signedAt.Sub(now) > internalAuthClockSkew {
		return errInternalAuthentication
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, internalAuthBodyLimit+1))
	if err != nil || len(body) > internalAuthBodyLimit {
		return errInternalAuthentication
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, internalAuthCanonical(req, body, timestamp, nonce))
	wantSignature, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal(wantSignature, mac.Sum(nil)) {
		return errInternalAuthentication
	}
	if !replay.accept(nonce, now) {
		return errInternalAuthentication
	}

	return nil
}
