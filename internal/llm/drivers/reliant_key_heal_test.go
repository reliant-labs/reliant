package drivers

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const legacyKey = "rlnt_abcdef0123456789"

type fakeMinter struct {
	calls atomic.Int32
	err   error
	key   string
}

func (f *fakeMinter) MintForUser(_ context.Context, req accesstokenclient.MintRequest) (accesstokenclient.Minted, error) {
	f.calls.Add(1)
	if f.err != nil {
		return accesstokenclient.Minted{}, f.err
	}
	return accesstokenclient.Minted{Plaintext: f.key, Rotated: req.Rotate}, nil
}

func newHealFixture(t *testing.T, minter *fakeMinter) (*db.Repo, *ReliantKeyHealer) {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	return repo, NewReliantKeyHealer(repo, minter)
}

func storedReliantKey(t *testing.T, repo *db.Repo, user string) string {
	t.Helper()
	k, err := repo.GetProviderAPIKey(context.Background(), user, "reliant")
	require.NoError(t, err)
	return k
}

func TestHeal_ReplacesLegacyKey(t *testing.T) {
	minter := &fakeMinter{key: validRlat}
	repo, h := newHealFixture(t, minter)
	ctx := context.Background()
	require.NoError(t, repo.SetProviderAPIKey(ctx, "u1", "reliant", legacyKey))

	key, healed, err := h.Heal(ctx, "u1")
	require.NoError(t, err)
	assert.True(t, healed)
	assert.Equal(t, validRlat, key)
	assert.Equal(t, validRlat, storedReliantKey(t, repo, "u1"))

	_, healed, err = h.Heal(ctx, "u1")
	require.NoError(t, err)
	assert.False(t, healed, "second heal is a no-op")
	assert.EqualValues(t, 1, minter.calls.Load())
}

func TestHeal_FailureKeepsOldKeyAndReports(t *testing.T) {
	minter := &fakeMinter{err: errors.New("control-plane down")}
	repo, h := newHealFixture(t, minter)
	ctx := context.Background()
	require.NoError(t, repo.SetProviderAPIKey(ctx, "u1", "reliant", legacyKey))

	_, healed, err := h.Heal(ctx, "u1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "control-plane down")
	assert.False(t, healed)
	assert.Equal(t, legacyKey, storedReliantKey(t, repo, "u1"), "old key must survive a failed heal")

	minter.err, minter.key = nil, "rlat_malformed"
	_, _, err = h.Heal(ctx, "u1")
	require.Error(t, err, "a malformed mint must not overwrite the key")
	assert.Equal(t, legacyKey, storedReliantKey(t, repo, "u1"))
}

func TestHeal_ConcurrentHealsMintOnce(t *testing.T) {
	minter := &fakeMinter{key: validRlat}
	repo, h := newHealFixture(t, minter)
	ctx := context.Background()
	require.NoError(t, repo.SetProviderAPIKey(ctx, "u1", "reliant", legacyKey))

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key, _, err := h.Heal(ctx, "u1")
			assert.NoError(t, err)
			assert.Equal(t, validRlat, key)
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, minter.calls.Load(), "concurrent heals must collapse to one mint")
	assert.Equal(t, validRlat, storedReliantKey(t, repo, "u1"))
}

func TestHeal_NoKeyAndValidKeyAreUntouched(t *testing.T) {
	minter := &fakeMinter{key: validRlat}
	repo, h := newHealFixture(t, minter)
	ctx := context.Background()

	key, healed, err := h.Heal(ctx, "nobody")
	require.NoError(t, err)
	assert.False(t, healed)
	assert.Empty(t, key)

	require.NoError(t, repo.SetProviderAPIKey(ctx, "u2", "reliant", validRlat))
	_, healed, err = h.Heal(ctx, "u2")
	require.NoError(t, err)
	assert.False(t, healed)
	assert.EqualValues(t, 0, minter.calls.Load())
}

func TestSweep_HealsAllAndContinuesPastFailures(t *testing.T) {
	minter := &fakeMinter{key: validRlat}
	repo, h := newHealFixture(t, minter)
	ctx := context.Background()
	for _, u := range []string{"a", "b", "c"} {
		require.NoError(t, repo.SetProviderAPIKey(ctx, u, "reliant", legacyKey))
	}
	require.NoError(t, repo.SetProviderAPIKey(ctx, "ok", "reliant", validRlat))

	healed, failed, err := h.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, healed)
	assert.Equal(t, 0, failed)
	for _, u := range []string{"a", "b", "c"} {
		assert.Equal(t, validRlat, storedReliantKey(t, repo, u))
	}

	require.NoError(t, repo.SetProviderAPIKey(ctx, "d", "reliant", legacyKey))
	minter.err = errors.New("boom")
	healed, failed, err = h.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, healed)
	assert.Equal(t, 1, failed)
	assert.Equal(t, legacyKey, storedReliantKey(t, repo, "d"))
}

func TestBuildAvailableDrivers_HealsLegacyKeyOnRead(t *testing.T) {
	minter := &fakeMinter{key: validRlat}
	repo, h := newHealFixture(t, minter)
	SetReliantKeyHealer(h)
	t.Cleanup(func() { SetReliantKeyHealer(nil) })
	ctx := context.Background()
	require.NoError(t, repo.SetProviderAPIKey(ctx, "u1", "reliant", legacyKey))

	available, err := BuildAvailableDrivers(ctx, repo, "u1")
	require.NoError(t, err)
	cfg, ok := available.Drivers["reliant"]
	require.True(t, ok, "legacy key must be healed and the driver registered")
	assert.Equal(t, validRlat, cfg.APIKey)
}
