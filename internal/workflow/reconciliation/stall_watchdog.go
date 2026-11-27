// Copyright (c) 2026 Reliant Labs. All rights reserved.
package reconciliation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/runs"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
	wfobserve "github.com/reliant-labs/reliant/internal/workflow/observe"
	wfruntime "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// The stall watchdog is the server's answer to a run that stays RUNNING with
// nothing executing: reliant bugs ce840d46 and 5d7a10e5 (2026-11-20/21), where
// pr-fix and main-red-fix runs froze between nodes while a long sibling node
// (the lease heartbeat) kept the execution, its lease and its slot alive for
// hours, until a person cancelled each one. control-plane's run-watchdog
// workflow contained it for one project; this is the same duty for every
// user's runs, done where the run state is.
//
// A run is STALLED when all of these hold:
//
//   - it is a RUNNING root run (DB and Temporal), not a conversation
//     (builtin://agent chats wait on their user by design; the progress
//     watchdog is theirs);
//   - nothing in flight parks it: no pending workflow task, child workflow or
//     nexus operation, and no pending activity still inside its own timeout
//     plus StallNodeGrace. A node runs as an activity whose start-to-close is
//     the node's timeout, so a pending activity past timeout + grace is not
//     work the run is waiting on;
//   - it has made no progress, i.e. its Temporal history has not grown, for
//     the threshold: max(its idle ceiling, the longest in-flight node's
//     timeout + grace), measured from the last progress this process saw;
//   - nobody parks it: its chat's activity is RUNNING (not awaiting input,
//     waiting for its machine, queued for a machine or on a usage limit), it
//     has no pending question or approval and no paused row;
//   - the worker fleet is polling (a fleet that is down stalls every run for
//     a known reason).
//
// Modes (ReconcilerConfig.StallWatchdog, reconciler.stall_watchdog):
//
//   - off: nothing above runs; not a query, not a map entry.
//   - report: one WARN, one reliant_reconciler_anomalies_total{class=
//     "stall_watchdog_detected"} and one durable record (node_observations,
//     source "reconciler", under the run id the run's own observations
//     carry: stallTelemetryRunID) per stalled streak. Nothing changes.
//   - enforce: the execution is stopped through the run lifecycle
//     (runs.Service.TerminateExecution): a cooperative cancel, so its
//     finally: releases its leases and slots, addressed to the exact
//     Temporal run the streak watched, so a newer execution of the chat is
//     never stopped. The chat is told why, and the stop is recorded.
//
// The idle ceiling is DefaultStallMaxIdle for every workflow today. A
// workflow-level knob may come later; the per-workflow cap is the run's own
// `timeout:` (E2a).
//
// Streaks live in memory, like the progress watchdog's: a restart (or a
// second replica) only restarts the count, and TerminateExecution refuses
// a run that is no longer current, so the response is idempotent across
// passes and replicas.

// StallWatchdogMode is reconciler.stall_watchdog.
type StallWatchdogMode string

// The three modes. Nothing else is accepted.
const (
	StallWatchdogOff     StallWatchdogMode = "off"
	StallWatchdogReport  StallWatchdogMode = "report"
	StallWatchdogEnforce StallWatchdogMode = "enforce"
)

// ParseStallWatchdogMode reads a configured mode. Empty is off; anything
// other than the three modes is an error, never a guess.
func ParseStallWatchdogMode(s string) (StallWatchdogMode, error) {
	switch m := StallWatchdogMode(strings.TrimSpace(s)); m {
	case "", StallWatchdogOff:
		return StallWatchdogOff, nil
	case StallWatchdogReport, StallWatchdogEnforce:
		return m, nil
	default:
		return StallWatchdogOff, fmt.Errorf("reconciler.stall_watchdog %q: want off, report or enforce", s)
	}
}

const (
	// DefaultStallMaxIdle is the idle ceiling every run gets: above the
	// run-watchdog's longest ceiling (cp-merge's 120m), so nothing it left
	// alone is stalled here.
	DefaultStallMaxIdle = 2 * time.Hour

	// StallNodeGrace is how far past its own timeout an in-flight node still
	// parks the run: room for Temporal's timeout to fire and the node's
	// retry backoff (a minute at most) to start the next attempt.
	StallNodeGrace = 10 * time.Minute

	// stallHistoryScanLimit bounds the history read that learns pending
	// activities' timeouts (Temporal before 1.27 does not describe them).
	stallHistoryScanLimit = 60_000

	// stallRecordStepID is the step_id of the watchdog's node_observations
	// rows: the check, not a node of the workflow.
	stallRecordStepID = "reconciler.stall_watchdog"

	// stallStoppedChatMessage is what the chat reads when enforce stopped
	// its run; %s is the stop reason.
	stallStoppedChatMessage = "This run was stopped because it had stopped making progress (%s)."
)

// The watchdog's anomaly classes (reliant_reconciler_anomalies_total).
const (
	anomalyStallDetected   = "stall_watchdog_detected"
	anomalyStallTerminated = "stall_watchdog_terminated"
)

// PendingActivity is one pending activity as the stall watchdog bounds it.
type PendingActivity struct {
	ActivityID   string
	ActivityType string
	Scheduled    time.Time
	LastStarted  time.Time
	// StartToClose and ScheduleToClose are the activity's timeouts when
	// Temporal describes them (server 1.27+); zero otherwise, and the
	// watchdog reads them from the run's history.
	StartToClose    time.Duration
	ScheduleToClose time.Duration
}

// pendingActivitiesOf is the census of a describe response.
func pendingActivitiesOf(in []*workflowpb.PendingActivityInfo) []PendingActivity {
	out := make([]PendingActivity, 0, len(in))
	for _, pa := range in {
		p := PendingActivity{ActivityID: pa.GetActivityId(), ActivityType: pa.GetActivityType().GetName()}
		if t := pa.GetScheduledTime(); t != nil {
			p.Scheduled = t.AsTime()
		}
		if t := pa.GetLastStartedTime(); t != nil {
			p.LastStarted = t.AsTime()
		}
		if o := pa.GetActivityOptions(); o != nil {
			p.StartToClose = o.GetStartToCloseTimeout().AsDuration()
			p.ScheduleToClose = o.GetScheduleToCloseTimeout().AsDuration()
		}
		out = append(out, p)
	}
	return out
}

// activityTimeouts are an activity's own timeouts.
type activityTimeouts struct{ startToClose, scheduleToClose time.Duration }

// stallObservation is one run's no-progress streak: anchored to the Temporal
// run id and history length it started at, so a new execution, or any
// history growth, starts a fresh one.
type stallObservation struct {
	runID         string
	historyLength int64
	since         time.Time
	// timeouts caches what the history said about pending activities
	// (history does not change while the streak holds).
	timeouts map[string]activityTimeouts
	// startInput is the execution's start input, when that same history
	// read saw it: stallTelemetryRunID then needs no read of its own.
	startInput *commonpb.Payloads
	// done: this streak was reported (report) or settled (enforce).
	done bool
}

// stallCandidate reports whether the run is in the shape the watchdog
// watches at all: a running root run that is not a conversation, with no
// pending workflow task, child or nexus operation, and none of the other
// detectors' conditions.
func stallCandidate(wf *db.Workflow, s *TemporalWorkflowState) bool {
	return wf.ParentID == nil && wf.Status == db.Active() &&
		s.IsRunning && s.RunID != "" &&
		!isConversation(wf.WorkflowName) &&
		!s.HasStuckTask && !s.HasWedgedWorkflowTask &&
		!s.HasPendingWorkflowTask &&
		s.PendingChildrenCount == 0 && s.PendingNexusCount == 0
}

// isConversation reports whether a run is the default conversational agent
// (builtin://agent), which parks on its user's next message by design.
func isConversation(workflowName string) bool {
	name := strings.TrimSpace(workflowName)
	return name == v2workflow.DefaultWorkflow || name == "agent"
}

// watchStall is the stall watchdog's pass over one run. It reports whether
// it stopped the run (enforce). Off, it returns at once.
func (r *Reconciler) watchStall(ctx context.Context, wf *db.Workflow, state *TemporalWorkflowState, pollers *pollerState, stats *passStats) bool {
	if r.stallMode == StallWatchdogOff || r.stallMode == "" {
		return false
	}
	if !stallCandidate(wf, state) {
		r.clearStallObservation(wf.ID)
		return false
	}
	now := r.clock()
	obs := r.observeStall(wf.ID, state, now)
	if obs.done {
		return false
	}
	idle := now.Sub(obs.since)
	if idle < r.stallMaxIdle {
		return false
	}

	// In flight: the latest a pending activity may still legitimately end.
	bound, bounded := r.inFlightBound(ctx, wf, state, obs)
	if !bounded {
		return false
	}
	threshold := r.stallMaxIdle
	if b := bound.Sub(obs.since); b > threshold {
		threshold = b
	}
	if idle < threshold {
		return false
	}

	// Parked by a person, a machine or a usage limit: not a stall.
	parked, err := r.stallParked(ctx, wf)
	if err != nil {
		logging.WarnContext(ctx, "[Reconciler] Stall watchdog could not check what parks the run - holding",
			"workflowID", wf.ID, "chatID", wf.ChatID, "error", err)
		return false
	}
	if parked != "" {
		logging.DebugContext(ctx, "[Reconciler] Stall watchdog: run is parked, not stalled",
			"workflowID", wf.ID, "chatID", wf.ChatID, "parkedOn", parked)
		r.clearStallObservation(wf.ID)
		return false
	}
	// A fleet that is down stalls everything for a known reason: hold.
	for _, kind := range []string{pollerTaskWorkflow, pollerTaskActivity} {
		if active, pollErr := r.pollersActive(ctx, pollers, kind); pollErr != nil || !active {
			return false
		}
	}

	reason := fmt.Sprintf("stalled: no node in flight for %s", idle.Round(time.Minute))
	kv := []any{
		"workflowID", wf.ID, "chatID", wf.ChatID, "workflowName", wf.WorkflowName,
		"temporalRunID", obs.runID, "historyLength", obs.historyLength,
		"idle", idle.Round(time.Second), "threshold", threshold.Round(time.Second),
		"pendingActivities", len(state.PendingActivities), "mode", string(r.stallMode),
		"reason", reason,
	}
	if r.stallMode == StallWatchdogReport {
		logging.WarnContext(ctx, "[Reconciler] Stall watchdog: run stalled (report only, nothing changed)", kv...)
		r.recordAnomaly(stats, anomalyStallDetected)
		r.recordStall(ctx, wf, obs, "detected", reason, idle, threshold)
		r.settleStall(wf.ID)
		return false
	}

	err = r.runs.TerminateExecution(ctx, wf.ChatID, wf.ID, obs.runID)
	switch {
	case errors.Is(err, runs.ErrExecutionNotCurrent):
		// A newer execution (a person's turn, a later chain) replaced the
		// one this streak watched, or it ended: nothing to stop.
		logging.InfoContext(ctx, "[Reconciler] Stall watchdog: the stalled execution is no longer current; left alone", kv...)
		r.clearStallObservation(wf.ID)
		return false
	case err != nil:
		// Retried next pass: the streak stays.
		logging.WarnContext(ctx, "[Reconciler] Stall watchdog could not stop a stalled run; retrying next pass",
			append(kv, "error", err)...)
		return false
	}
	logging.ErrorContext(ctx, "[Reconciler] Stall watchdog stopped a stalled run", kv...)
	r.recordAnomaly(stats, anomalyStallTerminated)
	r.recordStall(ctx, wf, obs, "terminated", reason, idle, threshold)
	r.settleStall(wf.ID)
	if _, msgErr := r.repo.SaveMessageToThread(ctx, wf.ChatID, wf.Thread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM),
		fmt.Sprintf(stallStoppedChatMessage, reason), &wf.ID, nil, nil); msgErr != nil {
		logging.WarnContext(ctx, "[Reconciler] Failed to tell the chat why the stall watchdog stopped its run",
			"workflowID", wf.ID, "error", msgErr)
	}
	return true
}

// observeStall returns the run's streak, starting a fresh one when the run
// moved (another execution, or its history grew).
func (r *Reconciler) observeStall(workflowID string, state *TemporalWorkflowState, now time.Time) *stallObservation {
	r.stallMu.Lock()
	defer r.stallMu.Unlock()
	obs := r.stallObservations[workflowID]
	if obs == nil || obs.runID != state.RunID || obs.historyLength != state.HistoryLength {
		obs = &stallObservation{runID: state.RunID, historyLength: state.HistoryLength, since: now}
		r.stallObservations[workflowID] = obs
	}
	return obs
}

// settleStall marks the streak handled, so a later pass neither reports nor
// stops it again.
func (r *Reconciler) settleStall(workflowID string) {
	r.stallMu.Lock()
	defer r.stallMu.Unlock()
	if obs := r.stallObservations[workflowID]; obs != nil {
		obs.done = true
	}
}

func (r *Reconciler) clearStallObservation(workflowID string) {
	if r.stallMode == StallWatchdogOff {
		return
	}
	r.stallMu.Lock()
	defer r.stallMu.Unlock()
	delete(r.stallObservations, workflowID)
}

// pruneStallObservations drops the streaks of runs no longer in the pass.
func (r *Reconciler) pruneStallObservations(running map[string]bool) {
	if r.stallMode == StallWatchdogOff {
		return
	}
	r.stallMu.Lock()
	defer r.stallMu.Unlock()
	for id := range r.stallObservations {
		if !running[id] {
			delete(r.stallObservations, id)
		}
	}
}

// inFlightBound is the latest time a pending activity of the run may still
// legitimately end: its last start (or schedule) plus its own timeout, plus
// StallNodeGrace. It reports false when a pending activity's timeout cannot
// be learned: work that cannot be bounded is never called stalled.
func (r *Reconciler) inFlightBound(ctx context.Context, wf *db.Workflow, state *TemporalWorkflowState, obs *stallObservation) (time.Time, bool) {
	var bound time.Time
	if len(state.PendingActivities) == 0 {
		return bound, true
	}
	missing := false
	for _, pa := range state.PendingActivities {
		if pa.StartToClose <= 0 && pa.ScheduleToClose <= 0 {
			if _, ok := obs.timeouts[pa.ActivityID]; !ok {
				missing = true
			}
		}
	}
	if missing && obs.timeouts == nil {
		timeouts, startInput, err := r.pendingActivityTimeouts(ctx, wf.ID, obs.runID)
		if err != nil {
			logging.WarnContext(ctx, "[Reconciler] Stall watchdog could not read the run's history for its in-flight timeouts - holding",
				"workflowID", wf.ID, "error", err)
			return bound, false
		}
		r.stallMu.Lock()
		obs.timeouts, obs.startInput = timeouts, startInput
		r.stallMu.Unlock()
	}
	for _, pa := range state.PendingActivities {
		t := activityTimeouts{startToClose: pa.StartToClose, scheduleToClose: pa.ScheduleToClose}
		if t.startToClose <= 0 && t.scheduleToClose <= 0 {
			known, ok := obs.timeouts[pa.ActivityID]
			if !ok {
				return bound, false
			}
			t = known
		}
		deadline, ok := activityDeadline(pa, t)
		if !ok {
			return bound, false
		}
		if deadline = deadline.Add(StallNodeGrace); deadline.After(bound) {
			bound = deadline
		}
	}
	return bound, true
}

// activityDeadline is when one pending activity's current attempt must end:
// its start-to-close from its last start (its schedule when it has not
// started), capped by its schedule-to-close.
func activityDeadline(pa PendingActivity, t activityTimeouts) (time.Time, bool) {
	start := pa.LastStarted
	if start.IsZero() || start.Before(pa.Scheduled) {
		start = pa.Scheduled
	}
	if start.IsZero() {
		return time.Time{}, false
	}
	var deadline time.Time
	if t.startToClose > 0 {
		deadline = start.Add(t.startToClose)
	}
	if t.scheduleToClose > 0 && !pa.Scheduled.IsZero() {
		if capped := pa.Scheduled.Add(t.scheduleToClose); deadline.IsZero() || capped.Before(deadline) {
			deadline = capped
		}
	}
	return deadline, !deadline.IsZero()
}

// pendingActivityTimeouts reads the run's history once for each scheduled
// activity's timeouts, by activity id, and the execution's start input on
// the way (nil when the history did not begin with the start event).
func (r *Reconciler) pendingActivityTimeouts(ctx context.Context, workflowID, runID string) (map[string]activityTimeouts, *commonpb.Payloads, error) {
	out := make(map[string]activityTimeouts)
	var startInput *commonpb.Payloads
	iter := r.tempClient.GetWorkflowHistory(ctx, workflowID, runID, false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for n := 0; iter.HasNext(); n++ {
		if n >= stallHistoryScanLimit {
			return nil, nil, fmt.Errorf("history of %s is longer than %d events", workflowID, stallHistoryScanLimit)
		}
		ev, err := iter.Next()
		if err != nil {
			return nil, nil, fmt.Errorf("read history of %s: %w", workflowID, err)
		}
		if a := ev.GetWorkflowExecutionStartedEventAttributes(); n == 0 && a != nil {
			startInput = a.GetInput()
		}
		if a := ev.GetActivityTaskScheduledEventAttributes(); a != nil {
			out[a.GetActivityId()] = activityTimeouts{
				startToClose:    a.GetStartToCloseTimeout().AsDuration(),
				scheduleToClose: a.GetScheduleToCloseTimeout().AsDuration(),
			}
		}
	}
	return out, startInput, nil
}

// stallTelemetryRunID is the run id the execution the streak watched records
// its node observations under (node_observations.run_id, forge.ai.run.id):
// the root run's, which outlasts the Temporal run id across continue-as-new
// and a restart from a checkpoint (internal/workflow/runtime/run_telemetry.go).
// The run cap of stored observations counts by it, so the watchdog's record
// is attributed to, and counted against, the run it is about, and never
// against another execution of the same workflow.
//
// The execution's start event holds the input it was started with, and
// runtime.RootRunID applies to it the rule the run itself applied.
func (r *Reconciler) stallTelemetryRunID(ctx context.Context, wf *db.Workflow, obs *stallObservation) (string, error) {
	r.stallMu.Lock()
	payloads := obs.startInput
	r.stallMu.Unlock()
	if payloads == nil {
		iter := r.tempClient.GetWorkflowHistory(ctx, wf.ID, obs.runID, false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		if !iter.HasNext() {
			return "", fmt.Errorf("history of %s run %s is empty", wf.ID, obs.runID)
		}
		ev, err := iter.Next()
		if err != nil {
			return "", fmt.Errorf("read the start of %s run %s: %w", wf.ID, obs.runID, err)
		}
		started := ev.GetWorkflowExecutionStartedEventAttributes()
		if started == nil {
			return "", fmt.Errorf("history of %s run %s begins with %s, not the start event", wf.ID, obs.runID, ev.GetEventType())
		}
		payloads = started.GetInput()
	}
	var input wfruntime.WorkflowInput
	if err := r.dataConverter.FromPayloads(payloads, &input); err != nil {
		return "", fmt.Errorf("decode the start input of %s run %s: %w", wf.ID, obs.runID, err)
	}
	return wfruntime.RootRunID(input, obs.runID), nil
}

// stallParked names what legitimately parks the run ("" when nothing does):
// its chat's activity when that is not RUNNING (awaiting input, waiting for
// its machine, queued for a machine, a usage limit), or a pending question,
// approval or paused row (awaitingUserInput).
func (r *Reconciler) stallParked(ctx context.Context, wf *db.Workflow) (string, error) {
	if r.chats != nil {
		chat, err := r.chats.GetChat(ctx, wf.ChatID)
		if err != nil {
			return "", fmt.Errorf("reading the run's chat: %w", err)
		}
		if chat != nil && chat.Activity != nil && *chat.Activity != int(reliantv1.ChatActivity_CHAT_ACTIVITY_RUNNING) {
			return reliantv1.ChatActivity(int32(*chat.Activity)).String(), nil //nolint:gosec // a ChatActivity value
		}
	}
	waiting, err := r.awaitingUserInput(ctx, wf)
	if err != nil {
		return "", err
	}
	if waiting {
		return "user input", nil
	}
	return "", nil
}

// recordStall writes the durable record of a stalled streak: one
// node_observations row per run, streak and outcome (the unique index makes
// a second pass, or a second replica, a no-op).
func (r *Reconciler) recordStall(ctx context.Context, wf *db.Workflow, obs *stallObservation, outcome, reason string, idle, threshold time.Duration) {
	if r.observations == nil {
		return
	}
	// A run id that cannot be learned is left empty, never guessed: the
	// store then counts the row under its workflow, its documented arm for
	// a writer that names no run.
	runID, err := r.stallTelemetryRunID(ctx, wf, obs)
	if err != nil {
		logging.WarnContext(ctx, "[Reconciler] Stall watchdog could not learn the run id of a stalled run; its record names none",
			"workflowID", wf.ID, "temporalRunID", obs.runID, "error", err)
		runID = ""
	}
	attrs, _ := json.Marshal(map[string]any{
		"reliant.stall.outcome":           outcome,
		"reliant.stall.mode":              string(r.stallMode),
		"reliant.stall.reason":            reason,
		"reliant.stall.idle_seconds":      int64(idle / time.Second),
		"reliant.stall.threshold_seconds": int64(threshold / time.Second),
		"reliant.stall.temporal_run_id":   obs.runID,
		"reliant.stall.history_length":    obs.historyLength,
		"reliant.workflow.name":           wf.WorkflowName,
	})
	events, _ := json.Marshal([]map[string]any{{"name": "reliant.reconciler.stall_" + outcome, "attributes": map[string]any{}}})
	_, err = r.observations.RecordNodeObservation(ctx, &db.NodeObservation{
		ID:         uuid.New().String(),
		ChatID:     wf.ChatID,
		WorkflowID: wf.ID,
		RunID:      runID,
		StepID:     stallRecordStepID,
		NodePath:   fmt.Sprintf("%s@%d/%s", obs.runID, obs.historyLength, outcome),
		Attempt:    1,
		Source:     db.NodeObservationSourceReconciler,
		Attributes: string(attrs),
		Events:     string(events),
		Errors:     "[]",
		CreatedAt:  r.clock(),
	}, wfobserve.MaxRowsPerRun)
	if err != nil {
		logging.WarnContext(ctx, "[Reconciler] Stall watchdog could not record a stalled run",
			"workflowID", wf.ID, "outcome", outcome, "error", err)
	}
}
