// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/antigravity"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// These tests pin the single highest-risk omission when adding an OAuth
// provider: the resolver picking the WRONG provider's token refresher.
//
// The failure it guards against is not a compile error and not a test failure
// elsewhere — it is a provider that connects cleanly, serves requests for an
// hour, and then dies calling the wrong vendor's token endpoint. Before
// oauthRefresherBuilders existed this was a switch with a `default` arm that
// built Claude's refresher, so a missing case was silently wrong rather than
// visibly missing.
//
// Refreshers are closures, so they cannot be compared for equality. Each test
// instead identifies the selected refresher BEHAVIOURALLY, by the error it
// produces: every provider's refresher wraps its failure with its own name.
// The global API key provider is not initialized in a unit test, so each
// refresher fails at its own "API key provider not initialized" guard — and
// the prefix on that error is the provider that was selected.

func refresherIdentityError(t *testing.T, driverID models.DriverID) string {
	t.Helper()
	refresher, reloader := tokenRefresherForDriver(context.Background(), driverID, "user-under-test")
	require.NotNil(t, refresher, "driver %q has no registered refresher", driverID)
	require.NotNil(t, reloader, "driver %q has no registered reloader", driverID)

	_, err := refresher(llm.OAuthTokens{
		AccessToken:  "at",
		RefreshToken: "rt",
		ExpiresAt:    time.Now().Add(-time.Hour),
	})
	require.Error(t, err, "refresher should fail without an initialized provider")
	return err.Error()
}

// TestTokenRefresherForDriver_AntigravitySelectsItsOwn is the regression test
// for the silent failure. With the old `default: assume Claude` arm this
// returned Claude's refresher and the assertion below failed on the error
// prefix — which is exactly how the bug would have reached production.
func TestTokenRefresherForDriver_AntigravitySelectsItsOwn(t *testing.T) {
	msg := refresherIdentityError(t, models.DriverID(antigravity.DriverID))
	assert.Contains(t, msg, "antigravity token refresh failed",
		"the antigravity driver must get antigravity's refresher, not another provider's")
	assert.NotContains(t, msg, "claude",
		"antigravity must not fall through to Claude's refresher")
}

// TestTokenRefresherForDriver_ExistingProvidersUnchanged pins that extracting
// the switch into a registry did not move Codex or Claude.
func TestTokenRefresherForDriver_ExistingProvidersUnchanged(t *testing.T) {
	assert.Contains(t, refresherIdentityError(t, models.DriverID("codex")),
		"codex token refresh failed")
	// Claude's OAuth credential registers under the driver id "anthropic",
	// not "claude" — provider name is not driver id.
	assert.Contains(t, refresherIdentityError(t, models.DriverID("anthropic")),
		"claude token refresh failed")
}

// TestTokenRefresherForDriver_UnknownDriverGetsNothing pins the property that
// replaced the dangerous default: an unregistered driver installs NO
// refresher. Silently handing it some other provider's is the behaviour this
// whole file exists to prevent.
func TestTokenRefresherForDriver_UnknownDriverGetsNothing(t *testing.T) {
	refresher, reloader := tokenRefresherForDriver(context.Background(), models.DriverID("not-a-real-provider"), "user")
	assert.Nil(t, refresher, "an unregistered driver must not inherit another provider's refresher")
	assert.Nil(t, reloader)
}

// TestDriverID_IsStableSpelling pins the one string that has to agree across
// the proto handler, the provider marker, the DB row and this registry. A
// rename that misses any one site produces a provider that connects but never
// resolves a driver.
func TestDriverID_IsStableSpelling(t *testing.T) {
	assert.Equal(t, "antigravity", antigravity.DriverID)
	_, ok := oauthRefresherBuilders[models.DriverID(antigravity.DriverID)]
	assert.True(t, ok, "antigravity must be registered under its own DriverID constant")
}
