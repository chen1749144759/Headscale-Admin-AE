package db

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/juanfont/headscale/hscontrol/types"
	"gorm.io/gorm"
)

var (
	ErrAccountGroupExists = errors.New("account group already exists")
	ErrAccountGroupInUse  = errors.New("account group is still in use")
)

func normalizeAccountGroupName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]byte(name)) > 255 {
		return "", errors.New("group name must contain between 1 and 255 bytes")
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", errors.New("group name must not contain control characters")
	}

	return name, nil
}

func (hsdb *HSDatabase) ListAccountGroups() ([]types.AccountGroup, error) {
	var groups []types.AccountGroup
	if err := hsdb.DB.Order("name ASC, id ASC").Find(&groups).Error; err != nil {
		return nil, fmt.Errorf("listing account groups: %w", err)
	}

	return groups, nil
}

func (hsdb *HSDatabase) CreateAccountGroup(name string) (*types.AccountGroup, error) {
	normalized, err := normalizeAccountGroupName(name)
	if err != nil {
		return nil, err
	}

	group := &types.AccountGroup{Name: normalized}
	if err := hsdb.Write(func(tx *gorm.DB) error {
		var existing int64
		if err := tx.Model(&types.AccountGroup{}).
			Where("LOWER(name) = LOWER(?)", normalized).
			Count(&existing).Error; err != nil {
			return fmt.Errorf("checking account group: %w", err)
		}
		if existing > 0 {
			return ErrAccountGroupExists
		}
		if err := tx.Create(group).Error; err != nil {
			return fmt.Errorf("creating account group: %w", err)
		}

		return nil
	}); err != nil {
		return nil, err
	}

	return group, nil
}

func (hsdb *HSDatabase) DeleteAccountGroup(groupID uint) error {
	return hsdb.Write(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&types.Account{}).Where("group_id = ?", groupID).Count(&count).Error; err != nil {
			return fmt.Errorf("counting group accounts: %w", err)
		}
		if count > 0 {
			return ErrAccountGroupInUse
		}

		result := tx.Delete(&types.AccountGroup{}, groupID)
		if result.Error != nil {
			return fmt.Errorf("deleting account group: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}

		return nil
	})
}
