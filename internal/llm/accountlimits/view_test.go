// Copyright (c) 2025 Reliant Labs

package accountlimits

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func TestEffective(t *testing.T) {
	now := t0
	w := func(used float64, resets time.Time, length time.Duration) core.LimitWindow {
		return core.LimitWindow{Name: "5h", UsedFraction: &used, ResetsAt: resets, LengthS: int64(length / time.Second)}
	}
	cases := []struct {
		name     string
		w        core.LimitWindow
		observed time.Time
		used     float64
		known    bool
	}{
		{"live window", w(0.6, now.Add(time.Hour), 5*time.Hour), now.Add(-time.Minute), 0.6, true},
		{"reset passed: a new window, used 0", w(0.99, now.Add(-time.Second), 5*time.Hour), now.Add(-time.Hour), 0, true},
		{"observed longer ago than its own length, no reset known: unknown", w(0.6, time.Time{}, 5*time.Hour), now.Add(-6 * time.Hour), 0, false},
		{"unknown reset and length, stale", w(0.6, time.Time{}, 0), now.Add(-11 * time.Minute), 0, false},
		{"unknown reset and length, fresh", w(0.6, time.Time{}, 0), now.Add(-9 * time.Minute), 0.6, true},
		{"never observed", w(0.6, now.Add(time.Hour), 5*time.Hour), time.Time{}, 0, false},
		{"no utilization at all", core.LimitWindow{Name: "5h", ResetsAt: now.Add(time.Hour)}, now, 0, false},
		{"clamped", w(1.4, now.Add(time.Hour), 5*time.Hour), now, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			used, known := Effective(tc.w, tc.observed, now)
			assert.Equal(t, tc.known, known)
			assert.InDelta(t, tc.used, used, 1e-9)
		})
	}

	limit, remaining := int64(1000), int64(250)
	used, known := Effective(core.LimitWindow{Name: "requests", Limit: &limit, Remaining: &remaining, ResetsAt: now.Add(time.Minute)}, now, now)
	assert.True(t, known)
	assert.InDelta(t, 0.75, used, 1e-9, "count-based windows derive used from limit and remaining")
}

func TestProtoAccountMapsLimitsToWindowsAndStatus(t *testing.T) {
	now := t0
	acct := core.ProviderAccount{ID: "a1", Provider: "claude", AuthKind: core.AuthKindOAuth, Email: "alice@example.com",
		Plan: "max_20x", OrganizationName: "Org", Priority: 1, CreatedAt: now.Add(-time.Hour)}
	until := now.Add(2 * time.Hour)
	obs := now.Add(-30 * time.Minute)
	limits := &core.LimitState{
		AccountID: "a1", Source: SourceHeaders, ObservedAt: obs,
		LimitedUntil: &until, LimitedWindow: "five_hour", UsageAPIError: "usage endpoint throttled",
		Windows: []core.LimitWindow{
			{Name: "5h", UsedFraction: f64(1), ResetsAt: until, LengthS: 5 * 3600},
			{Name: "7d", UsedFraction: f64(0.4), ResetsAt: now.Add(48 * time.Hour), LengthS: 7 * 86400},
			{Name: "7d.opus", UsedFraction: f64(0.9), LengthS: 7 * 86400},
		},
	}
	got := ProtoAccount(acct, limits, now)

	assert.Equal(t, "Claude subscription", got.ProviderDisplayName)
	assert.Equal(t, "alice@example.com", got.Label)
	assert.Equal(t, "Max 20×", got.GetPlanDisplay())
	assert.Equal(t, pb.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_LIMITED, got.Status)
	assert.Equal(t, until.UTC().Format(time.RFC3339), got.GetLimitedUntil())
	assert.Equal(t, "5h", got.GetLimitedWindow(), "the UI compares a bench against a window's name")
	assert.Equal(t, pb.UsageSource_USAGE_SOURCE_HEADERS, got.UsageSource)
	assert.Equal(t, obs.UTC().Format(time.RFC3339), got.GetUsageObservedAt())
	assert.Equal(t, "usage endpoint throttled", got.GetUsageError())
	require.Len(t, got.Windows, 3)
	assert.Equal(t, "5-hour", got.Windows[0].Label)
	assert.Equal(t, "Weekly", got.Windows[1].Label)
	assert.Equal(t, "7d_opus", got.Windows[2].Name)
	assert.Equal(t, "Weekly · Opus", got.Windows[2].Label)
	assert.Nil(t, got.Windows[2].ResetsAt, "an unknown reset stays unset")
	assert.InDelta(t, 1.0, got.Windows[0].GetUsedFraction(), 1e-9)
}

func TestProtoAccountStatusPrecedenceAndNoLimits(t *testing.T) {
	now := t0
	until := now.Add(time.Hour)
	limited := &core.LimitState{LimitedUntil: &until}
	base := core.ProviderAccount{ID: "a", Provider: "codex", AuthKind: core.AuthKindOAuth}

	assert.Equal(t, pb.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_AVAILABLE, ProtoAccount(base, nil, now).Status)
	assert.Equal(t, pb.UsageSource_USAGE_SOURCE_UNSPECIFIED, ProtoAccount(base, nil, now).UsageSource)
	assert.Equal(t, pb.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_LIMITED, ProtoAccount(base, limited, now).Status)

	past := now.Add(-time.Second)
	assert.Equal(t, pb.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_AVAILABLE,
		ProtoAccount(base, &core.LimitState{LimitedUntil: &past}, now).Status, "an ended bench is just over: lazy, nothing un-benches")

	paused := base
	paused.PausedAt = &now
	assert.Equal(t, pb.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_PAUSED, ProtoAccount(paused, limited, now).Status)

	reauth := paused
	reauth.NeedsReauthAt, reauth.NeedsReauthReason = &now, "invalid_grant"
	got := ProtoAccount(reauth, limited, now)
	assert.Equal(t, pb.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_NEEDS_SIGN_IN, got.Status)
	assert.Equal(t, "invalid_grant", got.GetNeedsSignInReason())

	// A scheduled-but-never-observed row has no windows and no source.
	sched := &core.LimitState{AccountID: "a", Source: SourceUsageAPI}
	got = ProtoAccount(base, sched, now)
	assert.Equal(t, pb.UsageSource_USAGE_SOURCE_UNSPECIFIED, got.UsageSource)
	assert.Nil(t, got.UsageObservedAt)
}

func TestProtoAccountAPIKeyShowsAMaskedHint(t *testing.T) {
	a := core.ProviderAccount{ID: "k", Provider: "anthropic", AuthKind: core.AuthKindAPIKey, KeyHint: "a1b2"}
	got := ProtoAccount(a, nil, t0)
	assert.Equal(t, "Anthropic key …a1b2", got.Label)
	assert.Equal(t, "…a1b2", got.GetMaskedKey())
	assert.Equal(t, "Anthropic API", got.ProviderDisplayName)
}

func TestWindowLabelAndPlanDisplay(t *testing.T) {
	for in, want := range map[string]string{"max_20x": "Max 20×", "max_5x": "Max 5×", "pro": "Pro", "plus": "Plus", "team": "Team", "": ""} {
		assert.Equal(t, want, PlanDisplay(in), in)
	}
	for _, tc := range []struct {
		w    core.LimitWindow
		want string
	}{
		{core.LimitWindow{Name: "primary", LengthS: 5 * 3600}, "5-hour"},
		{core.LimitWindow{Name: "secondary", LengthS: 7 * 86400}, "Weekly"},
		{core.LimitWindow{Name: "codex_other.primary", LengthS: 5 * 3600}, "Codex other · 5-hour"},
		{core.LimitWindow{Name: "7d.opus", LengthS: 7 * 86400}, "Weekly · Opus"},
		{core.LimitWindow{Name: "requests"}, "Requests"},
		{core.LimitWindow{Name: "tokens", LengthS: 60}, "Tokens"},
		{core.LimitWindow{Name: "x", LengthS: 30 * 86400}, "30-day"},
	} {
		assert.Equal(t, tc.want, WindowLabel(tc.w))
	}
}

func TestCanonicalWindowRoundTrips(t *testing.T) {
	for _, n := range []string{"5h", "7d", "primary"} {
		assert.Equal(t, n, DisplayWindow(CanonicalWindow(n)))
	}
	assert.Equal(t, "five_hour", CanonicalWindow("5h"))
}

func TestProbeDelay(t *testing.T) {
	want := []time.Duration{5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour, time.Hour, time.Hour}
	for i, d := range want {
		assert.Equal(t, d, ProbeDelay(i))
	}
	assert.Equal(t, 5*time.Minute, ProbeDelay(-3))
}
