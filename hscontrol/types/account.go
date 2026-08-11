package types

import (
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	AccountRoleUser    = "user"
	AccountRoleManager = "manager"

	AccountPasswordMaxAge = 90 * 24 * time.Hour
)

// Account is the single managed identity used by both ScaleTail clients and
// ScaleForge. Each account owns one internal Headscale network identity and
// belongs to an optional reusable business group.
type Account struct {
	gorm.Model //nolint:embeddedstructfieldcheck

	Username     string `gorm:"size:255;not null;uniqueIndex"`
	PasswordHash string `gorm:"not null" json:"-"`

	// UserID is one-to-one with a Headscale network namespace. This keeps a
	// node's existing UserID sufficient to identify the human account that
	// authenticated it without adding a second device-ownership table.
	UserID *uint `gorm:"uniqueIndex"`
	User   *User `gorm:"constraint:OnUpdate:CASCADE,OnDelete:SET NULL;"`

	GroupID *uint         `gorm:"index"`
	Group   *AccountGroup `gorm:"constraint:OnUpdate:CASCADE,OnDelete:RESTRICT;"`

	Role               string `gorm:"size:32;not null;default:user"`
	Enabled            bool   `gorm:"not null;default:true"`
	ExpiresAt          *time.Time
	PasswordChangedAt  time.Time `gorm:"not null"`
	MustChangePassword bool      `gorm:"not null;default:false"`
	PasswordVersion    uint64    `gorm:"not null;default:1"`
	LastLoginAt        *time.Time
}

// AccountGroup classifies accounts for policy, traffic and administration.
// It is deliberately separate from Headscale's User protocol identity so one
// group can contain many independently authenticated ScaleTail users.
type AccountGroup struct {
	gorm.Model //nolint:embeddedstructfieldcheck

	Name string `gorm:"size:255;not null;uniqueIndex"`
}

// AccountSession is an opaque ScaleForge browser session. Only a SHA-256 hash
// of the random bearer token is persisted.
type AccountSession struct {
	ID uint `gorm:"primaryKey"`

	TokenHash       []byte    `gorm:"size:32;not null;uniqueIndex"`
	AccountID       uint      `gorm:"not null;index"`
	Account         Account   `gorm:"constraint:OnDelete:CASCADE;"`
	PasswordVersion uint64    `gorm:"not null"`
	Restricted      bool      `gorm:"not null;default:false"`
	ExpiresAt       time.Time `gorm:"not null;index"`
	LastSeenAt      time.Time `gorm:"not null"`
	CreatedAt       time.Time
	RevokedAt       *time.Time
}

// AccountPasswordHistory stores previous password hashes so changing A to B
// and immediately back to A cannot bypass the password rotation policy.
type AccountPasswordHistory struct {
	ID uint `gorm:"primaryKey"`

	AccountID    uint      `gorm:"not null;index"`
	Account      Account   `gorm:"constraint:OnDelete:CASCADE;"`
	PasswordHash string    `gorm:"not null" json:"-"`
	CreatedAt    time.Time `gorm:"not null"`
}

func NormalizeAccountUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

func (a *Account) PasswordExpired(now time.Time) bool {
	return a.MustChangePassword ||
		a.PasswordChangedAt.IsZero() ||
		now.Sub(a.PasswordChangedAt) >= AccountPasswordMaxAge
}
