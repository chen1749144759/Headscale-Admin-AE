package db

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/juanfont/headscale/hscontrol/types"
	"gorm.io/gorm"
)

const (
	accountSessionDuration           = 12 * time.Hour
	restrictedAccountSessionDuration = 15 * time.Minute
)

var (
	ErrAccountSessionInvalid    = errors.New("invalid account session")
	ErrAccountSessionExpired    = errors.New("account session expired")
	ErrAccountSessionRestricted = errors.New("account session is restricted")
)

func accountSessionTokenHash(token string) [sha256.Size]byte {
	return sha256.Sum256([]byte(token))
}

func (hsdb *HSDatabase) CleanupAccountSessions(now time.Time) (int64, error) {
	result := hsdb.DB.
		Where("expires_at <= ? OR revoked_at IS NOT NULL", now).
		Delete(&types.AccountSession{})
	if result.Error != nil {
		return 0, fmt.Errorf("cleaning account sessions: %w", result.Error)
	}

	return result.RowsAffected, nil
}

func (hsdb *HSDatabase) CreateAccountSession(
	account *types.Account,
	restricted bool,
	now time.Time,
) (string, *types.AccountSession, error) {
	if _, err := hsdb.CleanupAccountSessions(now); err != nil {
		return "", nil, err
	}

	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return "", nil, fmt.Errorf("generating account session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes[:])
	tokenHash := accountSessionTokenHash(token)

	duration := accountSessionDuration
	if restricted {
		duration = restrictedAccountSessionDuration
	}
	expiresAt := now.Add(duration)
	if !restricted {
		passwordDeadline := account.PasswordChangedAt.Add(types.AccountPasswordMaxAge)
		if passwordDeadline.Before(expiresAt) {
			expiresAt = passwordDeadline
		}
	}
	if account.ExpiresAt != nil && account.ExpiresAt.Before(expiresAt) {
		expiresAt = *account.ExpiresAt
	}

	session := &types.AccountSession{
		TokenHash:       tokenHash[:],
		AccountID:       account.ID,
		PasswordVersion: account.PasswordVersion,
		Restricted:      restricted,
		ExpiresAt:       expiresAt,
		LastSeenAt:      now,
		CreatedAt:       now,
	}
	actorAccountID := account.ID
	if err := hsdb.Write(func(tx *gorm.DB) error {
		if err := tx.Create(session).Error; err != nil {
			return err
		}
		return writeAccountAudit(
			tx,
			&actorAccountID,
			"account.login",
			fmt.Sprintf("account:%d", account.ID),
			fmt.Sprintf("platform account %s logged in", account.Username),
		)
	}); err != nil {
		return "", nil, fmt.Errorf("creating account session: %w", err)
	}

	return token, session, nil
}

func (hsdb *HSDatabase) ValidateAccountSession(
	token string,
	now time.Time,
) (*types.AccountSession, error) {
	if len(token) < 32 || len(token) > 128 {
		return nil, ErrAccountSessionInvalid
	}

	tokenHash := accountSessionTokenHash(token)
	var session types.AccountSession
	result := hsdb.DB.
		Preload("Account").
		Preload("Account.User").
		Where("token_hash = ? AND revoked_at IS NULL", tokenHash[:]).
		First(&session)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, ErrAccountSessionInvalid
	}
	if result.Error != nil {
		return nil, fmt.Errorf("finding account session: %w", result.Error)
	}

	if !session.ExpiresAt.After(now) {
		return nil, ErrAccountSessionExpired
	}
	account := &session.Account
	if account.PasswordVersion != session.PasswordVersion || !account.Enabled {
		return nil, ErrAccountSessionInvalid
	}
	if account.ExpiresAt != nil && !account.ExpiresAt.After(now) {
		return nil, ErrAccountSessionInvalid
	}
	if account.PasswordExpired(now) && !session.Restricted {
		return nil, ErrAccountSessionExpired
	}

	if now.Sub(session.LastSeenAt) >= time.Minute {
		if err := hsdb.DB.Model(&session).Update("last_seen_at", now).Error; err != nil {
			return nil, fmt.Errorf("updating account session: %w", err)
		}
		session.LastSeenAt = now
	}

	return &session, nil
}

func (hsdb *HSDatabase) RevokeAccountSession(token string, now time.Time) error {
	tokenHash := accountSessionTokenHash(token)
	result := hsdb.DB.Model(&types.AccountSession{}).
		Where("token_hash = ? AND revoked_at IS NULL", tokenHash[:]).
		Update("revoked_at", now)
	if result.Error != nil {
		return fmt.Errorf("revoking account session: %w", result.Error)
	}

	return nil
}
