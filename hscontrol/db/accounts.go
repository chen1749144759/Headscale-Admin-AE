package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/juanfont/headscale/hscontrol/types"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const (
	accountUsernameMaxBytes = 254
	accountPasswordMinBytes = 12
	accountPasswordMaxBytes = 72
	accountPasswordHistory  = 4
	bcryptCost              = 12
)

var (
	ErrAccountInvalidCredentials = errors.New("invalid username or password")
	ErrAccountDisabled           = errors.New("account is disabled")
	ErrAccountExpired            = errors.New("account is expired")
	ErrAccountPasswordExpired    = errors.New("password has expired")
	ErrAccountUsernameExists     = errors.New("account username already exists")
	ErrAccountHasNoGroup         = errors.New("account is not assigned to a group")
	ErrAccountGroupNotFound      = errors.New("account group not found")
	ErrAccountPasswordReused     = errors.New("new password must be different")
	ErrAccountConcurrentUpdate   = errors.New("account changed concurrently")
	ErrAccountBootstrapComplete  = errors.New("account bootstrap has already completed")
	ErrLastManager               = errors.New("at least one enabled manager without account expiry is required")

	// Comparing against a fixed hash keeps unknown-account requests close to
	// the cost of a real bcrypt verification without creating per-request work.
	dummyAccountPasswordHash = []byte("$2a$12$EixZaYVK1fsbw1ZfbX3OXePaWxn96p36HQpR1hG4vGxBka.PaWZtW")
)

type CreateAccountParams struct {
	Username              string
	Password              string
	GroupID               *uint
	Role                  string
	Enabled               bool
	ExpiresAt             *time.Time
	RequirePasswordChange bool
	ActorAccountID        *uint
}

type UpdateAccountParams struct {
	Username       *string
	Role           *string
	Enabled        *bool
	ExpiresAt      *time.Time
	ClearExpiresAt bool
	GroupID        *uint
	ClearGroup     bool
	ActorAccountID *uint
}

func (hsdb *HSDatabase) CountAccounts() (int64, error) {
	var count int64
	if err := hsdb.DB.Model(&types.Account{}).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("counting accounts: %w", err)
	}

	return count, nil
}

// ValidateManagerAccountInvariant prevents an existing account database from
// starting without a durable manager capable of recovering account access.
func (hsdb *HSDatabase) ValidateManagerAccountInvariant() error {
	total, err := hsdb.CountAccounts()
	if err != nil {
		return err
	}
	if total == 0 {
		return nil
	}

	var durableManagers int64
	if err := hsdb.DB.Model(&types.Account{}).
		Where("role = ? AND enabled = ?", types.AccountRoleManager, true).
		Where("expires_at IS NULL").
		Count(&durableManagers).Error; err != nil {
		return fmt.Errorf("counting durable manager accounts: %w", err)
	}
	if durableManagers == 0 {
		return ErrLastManager
	}

	return nil
}

func (hsdb *HSDatabase) ListAccounts() ([]types.Account, error) {
	var accounts []types.Account
	if err := hsdb.DB.Preload("User").Preload("Group").Order("id ASC").Find(&accounts).Error; err != nil {
		return nil, fmt.Errorf("listing accounts: %w", err)
	}

	return accounts, nil
}

func (hsdb *HSDatabase) GetAccountByID(accountID uint) (*types.Account, error) {
	var account types.Account
	result := hsdb.DB.Preload("User").Preload("Group").First(&account, accountID)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, gorm.ErrRecordNotFound
	}
	if result.Error != nil {
		return nil, fmt.Errorf("finding account: %w", result.Error)
	}

	return &account, nil
}

func (hsdb *HSDatabase) UpdateAccount(
	accountID uint,
	params UpdateAccountParams,
	now time.Time,
) (*types.Account, error) {
	updates := map[string]any{}
	if params.Username != nil {
		username := types.NormalizeAccountUsername(*params.Username)
		if err := ValidateAccountUsername(username); err != nil {
			return nil, err
		}
		updates["username"] = username
	}
	if params.Role != nil {
		role := strings.ToLower(strings.TrimSpace(*params.Role))
		if role != types.AccountRoleUser && role != types.AccountRoleManager {
			return nil, errors.New("invalid account role")
		}
		updates["role"] = role
	}
	if params.Enabled != nil {
		updates["enabled"] = *params.Enabled
	}
	if params.ClearExpiresAt {
		updates["expires_at"] = nil
	} else if params.ExpiresAt != nil {
		updates["expires_at"] = *params.ExpiresAt
	}
	if params.ClearGroup {
		updates["group_id"] = nil
	} else if params.GroupID != nil {
		updates["group_id"] = *params.GroupID
	}
	if len(updates) == 0 {
		return hsdb.GetAccountByID(accountID)
	}

	err := hsdb.Write(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(675833145991)").Error; err != nil {
				return fmt.Errorf("locking manager account updates: %w", err)
			}
		}
		var current types.Account
		if err := tx.First(&current, accountID).Error; err != nil {
			return err
		}

		nextRole := current.Role
		if role, ok := updates["role"].(string); ok {
			nextRole = role
		}
		nextEnabled := current.Enabled
		if enabled, ok := updates["enabled"].(bool); ok {
			nextEnabled = enabled
		}
		nextHasExpiry := current.ExpiresAt != nil
		if params.ClearExpiresAt {
			nextHasExpiry = false
		} else if params.ExpiresAt != nil {
			nextHasExpiry = true
		}
		nextGroupID := current.GroupID
		if params.ClearGroup {
			nextGroupID = nil
		} else if params.GroupID != nil {
			value := *params.GroupID
			nextGroupID = &value
		}
		if current.Role == types.AccountRoleManager && current.Enabled &&
			(nextRole != types.AccountRoleManager || !nextEnabled || nextHasExpiry) {
			var otherManagers int64
			if err := tx.Model(&types.Account{}).
				Where("id <> ? AND role = ? AND enabled = ?", accountID, types.AccountRoleManager, true).
				Where("expires_at IS NULL").
				Count(&otherManagers).Error; err != nil {
				return fmt.Errorf("counting manager accounts: %w", err)
			}
			if otherManagers == 0 {
				return ErrLastManager
			}
		}
		if nextRole == types.AccountRoleUser && nextGroupID == nil {
			return ErrAccountHasNoGroup
		}
		if nextGroupID != nil {
			var count int64
			if err := tx.Model(&types.AccountGroup{}).Where("id = ?", *nextGroupID).Count(&count).Error; err != nil {
				return fmt.Errorf("checking account group: %w", err)
			}
			if count == 0 {
				return ErrAccountGroupNotFound
			}
		}

		result := tx.Model(&types.Account{}).Where("id = ?", accountID).Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("updating account: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if username, ok := updates["username"].(string); ok && current.UserID != nil {
			providerIdentifier := sql.NullString{String: "account:" + username, Valid: true}
			result := tx.Model(&types.User{}).Where("id = ?", *current.UserID).Updates(map[string]any{
				"name":                "account-" + username,
				"provider_identifier": providerIdentifier,
			})
			if result.Error != nil {
				return fmt.Errorf("renaming account network identity: %w", result.Error)
			}
			if result.RowsAffected == 0 {
				return fmt.Errorf("renaming account network identity: %w", gorm.ErrRecordNotFound)
			}
		}

		if err := tx.Model(&types.AccountSession{}).
			Where("account_id = ? AND revoked_at IS NULL", accountID).
			Update("revoked_at", now).Error; err != nil {
			return fmt.Errorf("revoking account sessions: %w", err)
		}

		var updated types.Account
		if err := tx.First(&updated, accountID).Error; err != nil {
			return fmt.Errorf("reading updated account: %w", err)
		}
		if err := clampAccountNodes(tx, updated.UserID, accountNodeExpiry(&updated, now)); err != nil {
			return err
		}
		if err := writeAccountAudit(
			tx,
			params.ActorAccountID,
			"account.update",
			fmt.Sprintf("account:%d", accountID),
			fmt.Sprintf("updated platform account %s", updated.Username),
		); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return hsdb.GetAccountByID(accountID)
}

func accountNodeExpiry(account *types.Account, now time.Time) time.Time {
	if account == nil || account.UserID == nil || !account.Enabled || account.PasswordExpired(now) {
		return now
	}

	expiry := account.PasswordChangedAt.Add(types.AccountPasswordMaxAge)
	if account.ExpiresAt != nil && account.ExpiresAt.Before(expiry) {
		expiry = *account.ExpiresAt
	}
	if !expiry.After(now) {
		return now
	}

	return expiry
}

// clampAccountNodes persists the restrictive side of an account change in the
// same transaction. It intentionally never extends an existing node lease.
func clampAccountNodes(tx *gorm.DB, userID *uint, expiry time.Time) error {
	if userID == nil || !tx.Migrator().HasTable(&types.Node{}) {
		return nil
	}
	result := tx.Model(&types.Node{}).
		Where("user_id = ?", *userID).
		Where("expiry IS NULL OR expiry > ?", expiry).
		Update("expiry", expiry)
	if result.Error != nil {
		return fmt.Errorf("clamping account node expiry: %w", result.Error)
	}
	return nil
}

// ReconcileAccountNodeExpiries fails closed after an interrupted account
// update. It never extends a node lease; successful password authentication is
// the only path that may grant a new lease.
func (hsdb *HSDatabase) ReconcileAccountNodeExpiries(now time.Time) error {
	return hsdb.Write(func(tx *gorm.DB) error {
		result := tx.Exec(`
UPDATE nodes AS n
SET expiry = CASE
	WHEN a.enabled = FALSE
		OR a.must_change_password = TRUE
		OR a.password_changed_at IS NULL
		OR a.password_changed_at <= ?
		OR (a.expires_at IS NOT NULL AND a.expires_at <= ?)
		THEN ?
	WHEN a.expires_at IS NOT NULL
		THEN LEAST(COALESCE(n.expiry, a.expires_at), a.expires_at, a.password_changed_at + INTERVAL '90 days')
	ELSE LEAST(COALESCE(n.expiry, a.password_changed_at + INTERVAL '90 days'), a.password_changed_at + INTERVAL '90 days')
END
FROM accounts AS a
WHERE n.user_id = a.user_id
	AND n.deleted_at IS NULL
	AND (n.expiry IS NULL OR n.expiry > CASE
		WHEN a.enabled = FALSE
			OR a.must_change_password = TRUE
			OR a.password_changed_at IS NULL
			OR a.password_changed_at <= ?
			OR (a.expires_at IS NOT NULL AND a.expires_at <= ?)
			THEN ?
		WHEN a.expires_at IS NOT NULL
			THEN LEAST(a.expires_at, a.password_changed_at + INTERVAL '90 days')
		ELSE a.password_changed_at + INTERVAL '90 days'
	END)
`,
			now.Add(-types.AccountPasswordMaxAge), now, now,
			now.Add(-types.AccountPasswordMaxAge), now, now,
		)
		if result.Error != nil {
			return fmt.Errorf("reconciling account node expiries: %w", result.Error)
		}
		return nil
	})
}

func (hsdb *HSDatabase) BootstrapManagerAccount(
	username,
	password string,
) (*types.Account, error) {
	normalizedUsername := types.NormalizeAccountUsername(username)
	if err := ValidateAccountUsername(normalizedUsername); err != nil {
		return nil, err
	}
	passwordHash, err := HashAccountPassword(password)
	if err != nil {
		return nil, err
	}

	var accountID uint
	err = hsdb.Write(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(675833145991)").Error; err != nil {
				return fmt.Errorf("locking manager account bootstrap: %w", err)
			}
		}

		var durableManagers int64
		if err := tx.Model(&types.Account{}).
			Where("role = ? AND enabled = ?", types.AccountRoleManager, true).
			Where("expires_at IS NULL").
			Count(&durableManagers).Error; err != nil {
			return fmt.Errorf("counting durable manager accounts: %w", err)
		}
		if durableManagers > 0 {
			return ErrAccountBootstrapComplete
		}

		var existing types.Account
		result := tx.Where("username = ?", normalizedUsername).First(&existing)
		if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return fmt.Errorf("finding bootstrap account: %w", result.Error)
		}

		var userID uint
		if result.Error == nil && existing.UserID != nil {
			userID = *existing.UserID
		} else {
			var user types.User
			userResult := tx.Where("name = ? AND provider_identifier IS NULL", normalizedUsername).First(&user)
			if errors.Is(userResult.Error, gorm.ErrRecordNotFound) {
				user = types.User{Name: normalizedUsername, Provider: "password"}
				if err := tx.Create(&user).Error; err != nil {
					return fmt.Errorf("creating bootstrap network: %w", err)
				}
			} else if userResult.Error != nil {
				return fmt.Errorf("finding bootstrap network: %w", userResult.Error)
			}
			userID = user.ID
		}

		now := time.Now().UTC()
		if result.Error == nil {
			updates := map[string]any{
				"password_hash":        passwordHash,
				"user_id":              userID,
				"role":                 types.AccountRoleManager,
				"enabled":              true,
				"expires_at":           nil,
				"password_changed_at":  now,
				"must_change_password": false,
				"password_version":     gorm.Expr("password_version + 1"),
			}
			if err := tx.Model(&existing).Updates(updates).Error; err != nil {
				return fmt.Errorf("recovering bootstrap manager: %w", err)
			}
			accountID = existing.ID
		} else {
			created := &types.Account{
				Username:          normalizedUsername,
				PasswordHash:      passwordHash,
				UserID:            &userID,
				Role:              types.AccountRoleManager,
				Enabled:           true,
				PasswordChangedAt: now,
				PasswordVersion:   1,
			}
			if err := tx.Create(created).Error; err != nil {
				return fmt.Errorf("creating bootstrap manager: %w", err)
			}
			accountID = created.ID
		}

		if err := tx.Model(&types.AccountSession{}).
			Where("account_id = ? AND revoked_at IS NULL", accountID).
			Update("revoked_at", now).Error; err != nil {
			return fmt.Errorf("revoking recovered manager sessions: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return hsdb.GetAccountByID(accountID)
}

func ValidateAccountPassword(password string) error {
	passwordBytes := len([]byte(password))
	if passwordBytes < accountPasswordMinBytes {
		return fmt.Errorf("password must contain at least %d bytes", accountPasswordMinBytes)
	}

	if passwordBytes > accountPasswordMaxBytes {
		return fmt.Errorf("password must contain at most %d bytes", accountPasswordMaxBytes)
	}
	if strings.IndexFunc(password, unicode.IsControl) >= 0 {
		return errors.New("password must not contain control characters")
	}

	return nil
}

func ValidateAccountUsername(username string) error {
	username = types.NormalizeAccountUsername(username)
	if username == "" || len([]byte(username)) > accountUsernameMaxBytes {
		return fmt.Errorf("username must contain between 1 and %d bytes", accountUsernameMaxBytes)
	}
	if strings.IndexFunc(username, unicode.IsControl) >= 0 {
		return errors.New("username must not contain control characters")
	}
	return nil
}

func HashAccountPassword(password string) (string, error) {
	if err := ValidateAccountPassword(password); err != nil {
		return "", err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hashing password: %w", err)
	}

	return string(hash), nil
}

func migrateLegacyAccountPassword(stored string) (hash string, ok bool, err error) {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return "", false, nil
	}

	if _, costErr := bcrypt.Cost([]byte(stored)); costErr == nil {
		return stored, true, nil
	}
	plainPassword, hasPlainMarker := strings.CutPrefix(stored, "plain:")
	if !hasPlainMarker {
		return "", false, nil
	}
	if err := ValidateAccountPassword(plainPassword); err != nil {
		return "", false, nil
	}
	legacyHash, hashErr := bcrypt.GenerateFromPassword([]byte(plainPassword), bcryptCost)
	if hashErr != nil {
		return "", false, fmt.Errorf("hashing legacy password: %w", hashErr)
	}

	return string(legacyHash), true, nil
}

func (hsdb *HSDatabase) CreateAccount(params CreateAccountParams) (*types.Account, error) {
	username := types.NormalizeAccountUsername(params.Username)
	if err := ValidateAccountUsername(username); err != nil {
		return nil, err
	}

	passwordHash, err := HashAccountPassword(params.Password)
	if err != nil {
		return nil, err
	}

	role := strings.ToLower(strings.TrimSpace(params.Role))
	if role == "" {
		role = types.AccountRoleUser
	}
	if role != types.AccountRoleUser && role != types.AccountRoleManager {
		return nil, errors.New("invalid account role")
	}

	if role == types.AccountRoleUser && params.GroupID == nil {
		return nil, ErrAccountHasNoGroup
	}

	now := time.Now().UTC()
	account := &types.Account{
		Username:           username,
		PasswordHash:       passwordHash,
		GroupID:            params.GroupID,
		Role:               role,
		Enabled:            params.Enabled,
		ExpiresAt:          params.ExpiresAt,
		PasswordChangedAt:  now,
		PasswordVersion:    1,
		MustChangePassword: params.RequirePasswordChange,
	}

	if err := hsdb.Write(func(tx *gorm.DB) error {
		var usernameCount int64
		if err := tx.Model(&types.Account{}).Where("username = ?", username).Count(&usernameCount).Error; err != nil {
			return fmt.Errorf("checking account username: %w", err)
		}
		if usernameCount > 0 {
			return ErrAccountUsernameExists
		}
		if params.GroupID != nil {
			var count int64
			if err := tx.Model(&types.AccountGroup{}).Where("id = ?", *params.GroupID).Count(&count).Error; err != nil {
				return fmt.Errorf("checking account group: %w", err)
			}
			if count == 0 {
				return ErrAccountGroupNotFound
			}
		}

		networkUser := types.User{
			Name:               "account-" + username,
			Provider:           "scaleforge-account",
			ProviderIdentifier: sql.NullString{String: "account:" + username, Valid: true},
		}
		if err := tx.Create(&networkUser).Error; err != nil {
			return fmt.Errorf("creating account network identity: %w", err)
		}
		account.UserID = &networkUser.ID
		if err := tx.Create(account).Error; err != nil {
			return err
		}
		return writeAccountAudit(
			tx,
			params.ActorAccountID,
			"account.create",
			fmt.Sprintf("account:%d", account.ID),
			fmt.Sprintf("created platform account %s", account.Username),
		)
	}); err != nil {
		return nil, fmt.Errorf("creating account: %w", err)
	}

	return account, nil
}

// AuthenticateAccount verifies a human credential and updates lockout/login
// state atomically. When the password is expired, it returns both the account
// and ErrAccountPasswordExpired so ScaleForge can offer only password change.
func (hsdb *HSDatabase) AuthenticateAccount(
	username,
	password string,
	now time.Time,
) (*types.Account, error) {
	normalizedUsername := types.NormalizeAccountUsername(username)
	if ValidateAccountUsername(normalizedUsername) != nil || password == "" ||
		len([]byte(password)) > accountPasswordMaxBytes ||
		strings.IndexFunc(password, unicode.IsControl) >= 0 {
		_ = bcrypt.CompareHashAndPassword(dummyAccountPasswordHash, []byte(password))
		return nil, ErrAccountInvalidCredentials
	}
	var candidate types.Account
	result := hsdb.DB.
		Preload("User").
		Where("username = ?", normalizedUsername).
		First(&candidate)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		_ = bcrypt.CompareHashAndPassword(dummyAccountPasswordHash, []byte(password))
		return nil, ErrAccountInvalidCredentials
	}
	if result.Error != nil {
		return nil, fmt.Errorf("finding account: %w", result.Error)
	}

	if bcrypt.CompareHashAndPassword([]byte(candidate.PasswordHash), []byte(password)) != nil {
		return nil, ErrAccountInvalidCredentials
	}
	if !candidate.Enabled {
		return nil, ErrAccountDisabled
	}
	if candidate.ExpiresAt != nil && !candidate.ExpiresAt.After(now) {
		return nil, ErrAccountExpired
	}

	update := hsdb.DB.Model(&types.Account{}).
		Where("id = ? AND password_version = ? AND password_hash = ? AND enabled = ?",
			candidate.ID, candidate.PasswordVersion, candidate.PasswordHash, true).
		Where("expires_at IS NULL OR expires_at > ?", now).
		Update("last_login_at", now)
	if update.Error != nil {
		return nil, fmt.Errorf("recording successful login: %w", update.Error)
	}
	if update.RowsAffected == 0 {
		return nil, ErrAccountConcurrentUpdate
	}

	candidate.LastLoginAt = &now
	if candidate.PasswordExpired(now) {
		return &candidate, ErrAccountPasswordExpired
	}

	return &candidate, nil
}

func (hsdb *HSDatabase) ChangeAccountPassword(
	accountID uint,
	newPassword string,
	now time.Time,
) error {
	return hsdb.changeAccountPassword(accountID, newPassword, now, false, &accountID, "account.password.change")
}

func (hsdb *HSDatabase) ResetAccountPassword(
	accountID uint,
	newPassword string,
	now time.Time,
	actorAccountID uint,
) error {
	return hsdb.changeAccountPassword(accountID, newPassword, now, true, &actorAccountID, "account.password.reset")
}

func (hsdb *HSDatabase) changeAccountPassword(
	accountID uint,
	newPassword string,
	now time.Time,
	mustChangePassword bool,
	actorAccountID *uint,
	action string,
) error {
	if err := ValidateAccountPassword(newPassword); err != nil {
		return err
	}
	current, err := hsdb.GetAccountByID(accountID)
	if err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(current.PasswordHash), []byte(newPassword)) == nil {
		return ErrAccountPasswordReused
	}
	var history []types.AccountPasswordHistory
	if err := hsdb.DB.Where("account_id = ?", accountID).
		Order("created_at DESC, id DESC").
		Limit(accountPasswordHistory).
		Find(&history).Error; err != nil {
		return fmt.Errorf("reading account password history: %w", err)
	}
	for idx := range history {
		if bcrypt.CompareHashAndPassword([]byte(history[idx].PasswordHash), []byte(newPassword)) == nil {
			return ErrAccountPasswordReused
		}
	}
	passwordHash, err := HashAccountPassword(newPassword)
	if err != nil {
		return err
	}

	return hsdb.Write(func(tx *gorm.DB) error {
		result := tx.Model(&types.Account{}).
			Where("id = ? AND password_version = ?", accountID, current.PasswordVersion).
			Updates(map[string]any{
				"password_hash":        passwordHash,
				"password_changed_at":  now,
				"must_change_password": mustChangePassword,
				"password_version":     gorm.Expr("password_version + 1"),
			})
		if result.Error != nil {
			return fmt.Errorf("changing account password: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return ErrAccountConcurrentUpdate
		}
		if err := tx.Create(&types.AccountPasswordHistory{
			AccountID:    accountID,
			PasswordHash: current.PasswordHash,
			CreatedAt:    now,
		}).Error; err != nil {
			return fmt.Errorf("recording account password history: %w", err)
		}
		var keepIDs []uint
		if err := tx.Model(&types.AccountPasswordHistory{}).
			Where("account_id = ?", accountID).
			Order("created_at DESC, id DESC").
			Limit(accountPasswordHistory).
			Pluck("id", &keepIDs).Error; err != nil {
			return fmt.Errorf("selecting account password history: %w", err)
		}
		if len(keepIDs) > 0 {
			if err := tx.Where("account_id = ? AND id NOT IN ?", accountID, keepIDs).
				Delete(&types.AccountPasswordHistory{}).Error; err != nil {
				return fmt.Errorf("trimming account password history: %w", err)
			}
		}

		if err := tx.Model(&types.AccountSession{}).
			Where("account_id = ? AND revoked_at IS NULL", accountID).
			Update("revoked_at", now).Error; err != nil {
			return fmt.Errorf("revoking account sessions: %w", err)
		}
		if err := clampAccountNodes(tx, current.UserID, now); err != nil {
			return err
		}
		if err := writeAccountAudit(
			tx,
			actorAccountID,
			action,
			fmt.Sprintf("account:%d", accountID),
			fmt.Sprintf("changed password for platform account %s", current.Username),
		); err != nil {
			return err
		}

		return nil
	})
}

func writeAccountAudit(
	tx *gorm.DB,
	actorAccountID *uint,
	action,
	resource,
	content string,
) error {
	if actorAccountID == nil || tx.Dialector.Name() != "postgres" {
		return nil
	}
	if err := tx.Exec(`
INSERT INTO log (account_id, content, action, resource, result, created_at)
VALUES (?, ?, ?, ?, 'success', CURRENT_TIMESTAMP)
`, *actorAccountID, content, action, resource).Error; err != nil {
		return fmt.Errorf("writing account audit event: %w", err)
	}

	return nil
}
