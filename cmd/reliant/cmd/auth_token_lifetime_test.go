// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --expires-in asks for a short-lived token, and a token that outlives the
// request (an older control plane that ignores it) is never printed
// (north-star AW-48).
func TestTokenLifetime(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]time.Duration{"": 0, "90m": 90 * time.Minute, "12h": 12 * time.Hour, "7d": 7 * 24 * time.Hour, " 1m ": time.Minute} {
		got, err := parseTokenLifetime(raw)
		require.NoError(t, err, raw)
		assert.Equal(t, want, got, raw)
	}
	for _, raw := range []string{"30s", "0d", "-1h", "soon", "1.5d"} {
		_, err := parseTokenLifetime(raw)
		assert.Error(t, err, raw)
	}

	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	assert.NoError(t, lifetimeMismatch(0, nil, now, "reliant-cli@ci"), "nothing asked, nothing checked")
	assert.NoError(t, lifetimeMismatch(12*time.Hour, at(12*time.Hour), now, "reliant-cli@ci"))
	assert.NoError(t, lifetimeMismatch(12*time.Hour, at(time.Hour), now, "reliant-cli@ci"), "shorter than asked is fine")
	err := lifetimeMismatch(12*time.Hour, at(90*24*time.Hour), now, "reliant-cli@ci")
	require.Error(t, err, "an older control plane's 90-day default is refused")
	assert.Contains(t, err.Error(), "not printed")
	assert.Contains(t, err.Error(), "reliant auth token revoke reliant-cli@ci")
	assert.Error(t, lifetimeMismatch(time.Hour, nil, now, "reliant-cli@ci"), "a token with no expiry outlives any request")
}
