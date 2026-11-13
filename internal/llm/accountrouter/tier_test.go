// Copyright (c) 2025 Reliant Labs

package accountrouter

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

// Tiers (control-plane docs/design/org-provider-accounts.md §4.3, §4.6): a
// thread is placed on the best tier that has an account serving the model,
// Balance never mixes tiers, and a limited tier waits rather than spends the
// next tier's money.

func tiered(id string, priority int, tier models.AccountTier) models.AccountConfig {
	a := acct(id, priority)
	a.Tier = tier
	return a
}

// Balance never mixes tiers: however much less used the second tier is, a
// new thread lands on the first while it has a usable account.
func TestTier_BalanceNeverMixesTiers(t *testing.T) {
	for seed := range uint64(80) {
		s := newStore()
		s.put(usage("org-1", window("5h", 0.95, time.Hour, 5*time.Hour)))
		s.put(usage("org-2", window("5h", 0.80, time.Hour, 5*time.Hour)))
		s.put(usage("mine", window("5h", 0.00, time.Hour, 5*time.Hour)))
		rng := rand.New(rand.NewPCG(seed, 3))
		r := newRouter(t, s, func(n int) int { return rng.IntN(n) })
		res, err := r.Assign(context.Background(), req("th",
			tiered("mine", 0, models.TierSecond), tiered("org-1", 0, models.TierFirst), tiered("org-2", 1, models.TierFirst)))
		require.NoError(t, err)
		assert.Equal(t, models.TierFirst, res.Account.Tier, "seed %d: %s", seed, res.Account.AccountID)
		assert.Equal(t, "org-2", res.Account.AccountID, "the least used of the first tier")
	}
}

// A first tier whose every account is limited makes the thread wait for its
// earliest reset; the second tier's idle account is not spent (§4.6 case 3:
// that is the person's question to answer, not the router's).
func TestTier_LimitedFirstTierWaitsInsteadOfSpilling(t *testing.T) {
	s := newStore()
	s.put(benched("org-1", t0.Add(2*time.Hour), "5h"))
	s.put(benched("org-2", t0.Add(time.Hour), "weekly"))
	r := newRouter(t, s, first)
	rq := req("th", tiered("org-1", 0, models.TierFirst), tiered("org-2", 1, models.TierFirst), tiered("mine", 0, models.TierSecond))

	_, err := r.Assign(context.Background(), rq)
	var all *ErrAllLimited
	require.ErrorAs(t, err, &all)
	assert.Equal(t, t0.Add(time.Hour), all.EarliestReset, "the first tier's earliest reset")
	assert.Equal(t, "weekly", all.Window)

	available, earliest, err := r.Alternatives(context.Background(), rq, "org-1")
	require.NoError(t, err)
	assert.False(t, available, "no alternative within the tier: the second tier is not one")
	assert.Equal(t, t0.Add(time.Hour), earliest.At)
}

// A first-tier account that cannot serve the model does not hold the thread:
// the best tier is the best that has an account serving it.
func TestTier_UnservedFirstTierFallsToTheTierThatServes(t *testing.T) {
	s := newStore()
	org := tiered("org-1", 0, models.TierFirst)
	org.Serves = func(string) bool { return false }
	res, err := newRouter(t, s, first).Assign(context.Background(), req("th", org, tiered("mine", 0, models.TierSecond)))
	require.NoError(t, err)
	assert.Equal(t, "mine", res.Account.AccountID)
}

// A sticky thread is never moved mid-turn because a better tier appeared:
// the upgrade happens at a turn start, not in the router.
func TestTier_StickySecondTierAccountStays(t *testing.T) {
	s := newStore()
	s.assign("th", "claude", "mine")
	res, err := newRouter(t, s, first).Assign(context.Background(),
		req("th", tiered("org-1", 0, models.TierFirst), tiered("mine", 0, models.TierSecond)))
	require.NoError(t, err)
	assert.Equal(t, "mine", res.Account.AccountID)
	assert.False(t, res.Changed)
}

// A thread whose first-tier account is limited moves within the tier, and
// never to the second while the tier has a usable account.
func TestTier_LimitedAccountMovesWithinItsTier(t *testing.T) {
	s := newStore()
	s.assign("th", "claude", "org-1")
	s.put(benched("org-1", t0.Add(time.Hour), "5h"))
	res, err := newRouter(t, s, first).Assign(context.Background(),
		req("th", tiered("org-1", 0, models.TierFirst), tiered("mine", 0, models.TierSecond), tiered("org-2", 1, models.TierFirst)))
	require.NoError(t, err)
	assert.Equal(t, "org-2", res.Account.AccountID)
	assert.Equal(t, ReasonLimited, res.Reason)
}

// A preferred account is an explicit choice: it serves whenever usable,
// whatever its tier.
func TestTier_PreferredAccountWinsAcrossTiers(t *testing.T) {
	s := newStore()
	rq := req("th", tiered("org-1", 0, models.TierFirst), tiered("mine", 0, models.TierSecond))
	rq.Preferred = "claude:mine@example.com"
	res, err := newRouter(t, s, first).Assign(context.Background(), rq)
	require.NoError(t, err)
	assert.Equal(t, "mine", res.Account.AccountID)
	assert.Equal(t, ReasonPinned, res.Reason)
}
