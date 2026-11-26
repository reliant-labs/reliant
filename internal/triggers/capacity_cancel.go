// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"log/slog"

	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/converter"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// ── concurrency.cancel_running: a newer firing stops the key's running run ──
//
// Under max_running_per_key: 1 a firing of a busy key waits until the key's
// running run ends. With cancel_running the firing stops that run instead:
// on_new_key_event for any newer firing of the key, on_new_head only when the
// firing's pull request head differs from the one the running run was fired
// for. The pr workflow uses on_new_head, so a push stops the review of the
// head it replaced rather than queueing behind it.
//
// Where: in prepareCapacity, after the firing is enrolled and before the
// occupied slots are reconciled. So a run that stops within the cancel's wait
// is seen closed by that reconcile, its slot released, and the firing admitted
// in the same fire; one still running its finally: when the wait ends is
// released by a later fire or the redriver's pass, as any run that ends.
//
// Which runs: those ListTriggerCapacityKeyRuns returns, i.e. launched runs of
// the firing's key that enrolled BEFORE it and still hold their slot. A
// firing never cancels a newer one's run, so two firings racing for one key
// cannot cancel each other's work in a loop: the newest wins, under waiting.
// The slot hand-off itself is admission's, unchanged: a cancelled run holds
// its slot until Temporal reports it closed (reconcileCapacityExecution), and
// the next firing is admitted under the scope lock (CanLaunchTriggerCapacity).
//
// How: the run lifecycle's cancel (RunCanceller), never a second mechanism,
// addressed to one exact execution. The chat's workflow id is reused by later
// human turns, and a turn a person started must never be cancelled for an
// automation's firing. So the cancel is decided on one Temporal description
// (the slot's own chain, running) and asked for the run id read in that same
// description: the run lifecycle revalidates it under the chat's run-control
// lock and addresses every Temporal call to it, so a chain that replaced it
// in between is refused, never cancelled.
//
// The "superseded by event <id>" note is written only once that cancel has
// succeeded: a refused or failed cancel leaves the run's firing as it was,
// and the next fire decides again.
//
// Every fire of a waiting firing re-derives this, so a cancel that failed
// (Temporal unreachable, a start not yet visible) is retried by the next fire
// of the firing: the redriver's, or a retry. Best effort throughout: a
// failure is logged and the firing simply waits, as it would without
// cancel_running.

// capacityCancel is concurrency.cancel_running as one fire applies it: the
// policy's mode, and the canceller the worker wired (nil: none).
type capacityCancel struct {
	mode reliantv1.WorkflowConcurrencyCancelRunning
	runs RunCanceller
}

func newCapacityCancel(policy *reliantv1.WorkflowConcurrency, runs RunCanceller) capacityCancel {
	return capacityCancel{mode: policy.GetCancelRunning(), runs: runs}
}

func (c capacityCancel) enabled() bool {
	switch c.mode {
	case reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_ON_NEW_KEY_EVENT,
		reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_ON_NEW_HEAD:
		return true
	}
	return false
}

func (c capacityCancel) onNewHead() bool {
	return c.mode == reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_ON_NEW_HEAD
}

// supersede cancels the running runs of enrolled's key that enrolled before
// it, as the mode allows. enrolled is the firing as stored: only a waiting
// firing whose key rendered cancels anything.
func (c capacityCancel) supersede(ctx context.Context, repo CapacityStore, descriptions capacityDescriptions, scope store.TriggerCapacityScope, enrolled *core.TriggerEvent) {
	if !c.enabled() || enrolled.Outcome != core.TriggerEventPending || enrolled.CapacityRunningKey == nil || enrolled.CapacityOrder == nil {
		return
	}
	if c.runs == nil {
		slog.WarnContext(ctx, "workflow capacity: concurrency.cancel_running is set but this worker cannot cancel runs; the firing waits for the running run to finish",
			"event_id", enrolled.ID)
		return
	}
	head := ""
	if c.onNewHead() {
		if head = pullRequestHead(enrolled.Payload); head == "" {
			return // no head to compare: a comment, a schedule
		}
	}
	running, err := repo.ListTriggerCapacityKeyRuns(ctx, scope, *enrolled.CapacityRunningKey, *enrolled.CapacityOrder)
	if err != nil {
		slog.WarnContext(ctx, "workflow capacity: cannot read the running runs a firing supersedes; it waits for them", "event_id", enrolled.ID, "error", err)
		return
	}
	for _, run := range running {
		// A run fired for no head, or for this one, is not stale.
		if c.onNewHead() && (run.HeadSHA == "" || run.HeadSHA == head) {
			continue
		}
		c.cancel(ctx, repo, descriptions, run, enrolled.ID)
	}
}

// cancel stops one superseded run, if Temporal shows its slot's chain running.
func (c capacityCancel) cancel(ctx context.Context, repo CapacityStore, descriptions capacityDescriptions, run store.TriggerCapacityKeyRun, by string) {
	if run.ChatID == "" || run.WorkflowID == "" {
		return // the start has not prepared its identity yet, or the chat is gone
	}
	runID, matched, running := capacityRunState(ctx, descriptions, run)
	if !matched || !running {
		return
	}
	// The exact execution this description showed, and no other: refused if
	// the chat's run is no longer it (a person's later turn).
	if err := c.runs.TerminateExecution(ctx, run.ChatID, run.WorkflowID, runID); err != nil {
		slog.WarnContext(ctx, "workflow capacity: cannot cancel a run a newer firing of its key supersedes; the next fire of that firing decides again",
			"event_id", run.EventID, "chat_id", run.ChatID, "run_id", runID, "superseded_by", by, "error", err)
		return
	}
	slog.InfoContext(ctx, "workflow capacity: cancelled a run a newer firing of its key supersedes (concurrency.cancel_running)",
		"event_id", run.EventID, "chat_id", run.ChatID, "run_id", runID, "superseded_by", by)
	// Recorded only now that the cancel succeeded. A run that completed or
	// failed on its own in the meantime keeps its own outcome (the mark is
	// refused); its own cancelled status does not refuse it.
	if _, err := repo.MarkTriggerCapacityRunSuperseded(ctx, run.EventID, by); err != nil {
		slog.WarnContext(ctx, "workflow capacity: cannot record which firing superseded a cancelled run",
			"event_id", run.EventID, "superseded_by", by, "error", err)
	}
	// A run that closed without writing its status (hard-terminated: it ran
	// no code) is settled here; one still running its finally: writes its own.
	if _, matched, running := capacityRunState(ctx, descriptions, run); !matched || running {
		return
	}
	if err := repo.SettleSupersededTriggerCapacityRun(ctx, run.EventID); err != nil {
		slog.WarnContext(ctx, "workflow capacity: cannot settle a superseded run cancelled", "event_id", run.EventID, "error", err)
	}
}

// capacityRunState reads the slot's chain from Temporal. matched is false when
// Temporal does not show that chain under the workflow id (missing, an outage,
// or a later chain: a person's own turn on the chat); running is its status
// then, and runID the exact execution the description showed. A chain not yet bound is matched by the trigger_event_id memo its
// launch set, as reconcileCapacityExecution does.
func capacityRunState(ctx context.Context, descriptions capacityDescriptions, run store.TriggerCapacityKeyRun) (runID string, matched, running bool) {
	if descriptions == nil {
		return "", false, false
	}
	desc, err := descriptions.DescribeWorkflowExecution(ctx, run.WorkflowID, "")
	if err != nil {
		return "", false, false
	}
	info := desc.GetWorkflowExecutionInfo()
	if info == nil || info.GetExecution().GetWorkflowId() != run.WorkflowID || info.GetExecution().GetRunId() == "" || info.GetFirstRunId() == "" {
		return "", false, false
	}
	if run.FirstRunID != "" {
		matched = info.GetFirstRunId() == run.FirstRunID
	} else {
		var memoEvent string
		payload := info.GetMemo().GetFields()["trigger_event_id"]
		matched = payload != nil && converter.GetDefaultDataConverter().FromPayload(payload, &memoEvent) == nil && memoEvent == run.EventID
	}
	return info.GetExecution().GetRunId(), matched, info.GetStatus() == enums.WORKFLOW_EXECUTION_STATUS_RUNNING
}

// pullRequestHead is the GitHub pull request head a firing carries
// (store.TriggerPullRequestHeadPath), or "" for none.
func pullRequestHead(payload map[string]any) string {
	var node any = payload
	for _, key := range store.TriggerPullRequestHeadPath {
		object, ok := node.(map[string]any)
		if !ok {
			return ""
		}
		node = object[key]
	}
	sha, _ := node.(string)
	return sha
}
