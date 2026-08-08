package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/juanfont/headscale/hscontrol/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const runtimeDNSSettingKey = "dns"

func (hsdb *HSDatabase) LoadRuntimeDNSConfig() (types.RuntimeDNSConfig, bool, error) {
	var setting types.RuntimeSetting
	result := hsdb.DB.First(&setting, "key = ?", runtimeDNSSettingKey)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return types.RuntimeDNSConfig{}, false, nil
	}
	if result.Error != nil {
		return types.RuntimeDNSConfig{}, false, fmt.Errorf("loading runtime DNS setting: %w", result.Error)
	}

	var config types.RuntimeDNSConfig
	if err := json.Unmarshal([]byte(setting.Value), &config); err != nil {
		return types.RuntimeDNSConfig{}, false, fmt.Errorf("decoding runtime DNS setting: %w", err)
	}

	return config, true, nil
}

func (hsdb *HSDatabase) SaveRuntimeDNSConfig(config types.RuntimeDNSConfig) error {
	value, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("encoding runtime DNS setting: %w", err)
	}

	setting := types.RuntimeSetting{
		Key:       runtimeDNSSettingKey,
		Value:     string(value),
		UpdatedAt: time.Now().UTC(),
	}
	if err := hsdb.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&setting).Error; err != nil {
		return fmt.Errorf("saving runtime DNS setting: %w", err)
	}

	return nil
}
