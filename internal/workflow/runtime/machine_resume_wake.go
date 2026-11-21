// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// ── Waking the machine when a run resumes ───────────────────────────────
//
// A run that is held off its machine — a provider usage limit, a timer, a
// sub-agent it waits on, a pause, a provider retry backoff — sends that
// machine no traffic, so the idle policy may suspend it meanwhile. That is
// intended: a machine nobody uses for hours should not bill for hours. But
// tool-time traffic never wakes a machine (#406: only run start and an
// attended send do), so a run resuming into a tool call used to find its
// machine asleep and spin until a human woke it — six hours on 2026-10-20
// (control-plane docs/research/machine-suspend-2026-10-20.md).
//
// So the run wakes it at the resume point: before the first machine step that
// follows a quiet stretch, it runs MachineResumeWake — the same wake run
// preflight does — and waits for the machine to attach, so the step's tool
// calls just work. This is infrastructure. The model is not told and needs no
// tool for it; the user sees the chat's machine status ("Waking your
// machine…") while it happens.
//
// "Resume point" is defined by the one thing every hold has in common rather
// than by enumerating holds: workflow time passed without a machine step.
// Under machineResumeWakeAfter nothing can have idled the machine out, and
// the check is skipped; past it, the activity asks the registry, which costs
// one database read when the machine is up. That covers every hold that
// exists and any added later, in every thread of the run.
//
// One waker per run, shared by every thread (it rides on the PauseController
// like the daemon-offline breaker), so sub-agents resuming together after the
// same usage limit share one wake: the first schedules it, the rest wait on
// its future. Across runs the router's per-daemon gate and the control plane's
// idempotent resume keep it to one wake per machine.

// machineResumeWakeChangeID gates the wake for histories recorded before it:
// replaying one must not schedule a MachineResumeWake it never ran.
const machineResumeWakeChangeID = "wake-machine-on-run-resume"

// machineResumeWakeAfter is the quiet stretch after which a machine step
// first checks that the machine is awake. Short enough to catch any idle
// timeout a user can set; long enough that an agent's normal LLM-then-tools
// turn rarely pays even the one read.
const machineResumeWakeAfter = 2 * time.Minute

const (
	// machineResumeWakeSlice is how long one MachineResumeWake polls for the
	// machine after waking it. A resume usually attaches within a minute or
	// two; a cold node can take several.
	machineResumeWakeSlice = 60 * time.Second
	// machineResumeWakeCap bounds the whole wait. Past it the step runs
	// anyway and reports what the machine is doing, as it always did.
	machineResumeWakeCap = 20 * time.Minute
)

// machineResumeWaker tracks when the run last used each machine it routes to
// and wakes a machine before a machine step that follows a hold on it.
// Deterministic workflow state: rebuilt on replay from the recorded steps and
// timestamps.
//
// The run's own machine (the workflow-level one, else the chat's) is the root
// target. A step placed on a pool member (run_placement.go) — by its own
// `daemon: {pool:}` or an enclosing loop's or workflow node's — routes
// elsewhere, so its member is a target of its own, with its own quiet-stretch
// clock: waking the root machine would leave the member asleep, and wait on a
// machine the step never uses. Only placed selectors get their own target, so
// a history recorded before pools existed wakes exactly as it did.
type machineResumeWaker struct {
	// root is the run's own machine; rootID the daemon id its selector
	// names ("" when none), which a placement onto the same machine shares.
	root   *machineWakeTarget
	rootID string
	// placed is each pool member a step was placed on, by daemon id.
	placed map[string]*machineWakeTarget
}

// machineWakeTarget is one machine the run wakes: its preflight-shaped
// target, when the run last used it, and the wake in progress.
type machineWakeTarget struct {
	// input is the machine target, preflight-shaped: chat_id and the
	// daemon_selector.
	input map[string]interface{}
	// lastMachineStep is the workflow time of the last machine step's
	// completion on this machine (or the run's start, right after
	// preflight, for the root). Zero for a member the run has not used yet:
	// its first step checks it, since nothing proves it awake.
	lastMachineStep time.Time
	// inflight is the wake in progress, which every other thread that needs
	// the machine meanwhile waits on instead of starting its own.
	inflight workflow.Future
}

func newMachineResumeWaker(ctx workflow.Context, preflightInput map[string]interface{}) *machineResumeWaker {
	w := &machineResumeWaker{root: &machineWakeTarget{input: preflightInput, lastMachineStep: workflow.Now(ctx)}}
	if sel, ok := preflightInput["daemon_selector"].(map[string]interface{}); ok {
		w.rootID, _ = sel["id"].(string)
	}
	return w
}

// target is the machine a step routed by sel uses: its placed member, or the
// root for any selector that was not placed (or placed on the root machine).
func (w *machineResumeWaker) target(sel *DaemonSelectorValue) *machineWakeTarget {
	if sel == nil || !sel.Placed || sel.ID == "" || sel.ID == w.rootID {
		return w.root
	}
	if t, ok := w.placed[sel.ID]; ok {
		return t
	}
	if w.placed == nil {
		w.placed = map[string]*machineWakeTarget{}
	}
	t := &machineWakeTarget{input: map[string]interface{}{
		"chat_id":         w.root.input["chat_id"],
		"daemon_selector": map[string]interface{}{"id": sel.ID},
	}}
	w.placed[sel.ID] = t
	return t
}

// machineStepTools reports whether a step of this type uses the machine and,
// for a tool batch, the tools it calls. known is false when the step needs
// the machine whatever it does. Only the node TYPE decides whether the wake is
// considered — it is replayed — and the activity, which is not, decides from
// the tool names whether any of them touch the machine.
func machineStepTools(stepType string, evalResult *reliantv1.Node) (machine bool, toolNames []string, known bool) {
	switch stepType {
	case model.NodeTypeRun, model.NodeTypeGitPublish:
		return true, nil, false
	case model.NodeTypeExecuteTools:
		calls := evalResult.GetExecuteTools().GetResolvedToolCalls()
		if len(calls) == 0 {
			// Nothing resolved to judge by: assume the machine is needed
			// rather than skip a wake the batch turns out to need.
			return true, nil, false
		}
		// spawn and ask_user run in the workflow, never on the machine; a
		// batch of only those (a parent fanning out sub-agents) wakes nothing.
		regular := splitProtoToolCalls(calls).regularToolCalls
		if len(regular) == 0 {
			return false, nil, false
		}
		names := make([]string, 0, len(regular))
		for _, call := range regular {
			names = append(names, call.GetName())
		}
		return true, names, true
	default:
		return false, nil, false
	}
}

// beforeMachineStep wakes the machine a step routed by sel uses if the run
// has been off it long enough for it to have been suspended, and waits for
// it. callerCtx MUST be the calling coroutine's own context; actCtx is the
// run's cancellable activity context. Never fails the step: whatever the wake
// could not fix, the step reports.
func (w *machineResumeWaker) beforeMachineStep(callerCtx, actCtx workflow.Context, sel *DaemonSelectorValue, toolNames []string, toolsKnown bool) {
	if w == nil {
		return
	}
	t := w.target(sel)
	if t.inflight != nil {
		_ = t.inflight.Get(callerCtx, nil)
		return
	}
	if workflow.Now(callerCtx).Sub(t.lastMachineStep) < machineResumeWakeAfter {
		return
	}
	if workflow.GetVersion(callerCtx, machineResumeWakeChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return
	}
	future, settable := workflow.NewFuture(callerCtx)
	t.inflight = future
	defer func() {
		t.inflight = nil
		t.lastMachineStep = workflow.Now(callerCtx)
		settable.Set(nil, nil)
	}()
	wakeMachineForResume(callerCtx, actCtx, t.input, toolNames, toolsKnown)
}

// machineStepDone records that the run just used the machine sel routes to.
func (w *machineResumeWaker) machineStepDone(ctx workflow.Context, sel *DaemonSelectorValue) {
	if w == nil {
		return
	}
	w.target(sel).lastMachineStep = workflow.Now(ctx)
}

// wakeMachineForResume runs MachineResumeWake slice by slice until the machine
// is up, will not come, or the cap passes.
func wakeMachineForResume(callerCtx, actCtx workflow.Context, target map[string]interface{}, toolNames []string, toolsKnown bool) {
	ctx := workflow.WithActivityOptions(actCtx, workflow.ActivityOptions{
		StartToCloseTimeout: machineResumeWakeSlice + preflightSliceMargin,
		HeartbeatTimeout:    preflightHeartbeatTimeout,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	logger := workflow.GetLogger(callerCtx)
	start := workflow.Now(callerCtx)
	// failedSince carries the machine's run of Failed reports from one slice
	// to the next, as waitForMachine does for preflight.
	var failedSince interface{}
	for {
		final := workflow.Now(callerCtx).Sub(start)+machineResumeWakeSlice >= machineResumeWakeCap
		in := make(map[string]interface{}, len(target)+5)
		for k, v := range target {
			in[k] = v
		}
		in["wait_seconds"] = int(machineResumeWakeSlice / time.Second)
		in["final"] = final
		if toolsKnown {
			in["tools_known"] = true
			in["tool_names"] = toolNames
		}
		if failedSince != nil {
			in["failed_since"] = failedSince
		}
		var result map[string]interface{}
		if err := workflow.ExecuteActivity(ctx, "MachineResumeWake", in).Get(callerCtx, &result); err != nil {
			logger.Warn("[Workflow Runtime] Resume wake failed; running the step anyway", "error", err)
			return
		}
		if woke, _ := result["woke"].(bool); woke {
			logger.Info("[Workflow Runtime] Woke the run's machine on resume")
		}
		if waiting, _ := result["waiting"].(bool); !waiting || final || callerCtx.Err() != nil {
			return
		}
		failedSince = result["failed_since"]
	}
}
