package hscontrol

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	hsdb "github.com/juanfont/headscale/hscontrol/db"
	"github.com/juanfont/headscale/hscontrol/types"
	"gorm.io/gorm"
	"tailscale.com/types/key"
)

func TestPasswordChangeHandlerCompletesTemporaryPasswordRotation(t *testing.T) {
	app := newScaleForgeAPITestHeadscale(t)
	account, err := app.state.CreateAccount(hsdb.CreateAccountParams{
		Username:              "noise-temporary-user",
		Password:              "temporary correct password",
		Role:                  types.AccountRoleManager,
		Enabled:               true,
		RequirePasswordChange: true,
	})
	if err != nil {
		t.Fatalf("creating temporary account: %v", err)
	}

	machineKey := key.NewMachine().Public()
	authID := types.MustAuthID()
	app.state.SetAuthCacheEntry(authID, types.NewRegisterAuthRequest(&types.RegistrationData{
		MachineKey: machineKey,
		NodeKey:    key.NewNode().Public(),
		DiscoKey:   key.NewDisco().Public(),
		Hostname:   "first-login-client",
	}))
	ns := &noiseServer{
		headscale:  app,
		machineKey: machineKey,
		authSource: "192.0.2.44",
	}
	body := fmt.Sprintf(`{
		"authId":%q,
		"username":"noise-temporary-user",
		"currentPassword":"temporary correct password",
		"newPassword":"new correct horse password"
	}`, authID.String())
	req := httptest.NewRequest(http.MethodPut, "https://unused/machine/auth/password", bytes.NewBufferString(body))
	res := httptest.NewRecorder()
	ns.PasswordChangeHandler(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body = %s", res.Code, http.StatusNoContent, res.Body.String())
	}

	if _, err := app.state.AuthenticateAccount(
		account.Username,
		"temporary correct password",
		time.Now().UTC(),
	); !errors.Is(err, hsdb.ErrAccountInvalidCredentials) {
		t.Fatalf("temporary password error = %v, want %v", err, hsdb.ErrAccountInvalidCredentials)
	}
	updated, err := app.state.AuthenticateAccount(
		account.Username,
		"new correct horse password",
		time.Now().UTC(),
	)
	if err != nil || updated.MustChangePassword {
		t.Fatalf("new password authentication = account %+v, err %v", updated, err)
	}
}

func TestPasswordChangeHandlerRejectsInvalidNewPassword(t *testing.T) {
	t.Parallel()

	ns := &noiseServer{}
	req := httptest.NewRequest(http.MethodPut, "https://unused/machine/auth/password", bytes.NewBufferString(`{
		"authId":"hskey-authreq-AbCdEfGhIjKlMnOpQrStUvWx",
		"username":"alice",
		"currentPassword":"temporary correct password",
		"newPassword":"short"
	}`))
	res := httptest.NewRecorder()
	ns.PasswordChangeHandler(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", res.Code, http.StatusBadRequest, res.Body.String())
	}
	if got := res.Body.String(); !bytes.Contains([]byte(got), []byte(`"code":"invalid_password"`)) {
		t.Fatalf("response = %s, want invalid_password", got)
	}
}

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
