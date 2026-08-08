package hscontrol

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestScaleForgeInternalRequestAuthentication(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	body := []byte(`{"machine":7}`)
	req, err := http.NewRequest(http.MethodPost, "http://internal/v1/session?mode=test", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}
	now := time.Unix(1_800_000_000, 0)
	if err := signScaleForgeInternalRequest(req, body, key, now); err != nil {
		t.Fatalf("signing request: %v", err)
	}
	if err := verifyScaleForgeInternalRequest(
		req,
		key,
		newInternalAuthReplayCache(8),
		now,
	); err != nil {
		t.Fatalf("verifying signed request: %v", err)
	}
}

func TestScaleForgeInternalRequestRejectsReplayAndTampering(t *testing.T) {
	key := bytes.Repeat([]byte{0x24}, 32)
	body := []byte("payload")
	req, err := http.NewRequest(http.MethodPost, "http://internal/v1/session", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}
	now := time.Unix(1_800_000_000, 0)
	if err := signScaleForgeInternalRequest(req, body, key, now); err != nil {
		t.Fatalf("signing request: %v", err)
	}
	replay := newInternalAuthReplayCache(8)
	first := req.Clone(req.Context())
	first.Body = io.NopCloser(bytes.NewReader(body))
	if err := verifyScaleForgeInternalRequest(first, key, replay, now); err != nil {
		t.Fatalf("first verification failed: %v", err)
	}
	repeated := req.Clone(req.Context())
	repeated.Body = io.NopCloser(bytes.NewReader(body))
	if err := verifyScaleForgeInternalRequest(repeated, key, replay, now); err == nil {
		t.Fatal("replayed request was accepted")
	}
	tampered := req.Clone(req.Context())
	tampered.Body = io.NopCloser(bytes.NewReader(append(body, '!')))
	if err := verifyScaleForgeInternalRequest(
		tampered,
		key,
		newInternalAuthReplayCache(8),
		now,
	); err == nil {
		t.Fatal("tampered body was accepted")
	}
	tamperedIdentity := req.Clone(req.Context())
	tamperedIdentity.Body = io.NopCloser(bytes.NewReader(body))
	tamperedIdentity.Header.Set("X-ScaleForge-User-ID", "12")
	if err := verifyScaleForgeInternalRequest(
		tamperedIdentity,
		key,
		newInternalAuthReplayCache(8),
		now,
	); err == nil {
		t.Fatal("tampered identity header was accepted")
	}
}

func TestScaleForgeInternalAuthenticationCanonicalVector(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	body := []byte(`{"rx":123}`)
	req, err := http.NewRequest(
		http.MethodPost,
		"http://internal/internal/v1/client/traffic?a=1&b=2",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer session-token")
	req.Header.Set("X-ScaleForge-Node-ID", "7")
	req.Header.Set("X-ScaleForge-Source", "198.51.100.8")
	req.Header.Set("X-ScaleForge-Source-IP", "203.0.113.9")
	req.Header.Set("X-ScaleForge-User-ID", "11")
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, internalAuthCanonical(
		req,
		body,
		"1800000000",
		"00112233445566778899aabbccddeeff",
	))
	if got, want := hex.EncodeToString(mac.Sum(nil)), "f45999ddd2efe3c9fd7805a8ef6d130a529a2e2e071a9ca85dccbe753f2008e6"; got != want {
		t.Fatalf("signature = %s, want %s", got, want)
	}
}
