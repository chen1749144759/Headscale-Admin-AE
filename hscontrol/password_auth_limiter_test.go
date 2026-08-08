package hscontrol

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPasswordAuthSource(t *testing.T) {
	require.Equal(t, "192.0.2.8", passwordAuthSource("192.0.2.8:443"))
	require.Equal(t, "2001:db8::8", passwordAuthSource("[2001:db8::8]:443"))
	require.Equal(t, "unknown", passwordAuthSource(""))
}

func TestPasswordAuthenticationWindowLimit(t *testing.T) {
	now := time.Now()
	for range passwordAuthWindowLimit {
		require.True(t, allowPasswordAuthentication("192.0.2.30", t.Name(), now))
	}
	require.False(t, allowPasswordAuthentication("192.0.2.30", t.Name(), now))
	require.True(t, allowPasswordAuthentication(
		"192.0.2.30",
		t.Name(),
		now.Add(passwordAuthWindow),
	))
}

func TestScaleForgePasswordAuthenticationWindowIsPerUsername(t *testing.T) {
	now := time.Now()
	for range passwordAuthWindowLimit {
		require.True(t, allowPasswordAuthentication(scaleForgeAuthSource, t.Name()+"-first", now))
	}
	require.False(t, allowPasswordAuthentication(scaleForgeAuthSource, t.Name()+"-first", now))
	require.True(t, allowPasswordAuthentication(scaleForgeAuthSource, t.Name()+"-second", now))
}

func TestPasswordAuthenticationSourceLimitStopsUsernameRotation(t *testing.T) {
	now := time.Now()
	source := "198.51.100.200-" + t.Name()
	for attempt := range passwordAuthSourceLimit {
		if !allowPasswordAuthentication(source, t.Name()+string(rune(attempt+1)), now) {
			t.Fatalf("source limit closed at attempt %d", attempt)
		}
	}
	if allowPasswordAuthentication(source, "rotated-again", now) {
		t.Fatal("username rotation bypassed the source-wide limit")
	}
}

func TestPasswordAuthenticationEntryCapacityEvictsOldest(t *testing.T) {
	now := time.Now()
	entries := make(map[string]passwordAuthWindowEntry, passwordAuthMaxEntries)
	for idx := range passwordAuthMaxEntries {
		entries[string(rune(idx+1))] = passwordAuthWindowEntry{
			windowStart: now,
			lastSeen:    now.Add(time.Duration(idx) * time.Second),
			attempts:    1,
		}
	}
	if !allowPasswordAuthenticationEntry(entries, "new-entry", now, passwordAuthWindowLimit) {
		t.Fatal("new entry was denied when the bounded map was full")
	}
	if len(entries) != passwordAuthMaxEntries {
		t.Fatalf("entry count = %d, want %d", len(entries), passwordAuthMaxEntries)
	}
	if _, ok := entries[string(rune(1))]; ok {
		t.Fatal("oldest entry was not evicted")
	}
}

func TestPendingRegistrationSourceLimit(t *testing.T) {
	now := time.Now()
	source := "203.0.113.200-" + t.Name()
	for range registrationWindowLimit {
		if !allowPendingRegistration(source, now) {
			t.Fatal("registration window closed before its limit")
		}
	}
	if allowPendingRegistration(source, now) {
		t.Fatal("registration source exceeded its pending-session limit")
	}
}
