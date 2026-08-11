package db

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/juanfont/headscale/hscontrol/util"
	"gorm.io/gorm"
)

func splitLegacyAccountNodes(tx *gorm.DB) error {
	var accounts []types.Account
	if err := tx.Where("user_id IS NOT NULL").Find(&accounts).Error; err != nil {
		return fmt.Errorf("listing accounts for node split: %w", err)
	}

	now := time.Now().UTC()
	for idx := range accounts {
		account := &accounts[idx]
		var nodes []types.Node
		if err := tx.Where("user_id = ?", *account.UserID).Order("id ASC").Find(&nodes).Error; err != nil {
			return fmt.Errorf("listing account nodes: %w", err)
		}
		if len(nodes) <= 1 {
			continue
		}

		groupID := account.GroupID
		if groupID == nil {
			legacyGroup, err := ensureLegacyAccountGroup(tx)
			if err != nil {
				return err
			}
			groupID = &legacyGroup.ID
		}

		sort.SliceStable(nodes, func(i, j int) bool {
			iPassword := nodes[i].RegisterMethod == util.RegisterMethodPassword
			jPassword := nodes[j].RegisterMethod == util.RegisterMethodPassword
			if iPassword != jPassword {
				return iPassword
			}
			iActive := nodes[i].Expiry == nil || nodes[i].Expiry.After(now)
			jActive := nodes[j].Expiry == nil || nodes[j].Expiry.After(now)
			if iActive != jActive {
				return iActive
			}
			if nodes[i].LastSeen == nil {
				return false
			}
			if nodes[j].LastSeen == nil {
				return true
			}
			return nodes[i].LastSeen.After(*nodes[j].LastSeen)
		})

		for nodeIdx := 1; nodeIdx < len(nodes); nodeIdx++ {
			node := &nodes[nodeIdx]
			username := fmt.Sprintf("legacy-node-%d", node.ID)
			providerID := fmt.Sprintf("legacy-node:%d", node.ID)
			networkUser := types.User{
				Name:               "account-" + username,
				Provider:           "scaleforge-account",
				ProviderIdentifier: sql.NullString{String: providerID, Valid: true},
			}
			if err := tx.Create(&networkUser).Error; err != nil {
				return fmt.Errorf("creating legacy node identity %d: %w", node.ID, err)
			}

			networkUserID := networkUser.ID
			placeholder := types.Account{
				Username:           username,
				PasswordHash:       account.PasswordHash,
				UserID:             &networkUserID,
				GroupID:            groupID,
				Role:               types.AccountRoleUser,
				Enabled:            false,
				PasswordChangedAt:  now,
				MustChangePassword: true,
				PasswordVersion:    1,
			}
			if err := tx.Create(&placeholder).Error; err != nil {
				return fmt.Errorf("creating legacy node account %d: %w", node.ID, err)
			}
			if err := tx.Model(&placeholder).UpdateColumn("enabled", false).Error; err != nil {
				return fmt.Errorf("disabling legacy node account %d: %w", node.ID, err)
			}
			if err := tx.Model(&types.Node{}).Where("id = ?", node.ID).
				Update("user_id", networkUserID).Error; err != nil {
				return fmt.Errorf("assigning legacy node %d: %w", node.ID, err)
			}
		}
	}

	return nil
}

func ensureLegacyAccountGroup(tx *gorm.DB) (*types.AccountGroup, error) {
	const legacyGroupName = "Legacy"

	var group types.AccountGroup
	result := tx.Where("LOWER(name) = LOWER(?)", legacyGroupName).First(&group)
	if result.Error == nil {
		return &group, nil
	}
	if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("finding legacy account group: %w", result.Error)
	}

	group.Name = legacyGroupName
	if err := tx.Create(&group).Error; err != nil {
		return nil, fmt.Errorf("creating legacy account group: %w", err)
	}

	return &group, nil
}

func migrateAccountGroups(tx *gorm.DB) error {
	return tx.Transaction(migrateAccountGroupsInTransaction)
}

func migrateAccountGroupsInTransaction(tx *gorm.DB) error {
	if tx.Dialector.Name() == "sqlite" {
		statements := []string{
			`CREATE TABLE IF NOT EXISTS account_groups (
id integer PRIMARY KEY AUTOINCREMENT,
name text NOT NULL,
created_at datetime,
updated_at datetime,
deleted_at datetime
)`,
			`DROP INDEX IF EXISTS idx_account_groups_name`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_account_groups_name_lower ON account_groups(LOWER(name)) WHERE deleted_at IS NULL`,
			`CREATE INDEX IF NOT EXISTS idx_account_groups_deleted_at ON account_groups(deleted_at)`,
		}
		for _, statement := range statements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("migrating account groups: %w", err)
			}
		}
		if !tx.Migrator().HasColumn("accounts", "group_id") {
			if err := tx.Exec(`ALTER TABLE accounts ADD COLUMN group_id integer`).Error; err != nil {
				return fmt.Errorf("adding account group column: %w", err)
			}
		}
		if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_accounts_group_id ON accounts(group_id)`).Error; err != nil {
			return fmt.Errorf("indexing account groups: %w", err)
		}
		if err := tx.Exec(`
INSERT OR IGNORE INTO account_groups (id, name, created_at, updated_at)
SELECT u.id, u.name, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
FROM users u
WHERE u.deleted_at IS NULL
  AND COALESCE(TRIM(u.name), '') <> ''
  AND NOT EXISTS (
      SELECT 1 FROM accounts manager
      WHERE manager.user_id = u.id
        AND manager.role = 'manager'
        AND manager.deleted_at IS NULL
  )`).Error; err != nil {
			return fmt.Errorf("seeding account groups: %w", err)
		}
		if err := tx.Exec(`
UPDATE accounts
SET group_id = (
    SELECT g.id
    FROM users u
    JOIN account_groups g ON LOWER(g.name) = LOWER(u.name) AND g.deleted_at IS NULL
    WHERE u.id = accounts.user_id
    LIMIT 1
), updated_at = CURRENT_TIMESTAMP
WHERE role = 'user'
  AND group_id IS NULL
  AND EXISTS (
      SELECT 1
      FROM users u
      JOIN account_groups g ON LOWER(g.name) = LOWER(u.name) AND g.deleted_at IS NULL
      WHERE u.id = accounts.user_id
  )`).Error; err != nil {
			return fmt.Errorf("assigning legacy account groups: %w", err)
		}

		return splitLegacyAccountNodes(tx)
	}

	statements := []string{
		`CREATE TABLE IF NOT EXISTS account_groups (
id bigserial PRIMARY KEY,
name varchar(255) NOT NULL,
created_at timestamptz,
updated_at timestamptz,
deleted_at timestamptz
)`,
		`DROP INDEX IF EXISTS idx_account_groups_name`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_account_groups_name_lower ON account_groups(LOWER(name)) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_account_groups_deleted_at ON account_groups(deleted_at)`,
		`ALTER TABLE accounts ADD COLUMN IF NOT EXISTS group_id bigint`,
		`CREATE INDEX IF NOT EXISTS idx_accounts_group_id ON accounts(group_id)`,
		`DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'fk_accounts_group'
          AND conrelid = 'accounts'::regclass
    ) THEN
        ALTER TABLE accounts
            ADD CONSTRAINT fk_accounts_group
            FOREIGN KEY (group_id) REFERENCES account_groups(id)
            ON UPDATE CASCADE ON DELETE RESTRICT;
    END IF;
END $$`,
		`INSERT INTO account_groups (id, name, created_at, updated_at)
SELECT DISTINCT ON (LOWER(u.name)) u.id, u.name, NOW(), NOW()
FROM users u
WHERE u.deleted_at IS NULL
  AND COALESCE(BTRIM(u.name), '') <> ''
  AND NOT EXISTS (
      SELECT 1 FROM accounts manager
      WHERE manager.user_id = u.id
        AND manager.role = 'manager'
        AND manager.deleted_at IS NULL
   )
ORDER BY LOWER(u.name), u.id
ON CONFLICT DO NOTHING`,
		`SELECT setval(
    pg_get_serial_sequence('account_groups', 'id'),
    GREATEST(COALESCE((SELECT MAX(id) FROM account_groups), 1), 1),
    TRUE
)`,
		`UPDATE accounts a
SET group_id = g.id, updated_at = NOW()
FROM users u
JOIN account_groups g ON LOWER(g.name) = LOWER(u.name) AND g.deleted_at IS NULL
WHERE a.user_id = u.id
  AND a.role = 'user'
  AND a.group_id IS NULL`,
	}

	for _, statement := range statements {
		if err := tx.Exec(statement).Error; err != nil {
			return fmt.Errorf("migrating account groups: %w", err)
		}
	}
	if err := splitLegacyAccountNodes(tx); err != nil {
		return err
	}

	constraints := []string{
		`DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'ck_accounts_user_identity_group'
          AND conrelid = 'accounts'::regclass
    ) THEN
        ALTER TABLE accounts
            ADD CONSTRAINT ck_accounts_user_identity_group
            CHECK (
                deleted_at IS NOT NULL
                OR role <> 'user'
                OR (user_id IS NOT NULL AND group_id IS NOT NULL)
            ) NOT VALID;
    END IF;
END $$`,
		`ALTER TABLE accounts VALIDATE CONSTRAINT ck_accounts_user_identity_group`,
	}

	for _, statement := range constraints {
		if err := tx.Exec(statement).Error; err != nil {
			return fmt.Errorf("validating account groups: %w", err)
		}
	}

	return nil
}
