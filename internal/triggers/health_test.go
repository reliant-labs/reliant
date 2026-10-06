// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"testing"
	"time"

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

// Once the run a trigger fired has ended, its recorded result is the firing's,
// whatever a person has done in the chat since: their turn is not the
// automation's. Until then the chat's live state is all there is.
func TestResolveFiring_TheFiredRunsRecordedResultWinsOverThePersonsLaterTurn(t *testing.T) {
	recorded := func(runStatus string, live core.RunDisplayState) *core.TriggerEventWithRun {
		f := launched(live)
		f.Event.RunStatus = runStatus
		return f
	}
	for _, tc := range []struct {
		name string
		f    *core.TriggerEventWithRun
		want firingResult
	}{
		{"fired run failed, person's reply completed", recorded(core.TriggerRunFailed, core.RunDisplayCompleted), resultFailure},
		{"fired run completed, person's reply failed", recorded(core.TriggerRunCompleted, core.RunDisplayFailed), resultSuccess},
		{"fired run completed, person's reply running", recorded(core.TriggerRunCompleted, core.RunDisplayRunning), resultSuccess},
		{"fired run cancelled", recorded(core.TriggerRunCancelled, core.RunDisplayCompleted), resultUnresolved},
		{"fired run still going: live state", launched(core.RunDisplayFailed), resultFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := resolveFiring(tc.f)
			assert.Equal(t, tc.want, got)
		})
	}
	// A recorded failure whose chat has since been deleted is still a failure.
	gone := &core.TriggerEventWithRun{Event: &core.TriggerEvent{Outcome: core.TriggerEventLaunched, RunStatus: core.TriggerRunFailed}}
	got, detail := resolveFiring(gone)
	assert.Equal(t, resultFailure, got)
	assert.Equal(t, "the launched run failed", detail)
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

// A polled source that cannot deliver has no firings, so health must read its
// source state: a dead Gmail connection would otherwise show HEALTHY (or, for
// a new trigger, "never fired") forever while no email ever arrives.
func TestWithSourceFoldsInAPolledSourcesState(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	healthy := func() *reliantv1.TriggerHealth {
		return ComputeHealth([]*core.TriggerEventWithRun{launched(core.RunDisplayCompleted)})
	}
	recent, old := now.Add(-2*time.Hour), now.Add(-3*24*time.Hour)

	// needs_reauth: FAILING, saying what to do, over anything the firings say.
	h := WithSource(healthy(), &core.TriggerRegistration{Status: core.TriggerRegistrationNeedsReauth, StatusDetail: `connection "work" needs to be reconnected`}, now)
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_FAILING, h.Status)
	assert.Contains(t, h.LastFailureDetail, "Not receiving events")
	assert.Contains(t, h.LastFailureDetail, "reconnected")
	h = WithSource(ComputeHealth(nil), &core.TriggerRegistration{Status: core.TriggerRegistrationNeedsReauth}, now)
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_FAILING, h.Status, "even a trigger that never fired")
	assert.Contains(t, h.LastFailureDetail, "reconnect")

	// A failing poll: DEGRADED with the poll's error.
	h = WithSource(healthy(), &core.TriggerRegistration{Status: core.TriggerRegistrationError, StatusDetail: "Gmail returned HTTP 503"}, now)
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_DEGRADED, h.Status)
	assert.Equal(t, "Polling failed: Gmail returned HTTP 503", h.LastFailureDetail)

	// A recent gap: DEGRADED, naming what was lost and when.
	h = WithSource(healthy(), &core.TriggerRegistration{Status: core.TriggerRegistrationActive, LastGapAt: &recent, LastGapDetail: "mail in the gap did not fire"}, now)
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_DEGRADED, h.Status)
	assert.Contains(t, h.LastFailureDetail, "Events were missed")
	assert.Contains(t, h.LastFailureDetail, "2026-10-05 10:00 UTC")
	assert.Contains(t, h.LastFailureDetail, "did not fire")

	// An old gap no longer degrades; an active source with none changes nothing.
	h = WithSource(healthy(), &core.TriggerRegistration{Status: core.TriggerRegistrationActive, LastGapAt: &old, LastGapDetail: "x"}, now)
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_HEALTHY, h.Status)
	assert.Equal(t, healthy(), WithSource(healthy(), &core.TriggerRegistration{Status: core.TriggerRegistrationActive}, now))
	assert.Equal(t, healthy(), WithSource(healthy(), nil, now), "not polled: unchanged")

	// FAILING from the firings stands, with its own detail; a source error does
	// not paper over it.
	failing := ComputeHealth([]*core.TriggerEventWithRun{outcomeOnly(core.TriggerEventFailed, "launch broke"), outcomeOnly(core.TriggerEventFailed, "launch broke")})
	h = WithSource(failing, &core.TriggerRegistration{Status: core.TriggerRegistrationError, StatusDetail: "503", LastGapAt: &recent}, now)
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_FAILING, h.Status)
	assert.Equal(t, "launch broke", h.LastFailureDetail)
}
