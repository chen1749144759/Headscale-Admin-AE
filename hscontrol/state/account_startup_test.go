package state

import (
	"crypto/sha256"
	"errors"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/juanfont/headscale/hscontrol/db"
	"github.com/juanfont/headscale/hscontrol/types"
)

func newAccountStartupTestConfig(t *testing.T) *types.Config {
	t.Helper()
	prefixV4 := netip.MustParsePrefix("100.64.0.0/10")
	prefixV6 := netip.MustParsePrefix("fd7a:115c:a1e0::/48")

	return &types.Config{
		ServerURL:    "http://localhost:0",
		PrefixV4:     &prefixV4,
		PrefixV6:     &prefixV6,
		IPAllocation: types.IPAllocationStrategySequential,
		Database: types.DatabaseConfig{
			Type:   types.DatabaseSqlite,
			Sqlite: types.SqliteConfig{Path: filepath.Join(t.TempDir(), "headscale.sqlite")},
		},
		Policy: types.PolicyConfig{Mode: types.PolicyModeDB},
	}
}

func TestNewStateAllowsBootstrapRecoveryWithoutDurableManager(t *testing.T) {
	config := newAccountStartupTestConfig(t)
	first, err := NewState(config)
	if err != nil {
		t.Fatalf("creating initial state: %v", err)
	}
	user, err := first.db.CreateUser(types.User{Name: "ordinary-network"})
	if err != nil {
		t.Fatalf("creating user network: %v", err)
	}
	userID := types.UserID(user.ID)
	if _, err := first.db.CreateAccount(db.CreateAccountParams{
		Username: "ordinary-user",
		Password: "correct horse battery staple",
		UserID:   &userID,
		Role:     types.AccountRoleUser,
		Enabled:  true,
	}); err != nil {
		t.Fatalf("creating ordinary account: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("closing initial state: %v", err)
	}

	second, err := NewState(config)
	if err != nil {
		t.Fatalf("reopening recoverable state: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if err := second.ValidateManagerAccountInvariant(); !errors.Is(err, db.ErrLastManager) {
		t.Fatalf("manager invariant error = %v, want %v", err, db.ErrLastManager)
	}
}

func TestNewStateCleansExpiredAndRevokedAccountSessions(t *testing.T) {
	config := newAccountStartupTestConfig(t)
	first, err := NewState(config)
	if err != nil {
		t.Fatalf("creating initial state: %v", err)
	}
	manager, err := first.db.CreateAccount(db.CreateAccountParams{
		Username: "session-manager",
		Password: "correct horse battery staple",
		Role:     types.AccountRoleManager,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("creating manager: %v", err)
	}
	now := time.Now().UTC()
	_, active, err := first.db.CreateAccountSession(manager, false, now)
	if err != nil {
		t.Fatalf("creating active session: %v", err)
	}
	expiredHash := sha256.Sum256([]byte("expired-startup-session"))
	revokedHash := sha256.Sum256([]byte("revoked-startup-session"))
	revokedAt := now.Add(-time.Minute)
	staleSessions := []types.AccountSession{
		{
			TokenHash:       expiredHash[:],
			AccountID:       manager.ID,
			PasswordVersion: manager.PasswordVersion,
			ExpiresAt:       now.Add(-time.Minute),
			LastSeenAt:      now.Add(-time.Hour),
			CreatedAt:       now.Add(-time.Hour),
		},
		{
			TokenHash:       revokedHash[:],
			AccountID:       manager.ID,
			PasswordVersion: manager.PasswordVersion,
			ExpiresAt:       now.Add(time.Hour),
			LastSeenAt:      now.Add(-time.Hour),
			CreatedAt:       now.Add(-time.Hour),
			RevokedAt:       &revokedAt,
		},
	}
	if err := first.db.DB.Create(&staleSessions).Error; err != nil {
		t.Fatalf("creating stale sessions: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("closing initial state: %v", err)
	}

	second, err := NewState(config)
	if err != nil {
		t.Fatalf("reopening state: %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("closing reopened state: %v", err)
		}
	})
	var sessions []types.AccountSession
	if err := second.db.DB.Order("id ASC").Find(&sessions).Error; err != nil {
		t.Fatalf("listing sessions after startup: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != active.ID {
		t.Fatalf("sessions after startup = %+v, want active session %d", sessions, active.ID)
	}
}

func TestBeginAccountAuthenticationRejectsOldPasswordVersion(t *testing.T) {
	config := newAccountStartupTestConfig(t)
	appState, err := NewState(config)
	if err != nil {
		t.Fatalf("creating state: %v", err)
	}
	t.Cleanup(func() { _ = appState.Close() })
	account, err := appState.CreateAccount(db.CreateAccountParams{
		Username: "versioned-manager",
		Password: "correct horse battery staple",
		Role:     types.AccountRoleManager,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("creating account: %v", err)
	}
	authenticated, err := appState.AuthenticateAccount(
		account.Username,
		"correct horse battery staple",
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("authenticating account: %v", err)
	}
	if _, err := appState.ChangeAccountPassword(
		account.ID,
		"another correct horse battery staple",
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("changing password: %v", err)
	}
	if _, unlock, err := appState.BeginAccountAuthentication(
		authenticated.ID,
		authenticated.PasswordVersion,
		time.Now().UTC(),
	); !errors.Is(err, db.ErrAccountConcurrentUpdate) {
		if unlock != nil {
			unlock()
		}
		t.Fatalf("old authentication snapshot error = %v, want %v", err, db.ErrAccountConcurrentUpdate)
	}
}
