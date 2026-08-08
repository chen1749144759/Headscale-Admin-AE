package types

import "time"

// RuntimeSetting stores small Headscale-owned settings that must survive a
// restart while remaining independently manageable from the startup config.
type RuntimeSetting struct {
	Key       string    `gorm:"primaryKey"`
	Value     string    `gorm:"type:text;not null"`
	UpdatedAt time.Time `gorm:"not null"`
}
