package db

import (
	"database/sql"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/juanfont/headscale/hscontrol/util"
)

func TestMigrateAccountGroupsPreservesNodesAndSplitsLegacyIdentity(t *testing.T) {
	database := newAccountTestDatabase(t)
	if err := database.DB.AutoMigrate(&types.Node{}); err != nil {
		t.Fatalf("migrating node schema: %v", err)
	}

	legacyUser, err := database.CreateUser(types.User{Name: "RD"})
	if err != nil {
		t.Fatalf("creating legacy network identity: %v", err)
	}
	legacyUserID := legacyUser.ID
	account := types.Account{
		Username:          "rd-user",
		PasswordHash:      "legacy-password-hash",
		UserID:            &legacyUserID,
		Role:              types.AccountRoleUser,
		Enabled:           true,
		PasswordChangedAt: time.Now().UTC(),
		PasswordVersion:   1,
	}
	if err := database.DB.Create(&account).Error; err != nil {
		t.Fatalf("creating legacy account: %v", err)
	}

	future := time.Now().UTC().Add(time.Hour)
	past := time.Now().UTC().Add(-time.Hour)
	primaryRoute := netip.MustParsePrefix("10.0.0.0/8")
	legacyRoute := netip.MustParsePrefix("192.168.0.0/16")
	primary := types.Node{
		ID:             1,
		GivenName:      "primary",
		UserID:         &legacyUserID,
		RegisterMethod: util.RegisterMethodPassword,
		Expiry:         &future,
		ApprovedRoutes: []netip.Prefix{primaryRoute},
	}
	legacy := types.Node{
		ID:             7,
		GivenName:      "legacy",
		UserID:         &legacyUserID,
		RegisterMethod: "authkey",
		Expiry:         &past,
		ApprovedRoutes: []netip.Prefix{legacyRoute},
	}
	if err := database.DB.Create(&primary).Error; err != nil {
		t.Fatalf("creating primary node: %v", err)
	}
	if err := database.DB.Create(&legacy).Error; err != nil {
		t.Fatalf("creating legacy node: %v", err)
	}

	if err := migrateAccountGroups(database.DB); err != nil {
		t.Fatalf("migrating account groups: %v", err)
	}

	updated, err := database.GetAccountByID(account.ID)
	if err != nil {
		t.Fatalf("loading migrated account: %v", err)
	}
	if updated.GroupID == nil || *updated.GroupID != legacyUser.ID || updated.Group == nil || updated.Group.Name != "RD" {
		t.Fatalf("legacy group was not preserved: account=%+v group=%+v", updated, updated.Group)
	}

	var primaryAfter, legacyAfter types.Node
	if err := database.DB.First(&primaryAfter, primary.ID).Error; err != nil {
		t.Fatalf("loading primary node: %v", err)
	}
	if err := database.DB.First(&legacyAfter, legacy.ID).Error; err != nil {
		t.Fatalf("loading split node: %v", err)
	}
	if primaryAfter.UserID == nil || *primaryAfter.UserID != legacyUser.ID {
		t.Fatalf("primary node identity changed: %v", primaryAfter.UserID)
	}
	if legacyAfter.UserID == nil || *legacyAfter.UserID == legacyUser.ID {
		t.Fatalf("legacy node was not split: %v", legacyAfter.UserID)
	}
	if len(primaryAfter.ApprovedRoutes) != 1 || primaryAfter.ApprovedRoutes[0] != primaryRoute ||
		len(legacyAfter.ApprovedRoutes) != 1 || legacyAfter.ApprovedRoutes[0] != legacyRoute {
		t.Fatalf("approved routes changed: primary=%v legacy=%v", primaryAfter.ApprovedRoutes, legacyAfter.ApprovedRoutes)
	}

	var placeholder types.Account
	if err := database.DB.Where("user_id = ?", *legacyAfter.UserID).First(&placeholder).Error; err != nil {
		t.Fatalf("loading split placeholder account: %v", err)
	}
	if placeholder.Enabled || placeholder.GroupID == nil || *placeholder.GroupID != legacyUser.ID {
		t.Fatalf("invalid split placeholder account: %+v", placeholder)
	}

	var accountCount int64
	if err := database.DB.Model(&types.Account{}).Count(&accountCount).Error; err != nil {
		t.Fatalf("counting migrated accounts: %v", err)
	}
	if err := migrateAccountGroups(database.DB); err != nil {
		t.Fatalf("repeating account group migration: %v", err)
	}
	var repeatedCount int64
	if err := database.DB.Model(&types.Account{}).Count(&repeatedCount).Error; err != nil {
		t.Fatalf("counting repeated accounts: %v", err)
	}
	if repeatedCount != accountCount {
		t.Fatalf("migration is not idempotent: before=%d after=%d", accountCount, repeatedCount)
	}
}

func TestMigrateAccountGroupsSplitsManagerNodesIntoLegacyGroup(t *testing.T) {
	database := newAccountTestDatabase(t)
	if err := database.DB.AutoMigrate(&types.Node{}); err != nil {
		t.Fatalf("migrating node schema: %v", err)
	}

	managerUser, err := database.CreateUser(types.User{Name: "admin"})
	if err != nil {
		t.Fatalf("creating manager identity: %v", err)
	}
	managerUserID := managerUser.ID
	manager := types.Account{
		Username:          "admin",
		PasswordHash:      "legacy-password-hash",
		UserID:            &managerUserID,
		Role:              types.AccountRoleManager,
		Enabled:           true,
		PasswordChangedAt: time.Now().UTC(),
		PasswordVersion:   1,
	}
	if err := database.DB.Create(&manager).Error; err != nil {
		t.Fatalf("creating manager account: %v", err)
	}

	future := time.Now().UTC().Add(time.Hour)
	nodes := []types.Node{
		{ID: 1, GivenName: "primary", UserID: &managerUserID, RegisterMethod: util.RegisterMethodPassword, Expiry: &future},
		{ID: 2, GivenName: "legacy", UserID: &managerUserID, RegisterMethod: "authkey", Expiry: &future},
	}
	if err := database.DB.Create(&nodes).Error; err != nil {
		t.Fatalf("creating manager nodes: %v", err)
	}

	if err := migrateAccountGroups(database.DB); err != nil {
		t.Fatalf("migrating manager nodes: %v", err)
	}

	var splitNode types.Node
	if err := database.DB.First(&splitNode, 2).Error; err != nil {
		t.Fatalf("loading split manager node: %v", err)
	}
	var placeholder types.Account
	if err := database.DB.Preload("Group").Where("user_id = ?", splitNode.UserID).First(&placeholder).Error; err != nil {
		t.Fatalf("loading manager placeholder: %v", err)
	}
	if placeholder.Enabled || placeholder.Group == nil || placeholder.Group.Name != "Legacy" {
		t.Fatalf("manager placeholder does not have the fallback group: %+v", placeholder)
	}
}

func TestMigrateAccountGroupsMergesCaseInsensitiveLegacyGroups(t *testing.T) {
	database := newAccountTestDatabase(t)
	if err := database.DB.AutoMigrate(&types.Node{}); err != nil {
		t.Fatalf("migrating node schema: %v", err)
	}

	for idx, name := range []string{"RD", "rd"} {
		user, err := database.CreateUser(types.User{Name: name})
		if err != nil {
			t.Fatalf("creating legacy identity %q: %v", name, err)
		}
		userID := user.ID
		account := types.Account{
			Username:          fmt.Sprintf("member-%d", idx+1),
			PasswordHash:      "legacy-password-hash",
			UserID:            &userID,
			Role:              types.AccountRoleUser,
			Enabled:           true,
			PasswordChangedAt: time.Now().UTC(),
			PasswordVersion:   1,
		}
		if err := database.DB.Create(&account).Error; err != nil {
			t.Fatalf("creating legacy account %q: %v", name, err)
		}
	}

	if err := migrateAccountGroups(database.DB); err != nil {
		t.Fatalf("migrating case-insensitive groups: %v", err)
	}

	var groups []types.AccountGroup
	if err := database.DB.Find(&groups).Error; err != nil {
		t.Fatalf("listing migrated groups: %v", err)
	}
	if len(groups) != 1 || groups[0].Name != "RD" {
		t.Fatalf("case variants were not merged: %+v", groups)
	}
	var accounts []types.Account
	if err := database.DB.Order("id ASC").Find(&accounts).Error; err != nil {
		t.Fatalf("listing migrated accounts: %v", err)
	}
	if len(accounts) != 2 || accounts[0].GroupID == nil || accounts[1].GroupID == nil || *accounts[0].GroupID != *accounts[1].GroupID {
		t.Fatalf("accounts were not assigned to the same group: %+v", accounts)
	}
}

func TestMigrateAccountGroupsRollsBackOnSplitFailure(t *testing.T) {
	database := newAccountTestDatabase(t)
	if err := database.DB.AutoMigrate(&types.Node{}); err != nil {
		t.Fatalf("migrating node schema: %v", err)
	}

	legacyUser, err := database.CreateUser(types.User{Name: "RD"})
	if err != nil {
		t.Fatalf("creating legacy identity: %v", err)
	}
	legacyUserID := legacyUser.ID
	account := types.Account{
		Username:          "rd-user",
		PasswordHash:      "legacy-password-hash",
		UserID:            &legacyUserID,
		Role:              types.AccountRoleUser,
		Enabled:           true,
		PasswordChangedAt: time.Now().UTC(),
		PasswordVersion:   1,
	}
	if err := database.DB.Create(&account).Error; err != nil {
		t.Fatalf("creating legacy account: %v", err)
	}
	future := time.Now().UTC().Add(time.Hour)
	nodes := []types.Node{
		{ID: 1, GivenName: "primary", UserID: &legacyUserID, RegisterMethod: util.RegisterMethodPassword, Expiry: &future},
		{ID: 7, GivenName: "legacy", UserID: &legacyUserID, RegisterMethod: "authkey", Expiry: &future},
	}
	if err := database.DB.Create(&nodes).Error; err != nil {
		t.Fatalf("creating legacy nodes: %v", err)
	}
	if _, err := database.CreateUser(types.User{
		Name:               "occupied-legacy-identity",
		Provider:           "scaleforge-account",
		ProviderIdentifier: sql.NullString{String: "legacy-node:7", Valid: true},
	}); err != nil {
		t.Fatalf("creating conflicting identity: %v", err)
	}
	if err := database.DB.Exec(`CREATE UNIQUE INDEX idx_test_provider_identifier
ON users(provider_identifier) WHERE provider_identifier IS NOT NULL`).Error; err != nil {
		t.Fatalf("creating provider identity constraint: %v", err)
	}

	if err := migrateAccountGroups(database.DB); err == nil {
		t.Fatal("migration unexpectedly succeeded despite a conflicting split identity")
	}

	var groupCount int64
	if err := database.DB.Model(&types.AccountGroup{}).Count(&groupCount).Error; err != nil {
		t.Fatalf("counting groups after rollback: %v", err)
	}
	if groupCount != 0 {
		t.Fatalf("migration changes were not rolled back: group count=%d", groupCount)
	}
	var accountAfter types.Account
	if err := database.DB.First(&accountAfter, account.ID).Error; err != nil {
		t.Fatalf("loading account after rollback: %v", err)
	}
	if accountAfter.GroupID != nil {
		t.Fatalf("account group assignment survived rollback: %v", accountAfter.GroupID)
	}
	var nodeAfter types.Node
	if err := database.DB.First(&nodeAfter, 7).Error; err != nil {
		t.Fatalf("loading node after rollback: %v", err)
	}
	if nodeAfter.UserID == nil || *nodeAfter.UserID != legacyUserID {
		t.Fatalf("node identity changed despite rollback: %v", nodeAfter.UserID)
	}
}
