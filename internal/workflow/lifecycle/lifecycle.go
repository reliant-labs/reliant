// Copyright (c) 2025 Reliant Labs

// Package lifecycle owns the rules about whether a workflow run may do work.
//
// It exists so that callers do not have to know what "stopped" means, which
// work is exempt from the rule, or how PAUSED differs from the other stop
// reasons. Those are lifecycle concerns, and spreading them across the
// activity dispatcher, the executors and the handlers is what this package is
// here to prevent: every one of those call sites then has to be kept in sync
// by hand, and each is a place the rule can be forgotten.
//
// The interface is deliberately one question — MayExecute — because that is
// the only thing a caller actually needs to decide. What counts as stopped and
// what is exempt live here, behind it.
//
// forge:exclude-contract
//
// Temporal workflow/activity code. The exported functions are registered with
// the Temporal SDK by name and invoked by the runtime, not through a Go
// interface a caller could substitute. Determinism constraints, not an
// interface, define this boundary.
package lifecycle

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// statusLookupTimeout bounds the guard's read. Short on purpose: it is a
// single primary-key lookup and it runs before every activity, so it must
// never be what exhausts a caller's remaining budget.
const statusLookupTimeout = 3 * time.Second

// WorkflowReader is the single repository operation this package needs. It is
// declared here, as a one-method interface, so callers can supply anything
// that can answer it — and so tests need no database.
type WorkflowReader interface {
	GetWorkflow(ctx context.Context, id string) (*core.Workflow, error)
}

// Decision is the answer to MayExecute: whether work may proceed, and if not,
// why. Reason is empty when Allowed is true.
//
// Retryable says how a caller should REFUSE. A refusal based on the durable
// status row can be STALE: resume signals Temporal and writes the row
// asynchronously, so there is a window where the run is genuinely awake and the
// row still says stopped. Observed at 3 seconds — PauseService logged "resumed
// successfully" at 21:24:31.3 while activities were still being refused at
// 21:24:34.3.
//
// Refusing non-retryably inside that window is unrecoverable: it kills a turn
// that was about to be legitimate, and the run settles as failed rather than
// resumed. A retryable refusal self-corrects — the next attempt reads the
// updated row and proceeds — while still stopping a genuinely stopped run,
// because for that run every attempt reads the same stopped status and the
// activity exhausts its budget without doing any work.
type Decision struct {
	Allowed bool
	Reason  string
	// Retryable is true when the refusal may be based on a stale read and the
	// caller should fail in a way Temporal will retry. False only for stop
	// reasons that cannot become executable again on their own.
	Retryable bool
}

// allow is the permissive answer, used both for "the run is fine" and for
// every inconclusive case (see MayExecute's doc on why those are the same).
var allow = Decision{Allowed: true}

// MayExecute reports whether work may run for this workflow right now.
//
// The rule is general rather than specific to any one activity: work is done
// on behalf of a run, and a run that has stopped has no work to do. Asking it
// at a single boundary means each caller does not have to remember to check,
// and new work gets the rule for free.
//
// STOPPED is not executable for ANY reason, PAUSED included. This is the
// distinction core.WorkflowStatus.Executable draws against Live(): Live asks
// "will this run produce another turn" (true for PAUSED, which resumes and
// drains what was queued), while this asks "should work run this instant"
// (false for PAUSED, because stopping work is the entire point of pausing).
// Conflating them let a paused run keep issuing LLM calls — chat 128cf4f5,
// where a self-pause resumed at 17:41:51, re-ran the same failing step, and
// failed identically at 17:42:08 with the row at STOPPED/PAUSED throughout.
//
// BEST-EFFORT BY DESIGN. An unreadable status, a nil reader, or an empty id
// all return allowed. This is a guard, not a gate: blocking real work because
// a bookkeeping lookup failed would be a worse failure than the one being
// prevented. Callers must not treat a permissive answer as proof the run is
// healthy.
//
// Exemptions are NOT handled here — see MayExecuteWork.
func MayExecute(ctx context.Context, reader WorkflowReader, workflowID string) Decision {
	if reader == nil || workflowID == "" {
		return allow
	}

	// An independent budget: the caller's context may already be near its
	// deadline, and detaching from cancellation would be wrong here (a
	// cancelled caller should not wait on this), so only the timeout is set.
	lookupCtx, cancel := context.WithTimeout(ctx, statusLookupTimeout)
	defer cancel()

	workflow, err := reader.GetWorkflow(lookupCtx, workflowID)
	if err != nil || workflow == nil {
		return allow
	}
	if workflow.Status.Executable() {
		return allow
	}
	// Only a run that can become executable again warrants a retryable
	// refusal — that is exactly the set whose status row may be a stale read
	// mid-resume. A COMPLETED or CANCELLED run will never flip back, so
	// retrying against it only burns the activity's budget to reach the same
	// answer; refuse those permanently and let the loop unwind.
	return Decision{
		Allowed:   false,
		Reason:    workflow.Status.StopReason.String(),
		Retryable: workflow.Status.Resumable(),
	}
}

// WorkKind says what a piece of work IS, so this package can decide whether a
// stopped run may still do it. Callers declare the kind; they do not decide the
// policy.
//
// A bool was not enough, and the gap was not theoretical. When the exemption
// was "is this registered as a lifecycle activity", EmitToolCallStatus — which
// only reports a tool call's status — was blocked on paused runs, because it
// happened to be registered as ordinary work. Naming the kind makes that a
// visible, reviewable declaration at the registration site instead of a
// property of which helper someone reached for.
type WorkKind int

const (
	// AgentWork advances the conversation: calling the model, executing
	// tools, saving the messages they produce. This is what pausing is FOR,
	// and the only kind a stopped run refuses.
	//
	// Zero value on purpose. A caller that forgets to say what kind of work it
	// is dispatching gets the guarded behavior, so the failure mode of
	// forgetting is "too safe" rather than "silently exempt".
	AgentWork WorkKind = iota

	// LifecycleWork changes or repairs the run's own state: writing status,
	// checkpoints, cleanup, the error that explains why it stopped.
	//
	// Always allowed, and it MUST be: this is how a stopped run reports,
	// repairs and un-stops itself. The activity that writes "started" on
	// resume is itself work issued by a run that is still marked stopped, so
	// blocking it would make the guard self-sealing — a paused run could never
	// write the status that un-pauses it.
	LifecycleWork

	// ObservabilityWork tells the outside world what already happened: tool
	// call status, stream markers, node execution events. It changes no
	// conversation state and starts nothing new.
	//
	// Always allowed. Suppressing it does not stop any work — the work is
	// already done or already stopped — it only blinds the UI at the exact
	// moment a user is watching to understand why their chat stopped. That is
	// the opposite of what a pause should do.
	ObservabilityWork
)

func (k WorkKind) String() string {
	switch k {
	case LifecycleWork:
		return "lifecycle"
	case ObservabilityWork:
		return "observability"
	default:
		return "agent"
	}
}

// guarded reports whether this kind of work is subject to the stopped-run rule.
// Exactly one kind is, which is the point: the rule exists to stop the agent
// loop, not to silence the system.
func (k WorkKind) guarded() bool { return k == AgentWork }

// MayExecuteWork is MayExecute plus the kind-based exemption, and is what
// generic dispatchers should call.
//
// Keeping the exemption here rather than at the call site is the point: a
// dispatcher says what KIND of work it is dispatching and lets this package
// decide, instead of encoding which kinds are special.
func MayExecuteWork(ctx context.Context, reader WorkflowReader, workflowID string, kind WorkKind) Decision {
	if !kind.guarded() {
		return allow
	}
	return MayExecute(ctx, reader, workflowID)
}
