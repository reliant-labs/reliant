package drivers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/anthropic"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// subscriptionAndKeyRepo is a user with BOTH a Claude subscription (OAuth) and
// an Anthropic API key connected.
type subscriptionAndKeyRepo struct{}

var _ AvailabilityStore = subscriptionAndKeyRepo{}

func (subscriptionAndKeyRepo) ListProviderAccountsWithSecrets(context.Context, string) ([]core.AccountWithSecrets, error) {
	return []core.AccountWithSecrets{
		{
			ProviderAccount: core.ProviderAccount{ID: "sub-1", UserID: "user-1", Provider: "claude", AuthKind: core.AuthKindOAuth,
				ExpiresAt: time.Now().Add(time.Hour), ExternalAccountID: "acct-1"},
			Secrets: core.AccountSecrets{Secret: "sk-ant-oat01-sub", Refresh: "refresh"},
		},
		{
			ProviderAccount: core.ProviderAccount{ID: "key-1", UserID: "user-1", Provider: "anthropic", AuthKind: core.AuthKindAPIKey},
			Secrets:         core.AccountSecrets{Secret: "sk-ant-api03-key"},
		},
	}, nil
}

func (subscriptionAndKeyRepo) ListAccountLimits(context.Context, string) (map[string]core.LimitState, error) {
	return nil, nil
}

func bothConnected(t *testing.T) models.AvailableDrivers {
	t.Helper()
	available, err := buildAvailableDrivers(context.Background(), subscriptionAndKeyRepo{}, "user-1", noReportClients)
	require.NoError(t, err)
	return available
}

// THE DEFECT: the subscription was registered under "anthropic", where the
// API key also lands, so one of the two credentials silently won by map order.
func TestClaudeSubscriptionAndAnthropicKeyCoexist(t *testing.T) {
	t.Parallel()
	available := bothConnected(t)

	sub, ok := available.Drivers["claude"]
	require.True(t, ok, "the subscription is its own provider")
	assert.Equal(t, models.DriverID("claude"), sub.DriverID)
	assert.Equal(t, "sk-ant-oat01-sub", sub.APIKey)
	assert.Equal(t, "refresh", sub.RefreshToken)

	key, ok := available.Drivers["anthropic"]
	require.True(t, ok, "the API key is its own provider")
	assert.Equal(t, "sk-ant-api03-key", key.APIKey)

	// Stable across repeated builds: no map-order winner.
	for i := 0; i < 20; i++ {
		again := bothConnected(t)
		assert.Equal(t, "sk-ant-api03-key", again.Drivers["anthropic"].APIKey)
		assert.Equal(t, "sk-ant-oat01-sub", again.Drivers["claude"].APIKey)
	}
}

func TestClaudeAndAnthropicResolveToTheirOwnClients(t *testing.T) {
	t.Parallel()
	available := bothConnected(t)

	for driverID, check := range map[models.DriverID]func(registry.Client) bool{
		"claude":    func(c registry.Client) bool { _, ok := c.(*anthropic.ClaudeCodeClient); return ok },
		"anthropic": func(c registry.Client) bool { _, ok := c.(*anthropic.AnthropicClient); return ok },
	} {
		cfg := available.Drivers[driverID]
		factory, ok := registry.GetDriverFactory(models.Family(driverID))
		require.True(t, ok, driverID)
		client, err := factory(&llm.DriverOptions{ApiKey: cfg.APIKey})
		require.NoError(t, err)
		assert.True(t, check(client), "%s built %T", driverID, client)
	}
	// The API-key client no longer sniffs sk-ant-oat: an OAuth token under
	// "anthropic" is not silently re-routed.
	factory, _ := registry.GetDriverFactory("anthropic")
	client, err := factory(&llm.DriverOptions{ApiKey: "sk-ant-oat01-x"})
	require.NoError(t, err)
	assert.IsType(t, &anthropic.AnthropicClient{}, client)
}

func TestClaudeAndAnthropicPinsEachWork(t *testing.T) {
	t.Parallel()
	available := bothConnected(t)
	reg := models.MustGetRegistry().WithAvailability(available.Availability)
	providers := servableProviderIDs(available)

	for _, driver := range []string{"claude", "anthropic"} {
		resolved, err := reg.Resolve(models.ModelSelector{ID: "claude-5.5-opus@" + driver}, providers)
		require.NoError(t, err, driver)
		assert.Equal(t, driver, resolved.Provider.Driver)
		assert.True(t, models.CanDriverUseModel(models.Family(driver), "claude-5.5-opus"))
	}

	cfg, found := models.SelectBestDriver("claude-5.5-opus", available)
	require.True(t, found)
	assert.Equal(t, models.DriverID("claude"), cfg.DriverID, "an unpinned model prefers the prepaid subscription")
}

func TestTierSelectorPrefersTheSubscriptionOverTheMeteredKey(t *testing.T) {
	t.Parallel()
	available := bothConnected(t)
	reg := models.MustGetRegistry().WithAvailability(available.Availability)
	providers := servableProviderIDs(available)

	for _, tier := range []string{models.TagFlagship, models.TagModerate, models.TagFast} {
		resolved, err := reg.Resolve(models.ModelSelector{Tags: []string{tier}}, providers)
		require.NoError(t, err, tier)
		assert.Equal(t, "claude", resolved.Provider.Driver, "tier %s", tier)
	}

	// With only the key connected, the key serves.
	resolved, err := models.MustGetRegistry().Resolve(models.ModelSelector{Tags: []string{models.TagFlagship}}, []string{"anthropic"})
	require.NoError(t, err)
	assert.Equal(t, "anthropic", resolved.Provider.Driver)
}

// A chat or preset pinned to <model>@anthropic by a user who only has the
// subscription moves to the same model on claude and says so.
func TestPinToAnthropicSubstitutesToSubscriptionWhenOnlyTheSubscriptionIsConnected(t *testing.T) {
	t.Parallel()
	sub, ok := SubstituteUnservableModel(map[string]interface{}{"id": "claude-5.5-opus@anthropic", "thinking_level": "low"}, claudeOnly())
	require.True(t, ok)
	assert.Equal(t, "claude-5.5-opus@anthropic", sub.From)
	assert.Equal(t, "claude-5.5-opus@claude", sub.To)
	assert.Equal(t, "low", sub.Selector["thinking_level"], "same model elsewhere keeps the effort")
	assert.Contains(t, sub.Notice, "Anthropic API")
	assert.Contains(t, sub.Notice, "Claude subscription")

	// Both connected: the pin resolves, nothing is substituted.
	_, ok = SubstituteUnservableModel(map[string]interface{}{"id": "claude-5.5-opus@anthropic"}, bothConnected(t))
	assert.False(t, ok)
}

// The retired family name is no longer a driver; a stale pin to it is not
// served silently, it is substituted like any unservable pin.
func TestPinToRemovedClaudeCodeIdIsSubstituted(t *testing.T) {
	t.Parallel()
	sub, ok := SubstituteUnservableModel(map[string]interface{}{"id": "claude-5.5-opus@claude-code"}, claudeOnly())
	require.True(t, ok)
	assert.Equal(t, "claude-5.5-opus@claude", sub.To)
}

// A run's provider preference (a chat pinned to one of the two) steers a tier
// to that credential even though the subscription leads by priority.
func TestProviderPreferenceSteersTierBetweenSubscriptionAndKey(t *testing.T) {
	t.Parallel()
	available := bothConnected(t)
	reg := models.MustGetRegistry().WithAvailability(available.Availability)
	providers := servableProviderIDs(available)

	for _, driver := range []string{"claude", "anthropic"} {
		resolved, err := reg.Resolve(models.ModelSelector{Tags: []string{models.TagFlagship}, Providers: []string{driver}}, providers)
		require.NoError(t, err, driver)
		assert.Equal(t, driver, resolved.Provider.Driver)
	}
}
