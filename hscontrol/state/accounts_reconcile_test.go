package state

import (
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/juanfont/headscale/hscontrol/db"
	"github.com/juanfont/headscale/hscontrol/types"
)

func TestNewStateReconcilesDisabledAccountNodesOnSQLite(t *testing.T) {
	prefixV4 := netip.MustParsePrefix("100.64.0.0/10")
	prefixV6 := netip.MustParsePrefix("fd7a:115c:a1e0::/48")
	config := &types.Config{
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
	first, err := NewState(config)
	if err != nil {
		t.Fatalf("creating initial state: %v", err)
	}
	if _, err := first.db.CreateAccount(db.CreateAccountParams{
		Username: "manager",
		Password: "correct horse battery staple",
		Role:     types.AccountRoleManager,
		Enabled:  true,
	}); err != nil {
		t.Fatalf("creating durable manager: %v", err)
	}
	user, err := first.db.CreateUser(types.User{Name: "account-network"})
	if err != nil {
		t.Fatalf("creating user: %v", err)
	}
	userID := types.UserID(user.ID)
	account, err := first.db.CreateAccount(db.CreateAccountParams{
		Username: "account-user",
		Password: "correct horse battery staple",
		UserID:   &userID,
		Role:     types.AccountRoleUser,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("creating account: %v", err)
	}
	future := time.Now().UTC().Add(time.Hour)
	node := types.Node{UserID: &user.ID, RegisterMethod: "password", Expiry: &future}
	if err := first.db.DB.Create(&node).Error; err != nil {
		t.Fatalf("creating node: %v", err)
	}
	if err := first.db.DB.Model(account).Update("enabled", false).Error; err != nil {
		t.Fatalf("disabling account without updating node: %v", err)
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
			t.Errorf("closing reconciled state: %v", err)
		}
	})
	reconciled, ok := second.GetNodeByID(types.NodeID(node.ID))
	if !ok {
		t.Fatal("reconciled node not found")
	}
	if !reconciled.IsExpired() {
		t.Fatal("disabled account node remained active after SQLite startup reconciliation")
	}
}

func TestNewStateDoesNotExtendManuallyShortenedNodeExpiry(t *testing.T) {
	config := newAccountStartupTestConfig(t)
	first, err := NewState(config)
	if err != nil {
		t.Fatalf("creating initial state: %v", err)
	}
	if _, err := first.db.CreateAccount(db.CreateAccountParams{
		Username: "manager",
		Password: "correct horse battery staple",
		Role:     types.AccountRoleManager,
		Enabled:  true,
	}); err != nil {
		t.Fatalf("creating manager: %v", err)
	}
	user, err := first.db.CreateUser(types.User{Name: "short-lease-network"})
	if err != nil {
		t.Fatalf("creating user: %v", err)
	}
	userID := types.UserID(user.ID)
	if _, err := first.db.CreateAccount(db.CreateAccountParams{
		Username: "short-lease-user",
		Password: "correct horse battery staple",
		UserID:   &userID,
		Role:     types.AccountRoleUser,
		Enabled:  true,
	}); err != nil {
		t.Fatalf("creating account: %v", err)
	}
	shortExpiry := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	node := types.Node{UserID: &user.ID, RegisterMethod: "password", Expiry: &shortExpiry}
	if err := first.db.DB.Create(&node).Error; err != nil {
		t.Fatalf("creating node: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("closing initial state: %v", err)
	}

	second, err := NewState(config)
	if err != nil {
		t.Fatalf("reopening state: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	reconciled, ok := second.GetNodeByID(types.NodeID(node.ID))
	if !ok {
		t.Fatal("reconciled node not found")
	}
	got := reconciled.Expiry()
	if !got.Valid() || !got.Get().Equal(shortExpiry) {
		t.Fatalf("node expiry = %v, want unchanged %v", got, shortExpiry)
	}
}
