// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/chatmarkers"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// A provider's usage limit is no longer only a wait. The activity that was
// refused reports facts (types.UsageLimitFacts) and the one decision function
// (decideOnUsageLimit) chooses: another account of the same provider now,
// another provider for a lease (opt-in), else the wait.

// limitWithFacts is a usage-limit failure as the activity wrapper hands it to
// Temporal: non-retryable, marker intact, facts in the details.
func limitWithFacts(resetAt time.Time, facts types.UsageLimitFacts) error {
	payload := ""
	if !resetAt.IsZero() {
		payload = resetAt.UTC().Format(time.RFC3339)
	}
	msg := chatmarkers.Wrap(chatmarkers.KindProviderUsageLimit, payload,
		chatmarkers.ProviderUsageLimitLead+" (Claude subscription, 5-hour window) — resets soon")
	return temporal.NewNonRetryableApplicationError(msg, "ProviderUsageLimit", nil, facts)
}

func TestDecideOnUsageLimit_Failover(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	earliest := now.Add(20 * time.Minute)

	t.Run("another account retries at once", func(t *testing.T) {
		d := decideOnUsageLimit(usageLimit{ResetAt: reset}, types.UsageLimitFacts{AlternativeAccount: true}, usageLimitState{}, now, 0)
		require.Equal(t, usageLimitRetryNow, d.Action)
	})

	t.Run("another account beats a configured fallback", func(t *testing.T) {
		d := decideOnUsageLimit(usageLimit{ResetAt: reset},
			types.UsageLimitFacts{AlternativeAccount: true, FallbackModel: "gpt-5.6-sol@codex"}, usageLimitState{}, now, 0)
		require.Equal(t, usageLimitRetryNow, d.Action, "same-provider failover comes first")
	})

	t.Run("a spent switch budget waits instead of looping", func(t *testing.T) {
		d := decideOnUsageLimit(usageLimit{ResetAt: reset}, types.UsageLimitFacts{AlternativeAccount: true},
			usageLimitState{Switches: usageLimitMaxSwitches}, now, 0)
		require.Equal(t, usageLimitWaitUntil, d.Action)
		require.Equal(t, reset.Add(usageLimitMargin), d.Until)
	})

	t.Run("a fallback switches until the provider's earliest reset", func(t *testing.T) {
		d := decideOnUsageLimit(usageLimit{ResetAt: reset},
			types.UsageLimitFacts{FallbackModel: "gpt-5.6-sol@codex", ProviderEarliestReset: earliest}, usageLimitState{}, now, 3*time.Second)
		require.Equal(t, usageLimitSwitchModel, d.Action)
		require.Equal(t, "gpt-5.6-sol@codex", d.Model)
		require.Equal(t, earliest.Add(usageLimitMargin+3*time.Second), d.Until, "the lease ends at the earliest reset across the provider's accounts")
	})

	t.Run("without opt-in it waits for the earliest reset across accounts", func(t *testing.T) {
		d := decideOnUsageLimit(usageLimit{ResetAt: reset},
			types.UsageLimitFacts{ProviderEarliestReset: earliest}, usageLimitState{}, now, 0)
		require.Equal(t, usageLimitWaitUntil, d.Action)
		require.Equal(t, earliest.Add(usageLimitMargin), d.Until, "any account resetting unblocks the thread")
		require.False(t, d.Probing)
	})

	t.Run("no facts is the plain wait", func(t *testing.T) {
		d := decideOnUsageLimit(usageLimit{ResetAt: reset}, types.UsageLimitFacts{}, usageLimitState{}, now, 0)
		require.Equal(t, usageLimitWaitUntil, d.Action)
		require.Equal(t, reset.Add(usageLimitMargin), d.Until)
	})

	t.Run("no reset anywhere climbs the probe ladder, with a fallback too", func(t *testing.T) {
		d := decideOnUsageLimit(usageLimit{}, types.UsageLimitFacts{FallbackModel: "gpt-5.6-sol@codex"}, usageLimitState{Probes: 1}, now, 0)
		require.Equal(t, usageLimitSwitchModel, d.Action)
		require.True(t, d.Probing)
		require.Equal(t, now.Add(15*time.Minute), d.Until)
	})
}

// A bench with no reset behind it ends at a recheck time. That time is the
// next look, so it is a probe: no stated reset, no margin.
func TestDecideOnUsageLimit_ProbeBenchIsNotAReset(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	recheck := now.Add(15 * time.Minute)
	facts := types.UsageLimitFacts{ProviderEarliestReset: recheck, ProviderResetIsProbe: true}

	d := decideOnUsageLimit(usageLimit{}, facts, usageLimitState{Probes: 1}, now, 4*time.Second)
	require.Equal(t, usageLimitWaitUntil, d.Action)
	require.True(t, d.Probing, "the footer must say it is checking again, not that it resumes")
	require.Equal(t, recheck.Add(4*time.Second), d.Until)

	facts.FallbackModel = "gpt-5.6-sol@codex"
	d = decideOnUsageLimit(usageLimit{}, facts, usageLimitState{}, now, 0)
	require.Equal(t, usageLimitSwitchModel, d.Action)
	require.True(t, d.Probing, "a lease on an unknown reset is a lease until the next look")

	facts.ProviderResetIsProbe = false
	d = decideOnUsageLimit(usageLimit{}, facts, usageLimitState{}, now, 0)
	require.False(t, d.Probing)
	require.Equal(t, recheck.Add(usageLimitMargin), d.Until, "the same time with a reset behind it is a reset")
}

func TestParseUsageLimit_ReadsTheFacts(t *testing.T) {
	t.Parallel()
	reset := time.Date(2026, 10, 18, 15, 42, 0, 0, time.UTC)
	want := types.UsageLimitFacts{Provider: "claude", AccountID: "acct-a", AlternativeAccount: true, ProviderEarliestReset: reset, FallbackModel: "x@codex"}
	got, ok := parseUsageLimit(limitWithFacts(reset, want))
	require.True(t, ok)
	require.True(t, got.ResetAt.Equal(reset))
	require.Equal(t, want.Provider, got.Facts.Provider)
	require.Equal(t, want.AccountID, got.Facts.AccountID)
	require.True(t, got.Facts.AlternativeAccount)
	require.Equal(t, want.FallbackModel, got.Facts.FallbackModel)
	require.True(t, got.Facts.ProviderEarliestReset.Equal(reset))

	got, ok = parseUsageLimit(usageLimitError(reset))
	require.True(t, ok, "a failure with no details is still a usage limit")
	require.Equal(t, types.UsageLimitFacts{}, got.Facts)
}

type UsageLimitFailoverSuite struct {
	suite.Suite
	temporaltest.WorkflowTestSuite
}

func TestUsageLimitFailover(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(UsageLimitFailoverSuite))
}

// Another account of the provider can serve: the step is retried at once, with
// no wait marker and no timer, and nothing pauses or fails.
func (s *UsageLimitFailoverSuite) TestAlternativeAccount_RetriesImmediately() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)
	input := spawnE2EWorkflowInput("chat-failover-account")
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	start := env.Now()
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return nil, limitWithFacts(start.Add(3*time.Hour), types.UsageLimitFacts{Provider: "claude", AlternativeAccount: true})
		}
		return nil, nil
	}
	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Equal(2, e.turns(e.parentThread), "refused once, retried once")
	retriedAt, ok := e.callAt("parent", 2)
	s.Require().True(ok)
	s.Less(retriedAt.Sub(start), time.Minute, "no wait: the other account serves now")
	s.Empty(e.markers(), "a switch is not a wait, so there is no marker")
	s.Empty(e.pausedStatuses())
}

// Every account is limited and the chat allows no other provider (the default):
// the thread waits for the EARLIEST reset across the provider's accounts, not
// the refused account's own.
func (s *UsageLimitFailoverSuite) TestAllLimited_WaitsForTheEarliestReset() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)
	input := spawnE2EWorkflowInput("chat-failover-earliest")
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	start := env.Now()
	own := start.Add(4 * time.Hour)
	earliest := start.Add(40 * time.Minute)
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return nil, limitWithFacts(own, types.UsageLimitFacts{Provider: "claude", ProviderEarliestReset: earliest})
		}
		return nil, nil
	}
	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	retriedAt, ok := e.callAt("parent", 2)
	s.Require().True(ok)
	s.False(retriedAt.Before(earliest.Add(usageLimitMargin)), "not before the earliest reset (+ margin)")
	s.Less(retriedAt.Sub(start), 2*time.Hour, "and long before this account's own reset")
	s.Empty(e.notices, "waiting posts no provider-switch notice")
}

// The opt-in cross-provider fallback: the thread continues on the named model
// for the lease, the notice is posted, and the first turn after the lease
// returns to the chat's own model.
func (s *UsageLimitFailoverSuite) TestFallbackLease_RunsOnTheFallbackThenReturns() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)
	input := spawnE2EWorkflowInput("chat-failover-lease")
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	start := env.Now()
	leaseEnd := start.Add(30 * time.Minute)
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return nil, limitWithFacts(leaseEnd, types.UsageLimitFacts{
				Provider: "claude", ProviderEarliestReset: leaseEnd, FallbackModel: "gpt-5.6-sol@codex",
			})
		}
		return nil, nil
	}
	e.hold = func(who string, turn int) string { return "" }
	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())

	// Turn 2 ran at once, on the fallback model, inside the lease.
	t2, ok := e.callAt("parent", 2)
	s.Require().True(ok)
	s.Less(t2.Sub(start), time.Minute, "the switch is immediate")
	e.mu.Lock()
	rt2 := e.callRuntime["parent#2"]
	notices := append([]map[string]interface{}(nil), e.notices...)
	e.mu.Unlock()
	s.Equal("gpt-5.6-sol@codex", rt2.FallbackModel)
	s.Nil(rt2.FallbackReturn)

	s.Require().Len(notices, 1, "one notice at the switch")
	s.Equal("switch_provider", notices[0]["notice"])
	s.Equal("gpt-5.6-sol@codex", notices[0]["model"])
	s.Equal("claude", notices[0]["provider"])
	s.Empty(e.markers(), "a switch is not a wait")
}

// "Use <model> now" on a waiting card answers the wait with a lease on that
// model until the wait's own end, without changing the chat's policy.
func (s *UsageLimitFailoverSuite) TestUseModelNow_TurnsTheWaitIntoALease() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)
	input := spawnE2EWorkflowInput("chat-failover-usemodel")
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	start := env.Now()
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return nil, limitWithFacts(start.Add(4*time.Hour), types.UsageLimitFacts{Provider: "claude", ProviderEarliestReset: start.Add(4 * time.Hour)})
		}
		return nil, nil
	}
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(CapacityWakeSignalName, CapacityWakeSignal{UseModel: "gpt-5.6-sol@codex"})
	}, 10*time.Minute)
	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	t2, ok := e.callAt("parent", 2)
	s.Require().True(ok)
	s.GreaterOrEqual(t2.Sub(start), 10*time.Minute)
	s.Less(t2.Sub(start), time.Hour, "woken by the signal, not the reset")
	e.mu.Lock()
	rt2 := e.callRuntime["parent#2"]
	e.mu.Unlock()
	s.Equal("gpt-5.6-sol@codex", rt2.FallbackModel, "the retry runs on the model the person picked")
}

// A plain "Retry now" (no model) is unchanged: no lease.
func (s *UsageLimitFailoverSuite) TestPlainRetryNow_StartsNoLease() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)
	input := spawnE2EWorkflowInput("chat-failover-plainretry")
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	start := env.Now()
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return nil, limitWithFacts(start.Add(4*time.Hour), types.UsageLimitFacts{Provider: "claude"})
		}
		return nil, nil
	}
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(CapacityWakeSignalName, nil) }, 10*time.Minute)
	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	e.mu.Lock()
	rt2 := e.callRuntime["parent#2"]
	e.mu.Unlock()
	s.Empty(rt2.FallbackModel)
}

// The lease ends at the original provider's reset: the first turn after it is
// back on the chat's own model (no fallback model) and carries what it
// returned from, so the transcript can say so. Here the fallback provider is
// itself limited for longer than the lease, so the thread waits out the lease.
func (s *UsageLimitFailoverSuite) TestLeaseEnd_ReturnsToTheChatsOwnModel() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)
	input := spawnE2EWorkflowInput("chat-failover-return")
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	start := env.Now()
	leaseEnd := start.Add(30 * time.Minute)
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		switch turn {
		case 1:
			return nil, limitWithFacts(leaseEnd, types.UsageLimitFacts{
				Provider: "claude", ProviderEarliestReset: leaseEnd, FallbackModel: "gpt-5.6-sol@codex"})
		case 2:
			// The fallback provider is limited too, for longer than the lease.
			return nil, limitWithFacts(start.Add(90*time.Minute), types.UsageLimitFacts{Provider: "codex"})
		}
		return nil, nil
	}
	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Equal(3, e.turns(e.parentThread))
	e.mu.Lock()
	rt2, rt3 := e.callRuntime["parent#2"], e.callRuntime["parent#3"]
	e.mu.Unlock()
	s.Equal("gpt-5.6-sol@codex", rt2.FallbackModel, "the second turn ran on the fallback")
	s.Empty(rt3.FallbackModel, "after the lease the chat is back on its own model")
	s.Require().NotNil(rt3.FallbackReturn, "and the turn says it just returned")
	s.Equal("claude", rt3.FallbackReturn.Provider)
	s.Equal("gpt-5.6-sol@codex", rt3.FallbackReturn.Model)
}

// With no reset known, a stay's end is only a recheck. The attempt to go back
// to the chat's own provider is refused again, and that is the stay going on:
// the same fallback model with no second "switching" notice and no "limit has
// reset" notice, until an attempt is not refused.
func (s *UsageLimitFailoverSuite) TestUnknownReset_StayOutlivesARefusedReturn() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)
	input := spawnE2EWorkflowInput("chat-failover-stay")
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	start := env.Now()
	const fallback = "gpt-5.6-sol@codex"
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		switch turn {
		case 1:
			// Claude is limited with no reset: the stay runs to the first recheck.
			return nil, limitWithFacts(time.Time{}, types.UsageLimitFacts{
				Provider: "claude", FallbackModel: fallback,
				ProviderEarliestReset: start.Add(5 * time.Minute), ProviderResetIsProbe: true})
		case 2:
			// The fallback is limited past the recheck, so the thread waits it out.
			return nil, limitWithFacts(start.Add(20*time.Minute), types.UsageLimitFacts{Provider: "codex"})
		case 3:
			// Back on Claude after the stay's end: still limited.
			return nil, limitWithFacts(time.Time{}, types.UsageLimitFacts{
				Provider: "claude", FallbackModel: fallback,
				ProviderEarliestReset: start.Add(45 * time.Minute), ProviderResetIsProbe: true})
		}
		return nil, nil
	}
	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Equal(4, e.turns(e.parentThread))
	e.mu.Lock()
	rt2, rt3, rt4 := e.callRuntime["parent#2"], e.callRuntime["parent#3"], e.callRuntime["parent#4"]
	notices := append([]map[string]interface{}(nil), e.notices...)
	e.mu.Unlock()

	s.Equal(fallback, rt2.FallbackModel)
	s.Empty(rt3.FallbackModel, "the stay's end tries the chat's own provider")
	s.NotNil(rt3.FallbackReturn)
	s.Equal(fallback, rt4.FallbackModel, "refused again, so the stay goes on")
	s.Nil(rt4.FallbackReturn, "no return is announced for a refused attempt")

	s.Require().Len(notices, 1, "one switch notice for the whole stay, not one per recheck")
	s.Equal(true, notices[0]["probing"], "the notice is told the time is a recheck, not a reset")
	s.True(rt4.FallbackModel != "" && rt3.FallbackModel == "", "the refused attempt is retried on the fallback at once")
}
