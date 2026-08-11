package hscontrol

import (
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/juanfont/headscale/hscontrol/types"
	"gorm.io/gorm"
	"tailscale.com/types/key"
)

func TestNoiseAccountProofIsScopedToNodeAndSession(t *testing.T) {
	t.Parallel()

	firstNode := key.NewNode().Public()
	secondNode := key.NewNode().Public()
	ns := &noiseServer{}
	if ns.hasAccountProof(firstNode, types.UserID(7)) {
		t.Fatal("empty Noise session unexpectedly has account proof")
	}
	account := &types.Account{
		Model:             gorm.Model{ID: 11},
		UserID:            new(uint),
		Enabled:           true,
		PasswordChangedAt: time.Now().UTC(),
		PasswordVersion:   3,
	}
	*account.UserID = 7
	ns.rememberAccountProof(account, firstNode, types.UserID(7))
	if ns.accountAuthID != account.ID || ns.accountAuthVersion != account.PasswordVersion ||
		ns.accountAuthNodeKey != firstNode || ns.accountAuthUserID != types.UserID(7) ||
		!ns.accountAuthExpiry.After(time.Now().UTC()) {
		t.Fatal("account proof did not retain the complete credential snapshot")
	}
	if ns.hasAccountProof(secondNode, types.UserID(7)) {
		t.Fatal("account proof leaked to another node")
	}
	if ns.hasAccountProof(firstNode, types.UserID(8)) {
		t.Fatal("account proof leaked to another network account")
	}
	otherSession := &noiseServer{}
	if otherSession.hasAccountProof(firstNode, types.UserID(7)) {
		t.Fatal("account proof leaked to another Noise session")
	}
}

func TestNoiseAuthSource(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("GET", "http://headscale.test/ts2021", nil)
	req.RemoteAddr = "172.18.0.1:54321"
	req.Header.Set("X-Real-IP", "203.0.113.9")
	req.Header.Set("X-Forwarded-For", "198.51.100.7, 203.0.113.8")
	trusted := []netip.Prefix{netip.MustParsePrefix("172.18.0.0/16")}

	if got, want := noiseAuthSource(req, nil), "172.18.0.1"; got != want {
		t.Fatalf("untrusted proxy source = %q, want %q", got, want)
	}
	if got, want := noiseAuthSource(req, trusted), "203.0.113.9"; got != want {
		t.Fatalf("trusted proxy source = %q, want %q", got, want)
	}

	req.Header.Del("X-Real-IP")
	if got, want := noiseAuthSource(req, trusted), "203.0.113.8"; got != want {
		t.Fatalf("forwarded source = %q, want %q", got, want)
	}
	req.RemoteAddr = "198.51.100.9:54321"
	if got, want := noiseAuthSource(req, trusted), "198.51.100.9"; got != want {
		t.Fatalf("untrusted direct peer source = %q, want %q", got, want)
	}
}
