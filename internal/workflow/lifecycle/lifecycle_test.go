// Copyright (c) 2025 Reliant Labs
package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// stubReader answers GetWorkflow from memory, so these tests need no database.
// That is the reason WorkflowReader is a one-method interface.
type stubReader struct {
	workflow *core.Workflow
	err      error
	calls    int
}

func (s *stubReader) GetWorkflow(_ context.Context, _ string) (*core.Workflow, error) {
	s.calls++
	return s.workflow, s.err
}

func workflowWith(status core.WorkflowStatus) *core.Workflow {
	return &core.Workflow{ID: "wf-1", Status: status}
}

// A PAUSED run must NOT execute, even though Live() calls it alive. This is
// the distinction the package exists to draw: a paused run resumes and drains
// queued work (Live), but must not run an activity this instant (Executable).
// Conflating them let a paused run keep issuing LLM calls.
func TestMayExecute_PausedIsNotExecutableEvenThoughItIsLive(t *testing.T) {
	paused := core.Paused()
	if !paused.Live() {
		t.Fatal("precondition: PAUSED is expected to be Live (it resumes and drains queued work)")
	}

	reader := &stubReader{workflow: workflowWith(paused)}
	decision := MayExecute(context.Background(), reader, "wf-1")

	if decision.Allowed {
		t.Fatal("a PAUSED run must not execute: stopping work is the entire point of pausing")
	}
	if decision.Reason == "" {
		t.Error("a refusal must say why, so the caller can report it")
	}
}

func TestMayExecute_StoppedForAnyReasonDoesNotExecute(t *testing.T) {
	for _, status := range []core.WorkflowStatus{
		core.Paused(), core.Failed(), core.Cancelled(), core.Completed(),
	} {
		reader := &stubReader{workflow: workflowWith(status)}
		if MayExecute(context.Background(), reader, "wf-1").Allowed {
			t.Errorf("stopped run (%s) must not execute", status.Label())
		}
	}
}

func TestMayExecute_RunningAndPendingExecute(t *testing.T) {
	for _, status := range []core.WorkflowStatus{core.Active(), core.Pending()} {
		reader := &stubReader{workflow: workflowWith(status)}
		if !MayExecute(context.Background(), reader, "wf-1").Allowed {
			t.Errorf("%s run must execute", status.Label())
		}
	}
}

// Best-effort: the guard must never block real work because a bookkeeping
// lookup failed. Every inconclusive case is permissive.
func TestMayExecute_InconclusiveLookupsAllow(t *testing.T) {
	cases := map[string]struct {
		reader     WorkflowReader
		workflowID string
	}{
		"read error": {&stubReader{err: errors.New("db down")}, "wf-1"},
		"nil row":    {&stubReader{workflow: nil}, "wf-1"},
		"nil reader": {nil, "wf-1"},
		"empty id":   {&stubReader{workflow: workflowWith(core.Paused())}, ""},
	}
	for name, tc := range cases {
		if !MayExecute(context.Background(), tc.reader, tc.workflowID).Allowed {
			t.Errorf("%s: must allow — a guard must not become a gate", name)
		}
	}
}

// Lifecycle work is exempt, and must be: it is how a stopped run reports,
// repairs and un-stops itself. Without this the guard is self-sealing — a
// paused run could never write the status that un-pauses it.
func TestMayExecuteWork_LifecycleWorkIsExemptAndSkipsTheLookup(t *testing.T) {
	reader := &stubReader{workflow: workflowWith(core.Paused())}

	if !MayExecuteWork(context.Background(), reader, "wf-1", LifecycleWork).Allowed {
		t.Fatal("lifecycle work must run for a stopped workflow, or the guard seals the run shut")
	}
	if reader.calls != 0 {
		t.Errorf("exempt work should not pay for a status lookup, got %d calls", reader.calls)
	}
}

func TestMayExecuteWork_NonLifecycleWorkIsGuarded(t *testing.T) {
	reader := &stubReader{workflow: workflowWith(core.Paused())}
	if MayExecuteWork(context.Background(), reader, "wf-1", AgentWork).Allowed {
		t.Fatal("ordinary work must not run for a paused workflow")
	}
}

// ---------------------------------------------------------------------------
// WorkKind: which work the stopped-run rule applies to
// ---------------------------------------------------------------------------

// The rule exists to stop the AGENT LOOP, not to silence the system. Anything
// that merely reports what already happened, or that repairs the run's own
// state, must still run while stopped.
//
// This is a REGRESSION test with a specific history. When the exemption was a
// single "is this a lifecycle activity" bool, EmitToolCallStatus — which only
// reports a tool call's status — was registered as ordinary work and therefore
// blocked on paused runs (observed 4 times). That blinded the UI at exactly the
// moment a user was trying to understand why their chat had stopped, and it
// contributed to resume appearing not to work.
func TestMayExecuteWork_OnlyAgentWorkIsGuarded(t *testing.T) {
	tests := []struct {
		kind        WorkKind
		wantAllowed bool
		wantLookup  bool
	}{
		// Agent work is the point of the rule: guarded, so it must look up.
		{AgentWork, false, true},
		// Lifecycle work un-stops the run; blocking it would be self-sealing.
		{LifecycleWork, true, false},
		// Observability work reports the past; blocking it stops nothing and
		// only hides the outcome.
		{ObservabilityWork, true, false},
	}

	for _, tc := range tests {
		t.Run(tc.kind.String(), func(t *testing.T) {
			reader := &stubReader{workflow: workflowWith(core.Paused())}
			got := MayExecuteWork(context.Background(), reader, "wf-1", tc.kind)

			if got.Allowed != tc.wantAllowed {
				t.Fatalf("%s on a paused run: Allowed=%v, want %v",
					tc.kind, got.Allowed, tc.wantAllowed)
			}
			// An exempt kind must not even pay for the lookup.
			if didLookup := reader.calls > 0; didLookup != tc.wantLookup {
				t.Fatalf("%s: performed lookup=%v, want %v", tc.kind, didLookup, tc.wantLookup)
			}
		})
	}
}

// The zero value must be the GUARDED kind, so that forgetting to declare a
// kind fails safe rather than silently exempting new work from the rule.
func TestWorkKind_ZeroValueIsGuardedAgentWork(t *testing.T) {
	var zero WorkKind
	if zero != AgentWork {
		t.Fatalf("zero WorkKind must be AgentWork, got %v", zero)
	}
	if !zero.guarded() {
		t.Fatal("the zero value must be guarded — forgetting to declare a kind must fail safe")
	}
}

// ---------------------------------------------------------------------------
// Retryability: refusing a STALE read must be recoverable
// ---------------------------------------------------------------------------

// Resume signals Temporal and writes the status row asynchronously, so there is
// a window where the run is genuinely awake and the row still says stopped.
// Measured at ~3s: PauseService logged "resumed successfully" at 21:24:31.3
// while activities were still refused at 21:24:34.3.
//
// Refusing NON-retryably inside that window is unrecoverable — it kills a turn
// that was about to be legitimate, and the run settles failed instead of
// resumed. A retryable refusal self-corrects on the next attempt, while a
// genuinely stopped run still does no work (every attempt reads the same
// stopped status and the budget simply expires).
func TestMayExecute_RefusalIsRetryableOnlyWhenTheRunCanResume(t *testing.T) {
	tests := []struct {
		name          string
		status        core.WorkflowStatus
		wantRetryable bool
	}{
		// PAUSED resumes — its stopped row is exactly the one that goes stale.
		{"paused", core.Paused(), true},
		// FAILED is resumable at position, so the same staleness applies.
		{"failed", core.Failed(), true},
		// COMPLETED and CANCELLED never become executable again. Retrying only
		// burns the activity's budget to reach the same answer.
		{"completed", core.Completed(), false},
		{"cancelled", core.Cancelled(), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := &stubReader{workflow: workflowWith(tc.status)}
			got := MayExecute(context.Background(), reader, "wf-1")

			if got.Allowed {
				t.Fatalf("%s must not be executable", tc.name)
			}
			if got.Retryable != tc.wantRetryable {
				t.Fatalf("%s: Retryable=%v, want %v — a refusal that may be based on a "+
					"stale mid-resume read must be retryable so it self-corrects",
					tc.name, got.Retryable, tc.wantRetryable)
			}
		})
	}
}

// A permissive answer carries no reason and no retry advice — there is nothing
// to recover from.
func TestMayExecute_AllowedDecisionsCarryNoRefusalDetail(t *testing.T) {
	reader := &stubReader{workflow: workflowWith(core.Active())}
	got := MayExecute(context.Background(), reader, "wf-1")

	if !got.Allowed {
		t.Fatal("an active run must be executable")
	}
	if got.Reason != "" || got.Retryable {
		t.Fatalf("an allowed decision must carry no refusal detail, got %+v", got)
	}
}

// Inconclusive reads stay permissive AND must not be marked retryable — the
// caller is proceeding, so there is nothing to retry.
func TestMayExecute_InconclusiveReadsAllowWithoutRetryAdvice(t *testing.T) {
	for _, reader := range []*stubReader{
		{err: errors.New("db down")},
		{workflow: nil},
	} {
		got := MayExecute(context.Background(), reader, "wf-1")
		if !got.Allowed || got.Retryable {
			t.Fatalf("an inconclusive read must allow without retry advice, got %+v", got)
		}
	}
}
