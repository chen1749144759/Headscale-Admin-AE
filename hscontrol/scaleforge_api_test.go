package hscontrol

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hsdb "github.com/juanfont/headscale/hscontrol/db"
	"github.com/juanfont/headscale/hscontrol/state"
	"github.com/juanfont/headscale/hscontrol/types"
)

func newScaleForgeAPITestHeadscale(t *testing.T) *Headscale {
	t.Helper()
	prefixV4 := netip.MustParsePrefix("100.64.0.0/10")
	prefixV6 := netip.MustParsePrefix("fd7a:115c:a1e0::/48")
	appState, err := state.NewState(&types.Config{
		ServerURL:    "http://localhost:0",
		PrefixV4:     &prefixV4,
		PrefixV6:     &prefixV6,
		IPAllocation: types.IPAllocationStrategySequential,
		Database: types.DatabaseConfig{
			Type:   types.DatabaseSqlite,
			Sqlite: types.SqliteConfig{Path: filepath.Join(t.TempDir(), "headscale.sqlite")},
		},
		Policy: types.PolicyConfig{Mode: types.PolicyModeDB},
	})
	if err != nil {
		t.Fatalf("creating test state: %v", err)
	}
	t.Cleanup(func() {
		if err := appState.Close(); err != nil {
			t.Errorf("closing test state: %v", err)
		}
	})

	return &Headscale{
		state:             appState,
		scaleForgeAuthKey: bytes.Repeat([]byte{0x42}, 32),
		scaleForgeReplay:  newInternalAuthReplayCache(internalAuthReplayLimit),
	}
}

func signScaleForgeAPITestRequest(t *testing.T, app *Headscale, req *http.Request) {
	t.Helper()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("reading test request body: %v", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	if err := signScaleForgeInternalRequest(req, body, app.scaleForgeAuthKey, time.Now().UTC()); err != nil {
		t.Fatalf("signing test request: %v", err)
	}
}

func TestBootstrapScaleForgeAccountRejectsExistingAccountsWithoutDurableManager(t *testing.T) {
	app := newScaleForgeAPITestHeadscale(t)
	group, err := app.state.CreateAccountGroup("ordinary-group")
	if err != nil {
		t.Fatalf("creating account group: %v", err)
	}
	if _, err := app.state.CreateAccount(hsdb.CreateAccountParams{
		Username: "ordinary-user",
		Password: "correct horse battery staple",
		GroupID:  &group.ID,
		Role:     types.AccountRoleUser,
		Enabled:  true,
	}); err != nil {
		t.Fatalf("creating ordinary account: %v", err)
	}

	err = bootstrapScaleForgeAccount(&types.Config{}, app.state)
	if !errors.Is(err, hsdb.ErrLastManager) {
		t.Fatalf("bootstrap error = %v, want %v", err, hsdb.ErrLastManager)
	}
}

func TestBootstrapScaleForgeAccountRecoversExistingAccounts(t *testing.T) {
	app := newScaleForgeAPITestHeadscale(t)
	group, err := app.state.CreateAccountGroup("ordinary-group")
	if err != nil {
		t.Fatalf("creating account group: %v", err)
	}
	if _, err := app.state.CreateAccount(hsdb.CreateAccountParams{
		Username: "ordinary-user",
		Password: "correct horse battery staple",
		GroupID:  &group.ID,
		Role:     types.AccountRoleUser,
		Enabled:  true,
	}); err != nil {
		t.Fatalf("creating ordinary account: %v", err)
	}
	passwordFile := filepath.Join(t.TempDir(), "bootstrap-password")
	if err := os.WriteFile(passwordFile, []byte("bootstrap correct horse battery staple\n"), 0o600); err != nil {
		t.Fatalf("writing bootstrap password: %v", err)
	}
	cfg := &types.Config{ScaleForge: types.ScaleForgeConfig{
		BootstrapUsername:     "recovery-manager",
		BootstrapPasswordFile: passwordFile,
	}}
	if err := bootstrapScaleForgeAccount(cfg, app.state); err != nil {
		t.Fatalf("recovering manager account: %v", err)
	}
	if err := app.state.ValidateManagerAccountInvariant(); err != nil {
		t.Fatalf("manager invariant after recovery: %v", err)
	}
}

func TestScaleForgeLoginAndPasswordChangeUseUsernameAttemptWindow(t *testing.T) {
	app := newScaleForgeAPITestHeadscale(t)
	username := "rate-limited-manager"
	password := "correct horse battery staple"
	manager, err := app.state.CreateAccount(hsdb.CreateAccountParams{
		Username: username,
		Password: password,
		Role:     types.AccountRoleManager,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("creating manager: %v", err)
	}
	now := time.Now().UTC()
	token, _, err := app.state.CreateAccountSession(manager, false, now)
	if err != nil {
		t.Fatalf("creating manager session: %v", err)
	}
	source := "203.0.113.10"
	rateLimitSource := "scaleforge:" + source
	key := rateLimitSource + "\x00" + types.NormalizeAccountUsername(username)
	passwordAuthGuard.Lock()
	delete(passwordAuthGuard.entries, key)
	passwordAuthGuard.Unlock()
	t.Cleanup(func() {
		passwordAuthGuard.Lock()
		delete(passwordAuthGuard.entries, key)
		passwordAuthGuard.Unlock()
	})
	for range passwordAuthWindowLimit {
		if !allowPasswordAuthentication(rateLimitSource, username, now) {
			t.Fatal("attempt window closed before reaching its limit")
		}
	}

	server := app.newScaleForgeAPIServer(http.NotFoundHandler())
	loginRequest := httptest.NewRequest(
		http.MethodPost,
		"/v1/sessions",
		strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`),
	)
	loginRequest.Header.Set("X-ScaleForge-Source", source)
	signScaleForgeAPITestRequest(t, app, loginRequest)
	loginResponse := httptest.NewRecorder()
	server.Handler.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusTooManyRequests {
		t.Fatalf("login status = %d, want %d; body = %s", loginResponse.Code, http.StatusTooManyRequests, loginResponse.Body.String())
	}

	passwordRequest := httptest.NewRequest(
		http.MethodPut,
		"/v1/session/password",
		strings.NewReader(`{"currentPassword":"`+password+`","newPassword":"another correct password"}`),
	)
	passwordRequest.Header.Set("Authorization", "Bearer "+token)
	passwordRequest.Header.Set("X-ScaleForge-Source", source)
	signScaleForgeAPITestRequest(t, app, passwordRequest)
	passwordResponse := httptest.NewRecorder()
	server.Handler.ServeHTTP(passwordResponse, passwordRequest)
	if passwordResponse.Code != http.StatusTooManyRequests {
		t.Fatalf("password status = %d, want %d; body = %s", passwordResponse.Code, http.StatusTooManyRequests, passwordResponse.Body.String())
	}
}

func TestScaleForgeGatewayRequiresScopedSession(t *testing.T) {
	app := newScaleForgeAPITestHeadscale(t)
	manager, err := app.state.CreateAccount(hsdb.CreateAccountParams{
		Username: "manager",
		Password: "correct horse battery staple",
		Role:     types.AccountRoleManager,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("creating manager: %v", err)
	}
	group, err := app.state.CreateAccountGroup("user-group")
	if err != nil {
		t.Fatalf("creating account group: %v", err)
	}
	user, err := app.state.CreateAccount(hsdb.CreateAccountParams{
		Username: "user",
		Password: "correct horse battery staple",
		GroupID:  &group.ID,
		Role:     types.AccountRoleUser,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("creating user account: %v", err)
	}
	boundUser, err := app.state.GetAccountByID(user.ID)
	if err != nil || boundUser.User == nil {
		t.Fatalf("loading account network identity: account=%+v err=%v", boundUser, err)
	}
	createdUser := *boundUser.User
	otherNetwork := types.User{Name: "other-network"}
	createdOtherUser, _, err := app.state.CreateUser(otherNetwork)
	if err != nil {
		t.Fatalf("creating other user network: %v", err)
	}
	ownedNode := app.state.CreateNodeForTest(&createdUser, "owned-node")
	app.state.PutNodeInStoreForTest(*ownedNode)
	otherNode := app.state.CreateNodeForTest(createdOtherUser, "other-node")
	app.state.PutNodeInStoreForTest(*otherNode)
	now := time.Now().UTC()
	managerToken, _, err := app.state.CreateAccountSession(manager, false, now)
	if err != nil {
		t.Fatalf("creating manager session: %v", err)
	}
	userToken, _, err := app.state.CreateAccountSession(user, false, now)
	if err != nil {
		t.Fatalf("creating user session: %v", err)
	}

	var forwardedUser string
	server := app.newScaleForgeAPIServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		forwardedUser = req.URL.Query().Get("user")
		writer.WriteHeader(http.StatusNoContent)
	}))
	for _, tt := range []struct {
		name       string
		method     string
		path       string
		token      string
		wantStatus int
		wantUser   string
	}{
		{name: "missing bearer", method: http.MethodGet, path: "/api/v1/node", wantStatus: http.StatusUnauthorized},
		{name: "manager retains full gateway access", method: http.MethodPost, path: "/api/v1/users", token: managerToken, wantStatus: http.StatusNoContent},
		{name: "ordinary user list is forced to bound network", method: http.MethodGet, path: "/api/v1/node?user=other-network", token: userToken, wantStatus: http.StatusNoContent, wantUser: createdUser.Name},
		{name: "ordinary user gets own node", method: http.MethodGet, path: "/api/v1/node/" + ownedNode.ID.String(), token: userToken, wantStatus: http.StatusNoContent},
		{name: "ordinary user cannot delete own node", method: http.MethodDelete, path: "/api/v1/node/" + ownedNode.ID.String(), token: userToken, wantStatus: http.StatusForbidden},
		{name: "ordinary user cannot expire own node", method: http.MethodPost, path: "/api/v1/node/" + ownedNode.ID.String() + "/expire", token: userToken, wantStatus: http.StatusForbidden},
		{name: "ordinary user cannot rename own node", method: http.MethodPost, path: "/api/v1/node/" + ownedNode.ID.String() + "/rename/renamed", token: userToken, wantStatus: http.StatusForbidden},
		{name: "ordinary user views own node routes", method: http.MethodGet, path: "/api/v1/node/" + ownedNode.ID.String() + "/routes", token: userToken, wantStatus: http.StatusNoContent},
		{name: "ordinary user cannot read another network node", method: http.MethodGet, path: "/api/v1/node/" + otherNode.ID.String(), token: userToken, wantStatus: http.StatusForbidden},
		{name: "ordinary user cannot delete another network node", method: http.MethodDelete, path: "/api/v1/node/" + otherNode.ID.String(), token: userToken, wantStatus: http.StatusForbidden},
		{name: "ordinary user cannot expire another network node", method: http.MethodPost, path: "/api/v1/node/" + otherNode.ID.String() + "/expire", token: userToken, wantStatus: http.StatusForbidden},
		{name: "ordinary user cannot rename another network node", method: http.MethodPost, path: "/api/v1/node/" + otherNode.ID.String() + "/rename/nope", token: userToken, wantStatus: http.StatusForbidden},
		{name: "ordinary user cannot view another network routes", method: http.MethodGet, path: "/api/v1/node/" + otherNode.ID.String() + "/routes", token: userToken, wantStatus: http.StatusForbidden},
		{name: "ordinary user cannot approve routes", method: http.MethodPost, path: "/api/v1/node/" + ownedNode.ID.String() + "/approve_routes", token: userToken, wantStatus: http.StatusForbidden},
		{name: "ordinary user cannot move node", method: http.MethodPost, path: "/api/v1/node/" + ownedNode.ID.String() + "/user", token: userToken, wantStatus: http.StatusForbidden},
		{name: "ordinary user cannot manage users", method: http.MethodGet, path: "/api/v1/users", token: userToken, wantStatus: http.StatusForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			forwardedUser = ""
			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			signScaleForgeAPITestRequest(t, app, req)
			response := httptest.NewRecorder()
			server.Handler.ServeHTTP(response, req)
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, tt.wantStatus, response.Body.String())
			}
			if forwardedUser != tt.wantUser {
				t.Fatalf("forwarded user = %q, want %q", forwardedUser, tt.wantUser)
			}
		})
	}
}
