// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/cliauth"
)

// --expires-in asks for a short-lived token, and a token that outlives the
// request (an older control plane that ignores it) is never printed
// (north-star AW-48).
func TestTokenLifetime(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]time.Duration{"": 0, "90m": 90 * time.Minute, "12h": 12 * time.Hour, "7d": 7 * 24 * time.Hour, " 1m ": time.Minute, "90d": 90 * 24 * time.Hour} {
		got, err := parseTokenLifetime(raw)
		require.NoError(t, err, raw)
		assert.Equal(t, want, got, raw)
	}
	for _, raw := range []string{"30s", "0d", "-1h", "soon", "1.5d", "91d", "2161h", "213504d", "9999999999999d"} {
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

// --expires-in at the command's own entry point, both flows: the request
// carries expires_in, a server that honours it gets the token printed, and a
// server that ignores it (an older control plane, 90 days) gets nothing
// printed and a revoke hint (review B1 on north-star AW-48).
func TestAuthTokenCreate_ExpiresIn(t *testing.T) {
	for _, flow := range []string{"--browser", "--device"} {
		t.Run(flow, func(t *testing.T) {
			isolateCLI(t)
			if flow == "--device" {
				// A host no browser can reach: the device flow opens nothing.
				t.Setenv("SSH_CONNECTION", "10.0.0.2 51000 10.0.0.1 22")
				withNoDeviceWait(t)
			} else {
				withFakeLoginBrowser(t)
			}
			for _, honor := range []bool{true, false} {
				cp := newFakeCP(t, "rlat_SHORTLIVED00000")
				cp.offerDevice = flow == "--device"
				cp.honorExpiresIn = honor
				out, err := executeRoot(t, "auth", "token", "create", flow, "--name", "ci", "--expires-in", "12h", "--server", cp.srv.URL)

				var sent string
				if flow == "--device" {
					form, _ := cp.deviceRequests()
					sent = url.Values(form).Get(cliauth.ExpiresInParam)
				} else {
					sent = cp.lastRequest().Get(cliauth.ExpiresInParam)
				}
				assert.Equal(t, "43200", sent, "the request asks for 12h")

				if honor {
					require.NoError(t, err, out)
					assert.Contains(t, out, "rlat_SHORTLIVED00000")
					continue
				}
				require.Error(t, err, "a 90-day grant for a 12h request is refused")
				assert.Contains(t, err.Error(), "reliant auth token revoke reliant-cli@ci")
				assert.NotContains(t, out, "rlat_SHORTLIVED00000", "the overlong token is never printed")
			}
		})
	}
}
