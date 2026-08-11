package db

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/juanfont/headscale/hscontrol/types"
	"gorm.io/gorm"
)

func newAccountTestDatabase(t *testing.T) *HSDatabase {
	t.Helper()

	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	if err := gormDB.AutoMigrate(
		&types.User{},
		&types.AccountGroup{},
		&types.Account{},
		&types.AccountSession{},
		&types.AccountPasswordHistory{},
	); err != nil {
		t.Fatalf("migrating database: %v", err)
	}
	sqlDB, err := gormDB.DB()
	if err != nil {
		t.Fatalf("getting database handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	return &HSDatabase{DB: gormDB}
}

func createAccountTestGroup(t *testing.T, database *HSDatabase, name string) *types.AccountGroup {
	t.Helper()
	group, err := database.CreateAccountGroup(name)
	if err != nil {
		t.Fatalf("creating account group: %v", err)
	}
	return group
}

func TestAccountCredentialValidation(t *testing.T) {
	if err := ValidateAccountUsername("a"); err != nil {
		t.Fatalf("single-character username should remain valid: %v", err)
	}
	if err := ValidateAccountUsername("bad\nname"); err == nil {
		t.Fatal("username containing a control character was accepted")
	}
	if err := ValidateAccountPassword("valid-password"); err != nil {
		t.Fatalf("valid password was rejected: %v", err)
	}
	if err := ValidateAccountPassword("invalid\npassword"); err == nil {
		t.Fatal("password containing a control character was accepted")
	}
}

func TestLastManagerCannotBeDisabledDemotedOrExpired(t *testing.T) {
	for _, tt := range []struct {
		name   string
		params func(now time.Time) UpdateAccountParams
		want   error
	}{
		{
			name: "disabled",
			params: func(_ time.Time) UpdateAccountParams {
				enabled := false
				return UpdateAccountParams{Enabled: &enabled}
			},
			want: ErrLastManager,
		},
		{
			name: "demoted",
			params: func(_ time.Time) UpdateAccountParams {
				role := types.AccountRoleUser
				return UpdateAccountParams{Role: &role}
			},
			want: ErrLastManager,
		},
		{
			name: "expired immediately",
			params: func(now time.Time) UpdateAccountParams {
				return UpdateAccountParams{ExpiresAt: &now}
			},
			want: ErrLastManager,
		},
		{
			name: "expires in the future",
			params: func(now time.Time) UpdateAccountParams {
				expiresAt := now.Add(time.Hour)
				return UpdateAccountParams{ExpiresAt: &expiresAt}
			},
			want: ErrLastManager,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			database := newAccountTestDatabase(t)
			manager, err := database.CreateAccount(CreateAccountParams{
				Username: "only-manager",
				Password: "correct horse battery staple",
				Role:     types.AccountRoleManager,
				Enabled:  true,
			})
			if err != nil {
				t.Fatalf("creating manager: %v", err)
			}

			_, err = database.UpdateAccount(manager.ID, tt.params(time.Now().UTC()), time.Now().UTC())
			if !errors.Is(err, tt.want) {
				t.Fatalf("UpdateAccount error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestManagerExpiryAllowedWhenAnotherDurableManagerExists(t *testing.T) {
	database := newAccountTestDatabase(t)
	var expiringManager *types.Account
	for _, username := range []string{"durable-manager", "expiring-manager"} {
		account, err := database.CreateAccount(CreateAccountParams{
			Username: username,
			Password: "correct horse battery staple",
			Role:     types.AccountRoleManager,
			Enabled:  true,
		})
		if err != nil {
			t.Fatalf("creating manager: %v", err)
		}
		if username == "expiring-manager" {
			expiringManager = account
		}
	}

	expiresAt := time.Now().UTC().Add(time.Hour)
	if _, err := database.UpdateAccount(
		expiringManager.ID,
		UpdateAccountParams{ExpiresAt: &expiresAt},
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("setting manager expiry with a durable peer: %v", err)
	}
}

func TestConcurrentLastManagerUpdatesKeepOneActiveManager(t *testing.T) {
	database := newAccountTestDatabase(t)
	now := time.Now().UTC()
	managers := make([]*types.Account, 0, 2)
	for _, name := range []string{"manager-one", "manager-two"} {
		manager, err := database.CreateAccount(CreateAccountParams{
			Username: name,
			Password: "correct horse battery staple",
			Role:     types.AccountRoleManager,
			Enabled:  true,
		})
		if err != nil {
			t.Fatalf("creating manager: %v", err)
		}
		managers = append(managers, manager)
	}

	enabled := false
	start := make(chan struct{})
	errs := make(chan error, len(managers))
	var waitGroup sync.WaitGroup
	for _, manager := range managers {
		waitGroup.Go(func() {
			<-start
			_, err := database.UpdateAccount(manager.ID, UpdateAccountParams{Enabled: &enabled}, now)
			errs <- err
		})
	}
	close(start)
	waitGroup.Wait()
	close(errs)

	successes := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, ErrLastManager) {
			t.Fatalf("concurrent update error = %v, want ErrLastManager", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent updates = %d, want 1", successes)
	}

	var activeManagers int64
	if err := database.DB.Model(&types.Account{}).
		Where("role = ? AND enabled = ?", types.AccountRoleManager, true).
		Where("expires_at IS NULL OR expires_at > ?", now).
		Count(&activeManagers).Error; err != nil {
		t.Fatalf("counting active managers: %v", err)
	}
	if activeManagers != 1 {
		t.Fatalf("active managers = %d, want 1", activeManagers)
	}
}

func TestAuthenticateAccount(t *testing.T) {
	database := newAccountTestDatabase(t)
	group := createAccountTestGroup(t, database, "engineering")
	account, err := database.CreateAccount(CreateAccountParams{
		Username: "Alice",
		Password: "correct horse battery staple",
		GroupID:  &group.ID,
		Role:     types.AccountRoleUser,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("creating account: %v", err)
	}

	now := time.Now().UTC()
	authenticated, err := database.AuthenticateAccount(
		" ALICE ",
		"correct horse battery staple",
		now,
	)
	if err != nil {
		t.Fatalf("authenticating account: %v", err)
	}
	if authenticated.ID != account.ID || authenticated.User == nil || account.UserID == nil || authenticated.User.ID != *account.UserID {
		t.Fatalf("unexpected authenticated account: %+v", authenticated)
	}

	if _, err := database.AuthenticateAccount("alice", "incorrect password", now); !errors.Is(err, ErrAccountInvalidCredentials) {
		t.Fatalf("wrong password error = %v", err)
	}
}

func TestAccountGroupCanContainMultipleIndependentUsers(t *testing.T) {
	database := newAccountTestDatabase(t)
	group := createAccountTestGroup(t, database, "RD")
	alice, err := database.CreateAccount(CreateAccountParams{
		Username: "alice",
		Password: "correct horse battery staple",
		GroupID:  &group.ID,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("creating first account: %v", err)
	}
	bob, err := database.CreateAccount(CreateAccountParams{
		Username: "bob",
		Password: "another correct horse battery staple",
		GroupID:  &group.ID,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("creating second account in same group: %v", err)
	}
	if alice.UserID == nil || bob.UserID == nil || *alice.UserID == *bob.UserID {
		t.Fatalf("accounts do not have independent network identities: alice=%v bob=%v", alice.UserID, bob.UserID)
	}
	if alice.GroupID == nil || bob.GroupID == nil || *alice.GroupID != group.ID || *bob.GroupID != group.ID {
		t.Fatalf("accounts are not assigned to the shared group: alice=%v bob=%v", alice.GroupID, bob.GroupID)
	}
}

func TestUpdateAccountRenamesInternalNetworkIdentity(t *testing.T) {
	database := newAccountTestDatabase(t)
	group := createAccountTestGroup(t, database, "RD")
	account, err := database.CreateAccount(CreateAccountParams{
		Username: "alice",
		Password: "correct horse battery staple",
		GroupID:  &group.ID,
		Role:     types.AccountRoleUser,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("creating account: %v", err)
	}
	username := "alice-renamed"
	updated, err := database.UpdateAccount(
		account.ID,
		UpdateAccountParams{Username: &username},
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("renaming account: %v", err)
	}
	if updated.User == nil || updated.User.Name != "account-alice-renamed" ||
		!updated.User.ProviderIdentifier.Valid || updated.User.ProviderIdentifier.String != "account:alice-renamed" {
		t.Fatalf("internal identity did not follow account rename: %+v", updated.User)
	}
}

func TestUserAccountRequiresGroup(t *testing.T) {
	database := newAccountTestDatabase(t)
	if _, err := database.CreateAccount(CreateAccountParams{
		Username: "unbound-user",
		Password: "correct horse battery staple",
		Role:     types.AccountRoleUser,
		Enabled:  true,
	}); !errors.Is(err, ErrAccountHasNoGroup) {
		t.Fatalf("unbound user account error = %v", err)
	}

	manager, err := database.CreateAccount(CreateAccountParams{
		Username: "manager-user",
		Password: "correct horse battery staple",
		Role:     types.AccountRoleManager,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("creating manager account: %v", err)
	}
	if _, err := database.CreateAccount(CreateAccountParams{
		Username: "backup-manager",
		Password: "correct horse battery staple",
		Role:     types.AccountRoleManager,
		Enabled:  true,
	}); err != nil {
		t.Fatalf("creating backup manager account: %v", err)
	}
	role := types.AccountRoleUser
	if _, err := database.UpdateAccount(manager.ID, UpdateAccountParams{Role: &role}, time.Now().UTC()); !errors.Is(err, ErrAccountHasNoGroup) {
		t.Fatalf("unbound manager-to-user update error = %v", err)
	}
}

func TestAuthenticateAccountPasswordExpiry(t *testing.T) {
	database := newAccountTestDatabase(t)
	account, err := database.CreateAccount(CreateAccountParams{
		Username: "expired-user",
		Password: "correct horse battery staple",
		Role:     types.AccountRoleManager,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("creating account: %v", err)
	}

	changedAt := time.Now().UTC().Add(-types.AccountPasswordMaxAge - time.Hour)
	if err := database.DB.Model(account).Update("password_changed_at", changedAt).Error; err != nil {
		t.Fatalf("aging password: %v", err)
	}

	authenticated, err := database.AuthenticateAccount(
		account.Username,
		"correct horse battery staple",
		time.Now().UTC(),
	)
	if !errors.Is(err, ErrAccountPasswordExpired) {
		t.Fatalf("password expiry error = %v", err)
	}
	if authenticated == nil || authenticated.ID != account.ID {
		t.Fatalf("expired credentials must still identify the account")
	}
}

func TestAccountSessionRevokedByPasswordChange(t *testing.T) {
	database := newAccountTestDatabase(t)
	account, err := database.CreateAccount(CreateAccountParams{
		Username: "session-user",
		Password: "correct horse battery staple",
		Role:     types.AccountRoleManager,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("creating account: %v", err)
	}

	now := time.Now().UTC()
	token, _, err := database.CreateAccountSession(account, false, now)
	if err != nil {
		t.Fatalf("creating session: %v", err)
	}
	if _, err := database.ValidateAccountSession(token, now); err != nil {
		t.Fatalf("validating session: %v", err)
	}

	if err := database.ChangeAccountPassword(account.ID, "another correct password", now.Add(time.Minute)); err != nil {
		t.Fatalf("changing password: %v", err)
	}
	if _, err := database.ValidateAccountSession(token, now.Add(2*time.Minute)); !errors.Is(err, ErrAccountSessionInvalid) {
		t.Fatalf("revoked session error = %v", err)
	}
}

func TestCleanupAccountSessionsDeletesExpiredAndRevokedRecords(t *testing.T) {
	database := newAccountTestDatabase(t)
	account, err := database.CreateAccount(CreateAccountParams{
		Username: "session-cleanup-manager",
		Password: "correct horse battery staple",
		Role:     types.AccountRoleManager,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("creating account: %v", err)
	}

	now := time.Now().UTC()
	_, active, err := database.CreateAccountSession(account, false, now)
	if err != nil {
		t.Fatalf("creating active session: %v", err)
	}
	expiredHash := accountSessionTokenHash("expired-session-token")
	revokedHash := accountSessionTokenHash("revoked-session-token")
	revokedAt := now.Add(-time.Minute)
	staleSessions := []types.AccountSession{
		{
			TokenHash:       expiredHash[:],
			AccountID:       account.ID,
			PasswordVersion: account.PasswordVersion,
			ExpiresAt:       now.Add(-time.Minute),
			LastSeenAt:      now.Add(-time.Hour),
			CreatedAt:       now.Add(-time.Hour),
		},
		{
			TokenHash:       revokedHash[:],
			AccountID:       account.ID,
			PasswordVersion: account.PasswordVersion,
			ExpiresAt:       now.Add(time.Hour),
			LastSeenAt:      now.Add(-time.Hour),
			CreatedAt:       now.Add(-time.Hour),
			RevokedAt:       &revokedAt,
		},
	}
	if err := database.DB.Create(&staleSessions).Error; err != nil {
		t.Fatalf("creating stale sessions: %v", err)
	}

	deleted, err := database.CleanupAccountSessions(now)
	if err != nil {
		t.Fatalf("cleaning sessions: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted sessions = %d, want 2", deleted)
	}
	var remaining []types.AccountSession
	if err := database.DB.Order("id ASC").Find(&remaining).Error; err != nil {
		t.Fatalf("listing remaining sessions: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != active.ID {
		t.Fatalf("remaining sessions = %+v, want active session %d", remaining, active.ID)
	}
}

func TestAccountPasswordCannotBeReused(t *testing.T) {
	database := newAccountTestDatabase(t)
	account, err := database.CreateAccount(CreateAccountParams{
		Username: "password-user",
		Password: "correct horse battery staple",
		Role:     types.AccountRoleManager,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("creating account: %v", err)
	}
	if err := database.ChangeAccountPassword(account.ID, "correct horse battery staple", time.Now().UTC()); !errors.Is(err, ErrAccountPasswordReused) {
		t.Fatalf("password reuse error = %v", err)
	}
}

func TestAccountPasswordHistoryRejectsRotationBack(t *testing.T) {
	database := newAccountTestDatabase(t)
	account, err := database.CreateAccount(CreateAccountParams{
		Username: "password-history-user",
		Password: "first correct password",
		Role:     types.AccountRoleManager,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("creating account: %v", err)
	}
	now := time.Now().UTC()
	if err := database.ChangeAccountPassword(account.ID, "second correct password", now); err != nil {
		t.Fatalf("changing password: %v", err)
	}
	if err := database.ChangeAccountPassword(account.ID, "first correct password", now.Add(time.Minute)); !errors.Is(err, ErrAccountPasswordReused) {
		t.Fatalf("historical password reuse error = %v", err)
	}
}

func TestAdminPasswordResetRequiresUserChange(t *testing.T) {
	database := newAccountTestDatabase(t)
	account, err := database.CreateAccount(CreateAccountParams{
		Username: "reset-password-user",
		Password: "first correct password",
		Role:     types.AccountRoleManager,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("creating account: %v", err)
	}
	now := time.Now().UTC()
	if err := database.ResetAccountPassword(
		account.ID,
		"temporary correct password",
		now,
		account.ID,
	); err != nil {
		t.Fatalf("resetting password: %v", err)
	}
	updated, err := database.GetAccountByID(account.ID)
	if err != nil {
		t.Fatalf("loading reset account: %v", err)
	}
	if !updated.MustChangePassword {
		t.Fatal("administrator reset did not create a temporary password")
	}
	authenticated, err := database.AuthenticateAccount(
		updated.Username,
		"temporary correct password",
		now.Add(time.Second),
	)
	if !errors.Is(err, ErrAccountPasswordExpired) || authenticated == nil {
		t.Fatalf("temporary password authentication = account %+v, err %v", authenticated, err)
	}
}

func TestCreateAccountCanRequireInitialPasswordChange(t *testing.T) {
	database := newAccountTestDatabase(t)
	account, err := database.CreateAccount(CreateAccountParams{
		Username:              "temporary-password-user",
		Password:              "temporary correct password",
		Role:                  types.AccountRoleManager,
		Enabled:               true,
		RequirePasswordChange: true,
	})
	if err != nil {
		t.Fatalf("creating account: %v", err)
	}
	if !account.MustChangePassword {
		t.Fatal("initial administrator-supplied password was not marked temporary")
	}
}

func TestCreateAccountPersistsExpiry(t *testing.T) {
	database := newAccountTestDatabase(t)
	group := createAccountTestGroup(t, database, "expiring-group")
	expiresAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	account, err := database.CreateAccount(CreateAccountParams{
		Username:  "expiring-user",
		Password:  "correct horse battery staple",
		GroupID:   &group.ID,
		Role:      types.AccountRoleUser,
		Enabled:   true,
		ExpiresAt: &expiresAt,
	})
	if err != nil {
		t.Fatalf("creating expiring account: %v", err)
	}
	if account.ExpiresAt == nil || !account.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("account expiry = %v, want %v", account.ExpiresAt, expiresAt)
	}
}

func TestBootstrapManagerRecoversDisabledAccount(t *testing.T) {
	database := newAccountTestDatabase(t)
	manager, err := database.CreateAccount(CreateAccountParams{
		Username: "recovery-manager",
		Password: "old correct horse battery staple",
		Role:     types.AccountRoleManager,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("creating manager: %v", err)
	}
	now := time.Now().UTC()
	token, _, err := database.CreateAccountSession(manager, false, now)
	if err != nil {
		t.Fatalf("creating old session: %v", err)
	}
	if err := database.DB.Model(manager).Update("enabled", false).Error; err != nil {
		t.Fatalf("disabling manager: %v", err)
	}

	recovered, err := database.BootstrapManagerAccount(
		"recovery-manager",
		"new correct horse battery staple",
	)
	if err != nil {
		t.Fatalf("recovering manager: %v", err)
	}
	if !recovered.Enabled || recovered.Role != types.AccountRoleManager || recovered.ExpiresAt != nil {
		t.Fatalf("unexpected recovered manager: %+v", recovered)
	}
	if recovered.PasswordVersion <= manager.PasswordVersion {
		t.Fatalf("password version = %d, want greater than %d", recovered.PasswordVersion, manager.PasswordVersion)
	}
	if _, err := database.AuthenticateAccount(
		"recovery-manager",
		"new correct horse battery staple",
		now.Add(time.Minute),
	); err != nil {
		t.Fatalf("authenticating recovered manager: %v", err)
	}
	if _, err := database.ValidateAccountSession(token, now.Add(time.Minute)); !errors.Is(err, ErrAccountSessionInvalid) {
		t.Fatalf("old session error = %v, want %v", err, ErrAccountSessionInvalid)
	}
	if _, err := database.BootstrapManagerAccount(
		"another-manager",
		"another correct horse battery staple",
	); !errors.Is(err, ErrAccountBootstrapComplete) {
		t.Fatalf("second bootstrap error = %v, want %v", err, ErrAccountBootstrapComplete)
	}
}

func TestMigrateScaleTailAccounts(t *testing.T) {
	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	if err := gormDB.AutoMigrate(&types.User{}); err != nil {
		t.Fatalf("migrating users: %v", err)
	}
	for _, statement := range []string{
		"ALTER TABLE users ADD COLUMN password text",
		"ALTER TABLE users ADD COLUMN expire datetime",
		"ALTER TABLE users ADD COLUMN cellphone text",
		"ALTER TABLE users ADD COLUMN role text",
		"ALTER TABLE users ADD COLUMN enable text",
		"ALTER TABLE users ADD COLUMN route text",
		"ALTER TABLE users ADD COLUMN node text",
	} {
		if err := gormDB.Exec(statement).Error; err != nil {
			t.Fatalf("preparing legacy schema: %v", err)
		}
	}

	expires := time.Now().UTC().Add(-time.Hour)
	user := types.User{Name: "legacy", Email: "legacy@example.com"}
	if err := gormDB.Create(&user).Error; err != nil {
		t.Fatalf("creating legacy user: %v", err)
	}
	if err := gormDB.Exec(
		"UPDATE users SET password = ?, role = ?, enable = ?, expire = ? WHERE id = ?",
		"plain:legacy-password", "administrator", "true", expires, user.ID,
	).Error; err != nil {
		t.Fatalf("writing legacy identity: %v", err)
	}
	fallbackUser := types.User{Name: " ", Email: "fallback@example.com"}
	if err := gormDB.Create(&fallbackUser).Error; err != nil {
		t.Fatalf("creating email fallback user: %v", err)
	}
	if err := gormDB.Exec(
		"UPDATE users SET password = ?, role = ?, enable = ? WHERE id = ?",
		"plain:fallback-password", "user", "true", fallbackUser.ID,
	).Error; err != nil {
		t.Fatalf("writing email fallback identity: %v", err)
	}
	nullableUser := types.User{Name: "nullable-source"}
	if err := gormDB.Create(&nullableUser).Error; err != nil {
		t.Fatalf("creating nullable legacy user: %v", err)
	}
	if err := gormDB.Exec(
		"UPDATE users SET name = NULL, email = NULL, password = ?, role = NULL, enable = ? WHERE id = ?",
		"plain:nullable-password", "true", nullableUser.ID,
	).Error; err != nil {
		t.Fatalf("writing nullable legacy identity: %v", err)
	}

	if err := migrateScaleTailAccounts(gormDB); err != nil {
		t.Fatalf("migrating accounts: %v", err)
	}

	var account types.Account
	if err := gormDB.Where("username = ?", "legacy").First(&account).Error; err != nil {
		t.Fatalf("finding migrated account: %v", err)
	}
	if account.UserID == nil || *account.UserID != user.ID || !account.MustChangePassword || account.Role != types.AccountRoleManager || account.ExpiresAt != nil {
		t.Fatalf("unexpected migrated account: %+v", account)
	}
	var fallbackAccount types.Account
	if err := gormDB.Where("username = ?", "fallback@example.com").First(&fallbackAccount).Error; err != nil {
		t.Fatalf("finding email fallback account: %v", err)
	}
	if fallbackAccount.UserID == nil || *fallbackAccount.UserID != fallbackUser.ID {
		t.Fatalf("unexpected email fallback account: %+v", fallbackAccount)
	}
	var nullableAccount types.Account
	if err := gormDB.Where("username = ?", fmt.Sprintf("account-%d", nullableUser.ID)).First(&nullableAccount).Error; err != nil {
		t.Fatalf("finding nullable-source account: %v", err)
	}
	if nullableAccount.UserID == nil || *nullableAccount.UserID != nullableUser.ID {
		t.Fatalf("unexpected nullable-source account: %+v", nullableAccount)
	}
	for _, column := range legacyUserAccountColumns {
		if !gormDB.Migrator().HasColumn("users", column) {
			t.Fatalf("legacy source column users.%s was removed during the first migration stage", column)
		}
	}
}

func TestMigrateScaleTailAccountsHandlesPartialLegacySchemaAndRepeats(t *testing.T) {
	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	if err := gormDB.AutoMigrate(&types.User{}); err != nil {
		t.Fatalf("migrating users: %v", err)
	}
	if err := gormDB.Exec("ALTER TABLE users ADD COLUMN password text").Error; err != nil {
		t.Fatalf("adding only legacy password column: %v", err)
	}
	user := types.User{Name: "partial-legacy-user"}
	if err := gormDB.Create(&user).Error; err != nil {
		t.Fatalf("creating legacy user: %v", err)
	}
	if err := gormDB.Exec(
		"UPDATE users SET password = ? WHERE id = ?",
		"plain:partial legacy password",
		user.ID,
	).Error; err != nil {
		t.Fatalf("writing legacy password: %v", err)
	}
	for attempt := range 2 {
		if err := migrateScaleTailAccounts(gormDB); err != nil {
			t.Fatalf("migration attempt %d: %v", attempt+1, err)
		}
	}
	var accounts int64
	if err := gormDB.Model(&types.Account{}).Where("user_id = ?", user.ID).Count(&accounts).Error; err != nil {
		t.Fatalf("counting migrated accounts: %v", err)
	}
	if accounts != 1 {
		t.Fatalf("migrated accounts = %d, want 1", accounts)
	}
	if !gormDB.Migrator().HasColumn("users", "password") {
		t.Fatal("partial migration removed its source password column")
	}
}

func TestMigrateScaleTailAccountsCompletesWithoutActiveManager(t *testing.T) {
	for _, tt := range []struct {
		name   string
		role   string
		enable string
	}{
		{name: "ordinary user", role: "user", enable: "true"},
		{name: "disabled administrator", role: "administrator", enable: "false"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			if err != nil {
				t.Fatalf("opening database: %v", err)
			}
			if err := gormDB.AutoMigrate(&types.User{}); err != nil {
				t.Fatalf("migrating users: %v", err)
			}
			for _, statement := range []string{
				"ALTER TABLE users ADD COLUMN password text",
				"ALTER TABLE users ADD COLUMN expire datetime",
				"ALTER TABLE users ADD COLUMN cellphone text",
				"ALTER TABLE users ADD COLUMN role text",
				"ALTER TABLE users ADD COLUMN enable text",
				"ALTER TABLE users ADD COLUMN route text",
				"ALTER TABLE users ADD COLUMN node text",
			} {
				if err := gormDB.Exec(statement).Error; err != nil {
					t.Fatalf("preparing legacy schema: %v", err)
				}
			}

			user := types.User{Name: "legacy-user"}
			if err := gormDB.Create(&user).Error; err != nil {
				t.Fatalf("creating user: %v", err)
			}
			if err := gormDB.Exec(
				"UPDATE users SET password = ?, role = ?, enable = ? WHERE id = ?",
				"plain:legacy-password", tt.role, tt.enable, user.ID,
			).Error; err != nil {
				t.Fatalf("writing legacy identity: %v", err)
			}
			if err := migrateScaleTailAccounts(gormDB); err != nil {
				t.Fatalf("migrating recoverable account data: %v", err)
			}
			if !gormDB.Migrator().HasColumn("users", "password") {
				t.Fatal("legacy password source column was removed before migration verification")
			}
			database := &HSDatabase{DB: gormDB}
			if err := database.ValidateManagerAccountInvariant(); !errors.Is(err, ErrLastManager) {
				t.Fatalf("manager invariant error = %v, want %v", err, ErrLastManager)
			}
		})
	}
}

func TestMigrateLegacyAccountPasswordRejectsUnknownHashes(t *testing.T) {
	tests := []string{
		"$2a$12$malformed",
		"$argon2id$v=19$m=65536,t=3,p=4$invalid",
		"{SHA}invalid",
		"pbkdf2:sha256:600000$invalid",
		"scrypt:32768:8:1$invalid",
		"5f4dcc3b5aa765d61d8327deb882cf99",
		"unmarked-plaintext-password",
	}
	if _, ok, err := migrateLegacyAccountPassword("plain:explicit-legacy-password"); err != nil || !ok {
		t.Fatalf("explicitly marked legacy plaintext was not migrated: ok %v, err %v", ok, err)
	}
	for _, stored := range tests {
		if _, ok, err := migrateLegacyAccountPassword(stored); err != nil || ok {
			t.Fatalf("migrateLegacyAccountPassword(%q) = ok %v, err %v; want unsupported", stored, ok, err)
		}
	}
}

func TestFreshDatabaseSchemaIncludesAccounts(t *testing.T) {
	config := &types.Config{
		Database: types.DatabaseConfig{
			Type: types.DatabaseSqlite,
			Sqlite: types.SqliteConfig{
				Path: filepath.Join(t.TempDir(), "headscale.sqlite"),
			},
		},
	}

	database, err := NewHeadscaleDatabase(config)
	if err != nil {
		t.Fatalf("creating fresh database: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("closing database: %v", err)
		}
	})

	if !database.DB.Migrator().HasTable(&types.Account{}) {
		t.Fatal("fresh database does not contain accounts table")
	}
	if !database.DB.Migrator().HasTable(&types.AccountPasswordHistory{}) {
		t.Fatal("fresh database does not contain account password history")
	}
}

func TestExpireLegacyAuthNodes(t *testing.T) {
	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	if err := gormDB.Exec(`CREATE TABLE nodes (
		id integer PRIMARY KEY,
		register_method text,
		expiry datetime
	)`).Error; err != nil {
		t.Fatalf("creating nodes table: %v", err)
	}
	if err := gormDB.Exec(`INSERT INTO nodes (id, register_method, expiry) VALUES
		(1, 'authkey', NULL),
		(2, 'password', NULL)`).Error; err != nil {
		t.Fatalf("creating nodes: %v", err)
	}

	if err := expireLegacyAuthNodes(gormDB); err != nil {
		t.Fatalf("expiring legacy nodes: %v", err)
	}

	var legacyExpiry, passwordExpiry sql.NullTime
	if err := gormDB.Raw("SELECT expiry FROM nodes WHERE id = 1").Scan(&legacyExpiry).Error; err != nil {
		t.Fatalf("reading legacy node: %v", err)
	}
	if err := gormDB.Raw("SELECT expiry FROM nodes WHERE id = 2").Scan(&passwordExpiry).Error; err != nil {
		t.Fatalf("reading password node: %v", err)
	}
	if !legacyExpiry.Valid {
		t.Fatal("legacy node was not expired")
	}
	if passwordExpiry.Valid {
		t.Fatal("password-authenticated node was expired")
	}
}
