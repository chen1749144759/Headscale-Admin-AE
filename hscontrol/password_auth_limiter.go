package hscontrol

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/juanfont/headscale/hscontrol/types"
)

const (
	passwordAuthWindow       = time.Minute
	passwordAuthWindowLimit  = 12
	passwordAuthSourceLimit  = 120
	passwordAuthGlobalLimit  = 2000
	passwordAuthEntryTTL     = 10 * time.Minute
	passwordAuthMaxEntries   = 4096
	passwordHashConcurrency  = 8
	passwordHashQueueTimeout = 2 * time.Second
	registrationWindowLimit  = 30
	scaleForgeAuthSource     = "scaleforge-session"
)

var errPasswordAuthRateLimited = errors.New("password authentication rate limited")

type passwordAuthWindowEntry struct {
	windowStart time.Time
	lastSeen    time.Time
	attempts    uint
}

var passwordAuthGuard = struct {
	sync.Mutex
	entries map[string]passwordAuthWindowEntry
	sources map[string]passwordAuthWindowEntry
	calls   uint
	slots   chan struct{}
}{
	entries: make(map[string]passwordAuthWindowEntry),
	sources: make(map[string]passwordAuthWindowEntry),
	slots:   make(chan struct{}, passwordHashConcurrency),
}

var registrationGuard = struct {
	sync.Mutex
	entries map[string]passwordAuthWindowEntry
}{
	entries: make(map[string]passwordAuthWindowEntry),
}

func passwordAuthSource(remoteAddr string) string {
	remoteAddr = strings.TrimSpace(remoteAddr)
	if remoteAddr == "" {
		return "unknown"
	}
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil && host != "" {
		return host
	}
	return remoteAddr
}

func allowPasswordAuthentication(source, username string, now time.Time) bool {
	source = strings.TrimSpace(source)
	key := source + "\x00" + types.NormalizeAccountUsername(username)

	passwordAuthGuard.Lock()
	defer passwordAuthGuard.Unlock()

	passwordAuthGuard.calls++
	if passwordAuthGuard.calls%256 == 0 {
		cutoff := now.Add(-passwordAuthEntryTTL)
		for _, entries := range []map[string]passwordAuthWindowEntry{
			passwordAuthGuard.entries,
			passwordAuthGuard.sources,
		} {
			for entryKey, entry := range entries {
				if entry.lastSeen.Before(cutoff) {
					delete(entries, entryKey)
				}
			}
		}
	}

	if !allowPasswordAuthenticationEntry(
		passwordAuthGuard.sources,
		"\x00global",
		now,
		passwordAuthGlobalLimit,
	) {
		return false
	}
	if !allowPasswordAuthenticationEntry(
		passwordAuthGuard.sources,
		source,
		now,
		passwordAuthSourceLimit,
	) {
		return false
	}
	return allowPasswordAuthenticationEntry(
		passwordAuthGuard.entries,
		key,
		now,
		passwordAuthWindowLimit,
	)
}

func allowPasswordAuthenticationEntry(
	entries map[string]passwordAuthWindowEntry,
	key string,
	now time.Time,
	limit uint,
) bool {
	entry, exists := entries[key]
	if !exists && len(entries) >= passwordAuthMaxEntries {
		var oldestKey string
		var oldestTime time.Time
		for candidateKey, candidate := range entries {
			if oldestKey == "" || candidate.lastSeen.Before(oldestTime) {
				oldestKey = candidateKey
				oldestTime = candidate.lastSeen
			}
		}
		delete(entries, oldestKey)
	}
	if !exists || now.Sub(entry.windowStart) >= passwordAuthWindow {
		entries[key] = passwordAuthWindowEntry{
			windowStart: now,
			lastSeen:    now,
			attempts:    1,
		}
		return true
	}

	entry.lastSeen = now
	if entry.attempts >= limit {
		entries[key] = entry
		return false
	}
	entry.attempts++
	entries[key] = entry
	return true
}

func allowPendingRegistration(source string, now time.Time) bool {
	registrationGuard.Lock()
	defer registrationGuard.Unlock()

	return allowPasswordAuthenticationEntry(
		registrationGuard.entries,
		strings.TrimSpace(source),
		now,
		registrationWindowLimit,
	)
}

func (ns *noiseServer) authenticateAccount(
	ctx context.Context,
	username,
	password string,
	now time.Time,
) (*types.Account, error) {
	if !allowPasswordAuthentication(ns.authSource, username, now) {
		return nil, errPasswordAuthRateLimited
	}

	return ns.headscale.authenticateAccountBounded(ctx, username, password, now)
}

func (h *Headscale) authenticateAccountBounded(
	ctx context.Context,
	username,
	password string,
	now time.Time,
) (*types.Account, error) {

	timer := time.NewTimer(passwordHashQueueTimeout)
	defer timer.Stop()
	select {
	case passwordAuthGuard.slots <- struct{}{}:
		defer func() { <-passwordAuthGuard.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, errPasswordAuthRateLimited
	}

	return h.state.AuthenticateAccount(username, password, now)
}

func (h *Headscale) authenticateScaleForgeAccount(
	ctx context.Context,
	username,
	password string,
	source string,
	now time.Time,
) (*types.Account, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		source = scaleForgeAuthSource
	}
	if !allowPasswordAuthentication("scaleforge:"+source, username, now) {
		return nil, errPasswordAuthRateLimited
	}

	return h.authenticateAccountBounded(ctx, username, password, now)
}
