// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

func withLookup(t *testing.T, fn func(context.Context, string) (models.AvailableDrivers, error)) {
	t.Helper()
	orig, origBackoff := lookupAvailableDrivers, providerStoreReadBackoff
	lookupAvailableDrivers = fn
	providerStoreReadBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { lookupAvailableDrivers, providerStoreReadBackoff = orig, origBackoff })
}

func resolveWithStoreDown(t *testing.T) error {
	t.Helper()
	_, err := resolveLLMCall(context.Background(), nil, llmCallSpec{
		UserID: "u1", SessionID: "s", ThreadID: "t",
		Selector: models.ModelSelector{ID: routeModel + "@claude"},
	})
	return err
}

// A failed READ of the user's providers is not "no provider": it must retry,
// not pause the run with a terminal "connect a provider".
func TestResolveLLMCall_StoreErrorIsRetryableNotNoProvider(t *testing.T) {
	calls := 0
	withLookup(t, func(context.Context, string) (models.AvailableDrivers, error) {
		calls++
		return models.AvailableDrivers{}, errors.Join(drivers.ErrDriverLookupFailed, errors.New("connection reset by peer"))
	})

	err := resolveWithStoreDown(t)
	require.Error(t, err)
	assert.Equal(t, 3, calls, "the read is re-tried in place before the activity gives up")
	assert.False(t, errors.Is(err, drivererrors.ErrNoServableProvider), "a store failure is not a verdict about the user's setup")
	assert.NotContains(t, err.Error(), "Connect a provider")

	var scheduled interface {
		RetryAfter() (time.Duration, bool)
		TemporalErrorType() string
	}
	require.ErrorAs(t, err, &scheduled, "it schedules its own retry, which classifyError honours")
	delay, retry := scheduled.RetryAfter()
	assert.True(t, retry)
	assert.Positive(t, delay)
}

// One blip costs nothing: the in-place re-read resolves the turn.
func TestResolveLLMCall_StoreBlipIsAbsorbed(t *testing.T) {
	calls := 0
	good := driversWith(claudeAccount("a", "alice@acme.com", 0))
	withLookup(t, func(context.Context, string) (models.AvailableDrivers, error) {
		calls++
		if calls == 1 {
			return models.AvailableDrivers{}, errors.New("temporary store failure")
		}
		return good, nil
	})
	env := newRoutingEnv(t)
	res, err := resolveOnClaude(t, env, "t", types.FailoverPolicy{})
	require.NoError(t, err)
	assert.Equal(t, "a", res.Account.Config.AccountID)
	assert.Equal(t, 2, calls)
}
