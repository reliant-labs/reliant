// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"testing"

	"github.com/stretchr/testify/assert"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func launched(state core.RunDisplayState) *core.TriggerEventWithRun {
	return &core.TriggerEventWithRun{
		Event: &core.TriggerEvent{Outcome: core.TriggerEventLaunched},
		Run:   &core.TriggerEventRun{DisplayState: state, Title: "nightly"},
	}
}

func outcomeOnly(o core.TriggerEventOutcome, detail string) *core.TriggerEventWithRun {
	return &core.TriggerEventWithRun{Event: &core.TriggerEvent{Outcome: o, OutcomeDetail: detail}}
}

// An event-driven trigger's filter skips most of a busy source's events. That
// is the filter working, so a run of them must not read as DEGRADED the way a
// schedule that keeps standing down does. A pending event is unresolved.
func TestEventDrivenSkipsDoNotDegradeHealth(t *testing.T) {
	inboundSkip := func() *core.TriggerEventWithRun {
		return &core.TriggerEventWithRun{Event: &core.TriggerEvent{
			Kind: core.TriggerEventKindIntegration, Outcome: core.TriggerEventSkipped, OutcomeDetail: "filter did not match",
		}}
	}
	firings := []*core.TriggerEventWithRun{inboundSkip(), inboundSkip(), inboundSkip(), inboundSkip(), launched(core.RunDisplayCompleted)}

	health := ComputeHealth(firings)
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_HEALTHY, health.Status)
	assert.Zero(t, health.ConsecutiveSkips)

	pending := &core.TriggerEventWithRun{Event: &core.TriggerEvent{Kind: core.TriggerEventKindWebhook, Outcome: core.TriggerEventPending}}
	result, _ := resolveFiring(pending)
	assert.Equal(t, resultUnresolved, result, "a pending event has not resolved either way")
}

// Firings are listed newest first, as the store returns them.
func TestComputeHealth(t *testing.T) {
	const (
		healthy  = reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_HEALTHY
		degraded = reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_DEGRADED
		failing  = reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_FAILING
		unknown  = reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_UNKNOWN
	)
	skip := func() *core.TriggerEventWithRun { return outcomeOnly(core.TriggerEventSkipped, "overlap") }
	fail := func() *core.TriggerEventWithRun { return outcomeOnly(core.TriggerEventFailed, "daemon offline") }
	done := func() *core.TriggerEventWithRun { return launched(core.RunDisplayCompleted) }
	died := func() *core.TriggerEventWithRun { return launched(core.RunDisplayFailed) }
	live := func() *core.TriggerEventWithRun { return launched(core.RunDisplayRunning) }
	list := func(f ...*core.TriggerEventWithRun) []*core.TriggerEventWithRun { return f }

	tests := []struct {
		name      string
		firings   []*core.TriggerEventWithRun
		status    reliantv1.TriggerHealthStatus
		failures  int32
		skips     int32
		detailHas string
	}{
		{"never fired", nil, unknown, 0, 0, ""},
		{"only a running run", list(live()), unknown, 0, 0, ""},
		{"completed run", list(done()), healthy, 0, 0, ""},
		{"completed after an old failure recovered to degraded", list(done(), fail()), degraded, 0, 0, "daemon offline"},
		{"one launch failure", list(fail(), done()), degraded, 1, 0, "daemon offline"},
		{"launched then failed counts as a failure", list(died(), done()), degraded, 1, 0, "nightly"},
		{"two launched-then-failed runs", list(died(), died()), failing, 2, 0, "nightly"},
		{"launch failure then run failure", list(fail(), died()), failing, 2, 0, "daemon offline"},
		{"a success ends the failure streak", list(fail(), done(), fail(), fail()), degraded, 1, 0, ""},
		{"still-running run does not end the streak", list(fail(), live(), fail()), failing, 2, 0, ""},
		{"a skip does not end the failure streak", list(fail(), skip(), fail()), failing, 2, 0, ""},
		{"two skips are not yet degraded", list(skip(), skip(), done()), healthy, 0, 2, ""},
		{"three skips degrade", list(skip(), skip(), skip(), done()), degraded, 0, 3, ""},
		{"a run in between ends the skip streak", list(skip(), skip(), live(), skip()), unknown, 0, 2, ""},
		{"cancelled is neither success nor failure", list(launched(core.RunDisplayCancelled)), unknown, 0, 0, ""},
		{"launched with its chat deleted is unresolved", list(&core.TriggerEventWithRun{Event: &core.TriggerEvent{Outcome: core.TriggerEventLaunched}}), unknown, 0, 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeHealth(tc.firings)
			assert.Equal(t, tc.status, got.Status)
			assert.Equal(t, tc.failures, got.ConsecutiveFailures)
			assert.Equal(t, tc.skips, got.ConsecutiveSkips)
			if tc.detailHas != "" {
				assert.Contains(t, got.LastFailureDetail, tc.detailHas)
			}
		})
	}
}

func TestComputeHealth_OnlyReadsTheWindow(t *testing.T) {
	// A failure just outside the 10-firing window must not count.
	firings := make([]*core.TriggerEventWithRun, 0, HealthWindow+1)
	for i := 0; i < HealthWindow; i++ {
		firings = append(firings, launched(core.RunDisplayCompleted))
	}
	firings = append(firings, outcomeOnly(core.TriggerEventFailed, "old"))
	got := ComputeHealth(firings)
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_HEALTHY, got.Status)
	assert.Empty(t, got.LastFailureDetail)
}
