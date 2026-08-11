package db

import (
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
