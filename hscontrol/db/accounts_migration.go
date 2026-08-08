package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/juanfont/headscale/hscontrol/types"
	"gorm.io/gorm"
)

var legacyUserAccountColumns = []string{
	"password",
	"expire",
	"cellphone",
	"role",
	"enable",
	"route",
	"node",
}

type legacyUserAccount struct {
	ID        uint
	Name      string
	Email     string
	Password  string
	Expire    sql.NullTime
	Role      string
	Enabled   string
	CreatedAt sql.NullTime
	UpdatedAt sql.NullTime
}

func migrateScaleTailAccounts(tx *gorm.DB) error {
	return tx.Transaction(func(tx *gorm.DB) error {
		if err := createScaleTailAccountTables(tx); err != nil {
			return fmt.Errorf("creating accounts table: %w", err)
		}

		if !tx.Migrator().HasColumn("users", "password") {
			return nil
		}

		rows, err := tx.Table("users").
			Select(strings.Join([]string{
				"id",
				legacyAccountColumn(tx, "name", "''"),
				legacyAccountColumn(tx, "email", "''"),
				"password",
				legacyAccountColumn(tx, "expire", "NULL"),
				legacyAccountColumn(tx, "role", "''"),
				legacyAccountTextColumn(tx, "enable"),
				legacyAccountColumn(tx, "created_at", "NULL"),
				legacyAccountColumn(tx, "updated_at", "NULL"),
			}, ", ")).
			Where("password IS NOT NULL AND password <> ''").
			Rows()
		if err != nil {
			return fmt.Errorf("reading legacy accounts: %w", err)
		}
		var legacyAccounts []legacyUserAccount
		for rows.Next() {
			var legacy legacyUserAccount
			if err := rows.Scan(
				&legacy.ID,
				&legacy.Name,
				&legacy.Email,
				&legacy.Password,
				&legacy.Expire,
				&legacy.Role,
				&legacy.Enabled,
				&legacy.CreatedAt,
				&legacy.UpdatedAt,
			); err != nil {
				rows.Close()
				return fmt.Errorf("reading legacy account: %w", err)
			}
			legacyAccounts = append(legacyAccounts, legacy)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterating legacy accounts: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("closing legacy account rows: %w", err)
		}

		for _, legacy := range legacyAccounts {
			passwordHash, supported, err := migrateLegacyAccountPassword(legacy.Password)
			if err != nil {
				return fmt.Errorf("migrating account for user %d: %w", legacy.ID, err)
			}
			if !supported {
				passwordHash, err = unusableLegacyAccountPasswordHash()
				if err != nil {
					return fmt.Errorf("disabling unsupported legacy account for user %d: %w", legacy.ID, err)
				}
			}

			username := types.NormalizeAccountUsername(legacy.Name)
			if ValidateAccountUsername(username) != nil {
				username = types.NormalizeAccountUsername(legacy.Email)
			}
			if ValidateAccountUsername(username) != nil {
				username = fmt.Sprintf("account-%d", legacy.ID)
			}

			var existing types.Account
			result := tx.Where("username = ?", username).Limit(1).Find(&existing)
			if result.Error != nil {
				return fmt.Errorf("checking migrated account %q: %w", username, result.Error)
			}
			if result.RowsAffected > 0 {
				if existing.UserID != nil && *existing.UserID == legacy.ID {
					continue
				}
				return fmt.Errorf("legacy account username %q conflicts with account %d", username, existing.ID)
			}

			role := legacyAccountRole(legacy.Role)
			enabled := legacyAccountEnabled(legacy.Enabled) && supported
			expiresAt := legacy.Expire
			if role == types.AccountRoleManager && enabled {
				expiresAt = sql.NullTime{}
			}

			passwordChangedAt := legacy.UpdatedAt.Time
			if !legacy.UpdatedAt.Valid || passwordChangedAt.IsZero() {
				passwordChangedAt = legacy.CreatedAt.Time
			}
			if passwordChangedAt.IsZero() {
				passwordChangedAt = time.Now().UTC()
			}

			userID := legacy.ID
			account := types.Account{
				Username:           username,
				PasswordHash:       passwordHash,
				UserID:             &userID,
				Role:               role,
				Enabled:            enabled,
				ExpiresAt:          nullTimePointer(expiresAt),
				PasswordChangedAt:  passwordChangedAt,
				MustChangePassword: true,
				PasswordVersion:    1,
			}
			if err := tx.Create(&account).Error; err != nil {
				return fmt.Errorf("creating migrated account %q: %w", username, err)
			}
			// GORM omits false values for fields with a database default during Create.
			// Write it explicitly so a legacy disabled account cannot become enabled.
			if err := tx.Model(&account).UpdateColumn("enabled", enabled).Error; err != nil {
				return fmt.Errorf("setting enabled state for migrated account %q: %w", username, err)
			}
		}

		return nil
	})
}

func createScaleTailAccountTables(tx *gorm.DB) error {
	statements := []string{}
	if tx.Dialector.Name() == "postgres" {
		statements = []string{
			`CREATE TABLE IF NOT EXISTS accounts (
id bigserial PRIMARY KEY,
username varchar(255) NOT NULL,
password_hash text NOT NULL,
user_id bigint,
role varchar(32) NOT NULL DEFAULT 'user',
enabled boolean NOT NULL DEFAULT true,
expires_at timestamptz,
password_changed_at timestamptz NOT NULL,
must_change_password boolean NOT NULL DEFAULT false,
password_version bigint NOT NULL DEFAULT 1,
last_login_at timestamptz,
created_at timestamptz,
updated_at timestamptz,
deleted_at timestamptz,
CONSTRAINT fk_accounts_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL ON UPDATE CASCADE
)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_username ON accounts(username)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_user_id ON accounts(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_accounts_deleted_at ON accounts(deleted_at)`,
			`CREATE TABLE IF NOT EXISTS account_sessions (
id bigserial PRIMARY KEY,
token_hash bytea NOT NULL,
account_id bigint NOT NULL,
password_version bigint NOT NULL,
restricted boolean NOT NULL DEFAULT false,
expires_at timestamptz NOT NULL,
last_seen_at timestamptz NOT NULL,
created_at timestamptz,
revoked_at timestamptz,
CONSTRAINT fk_account_sessions_account FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_account_sessions_token_hash ON account_sessions(token_hash)`,
			`CREATE INDEX IF NOT EXISTS idx_account_sessions_account_id ON account_sessions(account_id)`,
			`CREATE INDEX IF NOT EXISTS idx_account_sessions_expires_at ON account_sessions(expires_at)`,
			`CREATE TABLE IF NOT EXISTS account_password_histories (
id bigserial PRIMARY KEY,
account_id bigint NOT NULL,
password_hash text NOT NULL,
created_at timestamptz NOT NULL,
CONSTRAINT fk_account_password_histories_account FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
)`,
			`CREATE INDEX IF NOT EXISTS idx_account_password_histories_account_id ON account_password_histories(account_id)`,
		}
	} else {
		statements = []string{
			`CREATE TABLE IF NOT EXISTS accounts (
id integer PRIMARY KEY AUTOINCREMENT,
username text NOT NULL,
password_hash text NOT NULL,
user_id integer,
role text NOT NULL DEFAULT 'user',
enabled numeric NOT NULL DEFAULT true,
expires_at datetime,
password_changed_at datetime NOT NULL,
must_change_password numeric NOT NULL DEFAULT false,
password_version integer NOT NULL DEFAULT 1,
last_login_at datetime,
created_at datetime,
updated_at datetime,
deleted_at datetime,
CONSTRAINT fk_accounts_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL ON UPDATE CASCADE
)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_username ON accounts(username)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_user_id ON accounts(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_accounts_deleted_at ON accounts(deleted_at)`,
			`CREATE TABLE IF NOT EXISTS account_sessions (
id integer PRIMARY KEY AUTOINCREMENT,
token_hash blob NOT NULL,
account_id integer NOT NULL,
password_version integer NOT NULL,
restricted numeric NOT NULL DEFAULT false,
expires_at datetime NOT NULL,
last_seen_at datetime NOT NULL,
created_at datetime,
revoked_at datetime,
CONSTRAINT fk_account_sessions_account FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_account_sessions_token_hash ON account_sessions(token_hash)`,
			`CREATE INDEX IF NOT EXISTS idx_account_sessions_account_id ON account_sessions(account_id)`,
			`CREATE INDEX IF NOT EXISTS idx_account_sessions_expires_at ON account_sessions(expires_at)`,
			`CREATE TABLE IF NOT EXISTS account_password_histories (
id integer PRIMARY KEY AUTOINCREMENT,
account_id integer NOT NULL,
password_hash text NOT NULL,
created_at datetime NOT NULL,
CONSTRAINT fk_account_password_histories_account FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
)`,
			`CREATE INDEX IF NOT EXISTS idx_account_password_histories_account_id ON account_password_histories(account_id)`,
		}
	}
	for _, statement := range statements {
		if err := tx.Exec(statement).Error; err != nil {
			return err
		}
	}

	return nil
}

func legacyAccountColumn(tx *gorm.DB, name, fallback string) string {
	if tx.Migrator().HasColumn("users", name) {
		return name
	}

	return fallback
}

func legacyAccountTextColumn(tx *gorm.DB, name string) string {
	if tx.Migrator().HasColumn("users", name) {
		return fmt.Sprintf("COALESCE(CAST(%s AS TEXT), '')", name)
	}

	return "''"
}

func nullTimePointer(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time

	return &result
}

func unusableLegacyAccountPasswordHash() (string, error) {
	randomPassword := make([]byte, 32)
	if _, err := rand.Read(randomPassword); err != nil {
		return "", fmt.Errorf("generating unusable password: %w", err)
	}
	return HashAccountPassword(base64.RawURLEncoding.EncodeToString(randomPassword))
}

func legacyAccountEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "enabled", "enable", "yes", "on":
		return true
	default:
		return false
	}
}

func legacyAccountRole(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "manager", "admin", "administrator", "owner", "superadmin", "super-admin", "super_admin":
		return types.AccountRoleManager
	default:
		return types.AccountRoleUser
	}
}

func expireLegacyAuthNodes(tx *gorm.DB) error {
	query := `
UPDATE nodes
SET expiry = CURRENT_TIMESTAMP
WHERE COALESCE(register_method, '') <> 'password'
	AND (expiry IS NULL OR expiry > CURRENT_TIMESTAMP);
`
	if tx.Migrator().HasColumn("nodes", "tags") {
		query = `
UPDATE nodes
SET expiry = CURRENT_TIMESTAMP
WHERE (
	COALESCE(register_method, '') <> 'password'
	OR (tags IS NOT NULL AND tags NOT IN ('', '[]', 'null'))
)
	AND (expiry IS NULL OR expiry > CURRENT_TIMESTAMP);
`
	}

	if err := tx.Exec(query).Error; err != nil {
		return fmt.Errorf("expiring legacy-authenticated nodes: %w", err)
	}

	return nil
}
