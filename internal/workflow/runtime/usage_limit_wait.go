// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/chatmarkers"
)

// usageLimitWaitChangeID gates holding a run until a provider's usage limit
// resets (workflow.GetVersion). It is consulted only where the old and new
// shapes first differ: a step that exhausted its retries on a
// chatmarkers.KindProviderUsageLimit error. A history recorded before it took
// the pause path there (a WorkflowError, a "paused" status, a park on the pause
// coordinator), and keeps replaying that way.
const usageLimitWaitChangeID = "usage-limit-wait"

// UsageLimitRetrySignalName is the signal behind "Retry now": it wakes every
// thread parked on a usage limit, so each retries its step at once instead of at
// the reset. A signal sent to a run that is not waiting is dropped on the next
// wait (see usageLimitWaiter.wait).
const UsageLimitRetrySignalName = "signal.usage_limit.retry"

const (
	// usageLimitMargin is added to a provider's stated reset before retrying:
	// the reset is the provider's clock, and a retry at the exact second
	// routinely lands a hair early and spends a wait on a second refusal.
	usageLimitMargin = 30 * time.Second
	// usageLimitJitterSpan spreads the retries of runs that share a window, so
	// every parked run does not hit the provider in the same second.
	usageLimitJitterSpan = 30 * time.Second
	// usageLimitQuietReset is how long a step goes without waiting before its
	// probe ladder starts over, so a long run's unrelated limits hours apart
	// do not inherit each other's rung.
	usageLimitQuietReset = 3 * time.Hour
)

// usageLimitProbeLadder is how long a run waits between attempts when the
// provider did not say when the limit resets. The last rung repeats with no cap:
// a wait is bounded by the provider, never by us.
var usageLimitProbeLadder = []time.Duration{
	5 * time.Minute,
	15 * time.Minute,
	30 * time.Minute,
	time.Hour,
}

// usageLimit is what a failed step tells us about the limit it hit.
type usageLimit struct {
	// Provider names who imposed it ("Anthropic"); empty when the message does
	// not say.
	Provider string
	// ResetAt is when the provider says the window resets; zero when it named
	// no time.
	ResetAt time.Time
}

// usageLimitState is what the run remembers about this step's earlier waits.
type usageLimitState struct {
	// Probes counts the consecutive waits on this step whose reset was unknown.
	Probes int
}

// usageLimitAction is what to do about a usage limit.
type usageLimitAction int

const (
	// usageLimitWaitUntil parks the step until Until, then retries it.
	usageLimitWaitUntil usageLimitAction = iota
)

// usageLimitDecision is the answer to "what now?" for one usage limit.
type usageLimitDecision struct {
	Action usageLimitAction
	// Until is when to retry (usageLimitWaitUntil).
	Until time.Time
	// Probing is true when the provider named no reset, so Until is only the
	// next rung of the probe ladder and not a promise.
	Probing bool
}

// decideOnUsageLimit is the ONE place that chooses what happens when a step
// hits a provider's usage limit. Today it always answers wait-until-T.
//
// Provider load balancing (phase 3) extends it here and nowhere else: before
// waiting, try another account of the same provider and then — only when the
// chat allows cross-provider fallback — another provider, answering with a new
// action (switch to account/model X, retry now) whose handling lives in
// usageLimitWaiter.wait beside the wait. Callers never choose a wait
// themselves, so a new action reaches all of them.
//
// It must stay a pure function of its arguments: it runs in workflow code, so
// the time comes in as `now` (workflow.Now) and the jitter as `jitter`, derived
// deterministically by the caller.
func decideOnUsageLimit(limit usageLimit, state usageLimitState, now time.Time, jitter time.Duration) usageLimitDecision {
	if limit.ResetAt.After(now) {
		return usageLimitDecision{
			Action: usageLimitWaitUntil,
			Until:  limit.ResetAt.Add(usageLimitMargin + jitter),
		}
	}
	// No reset, or one already in the past with the limit still enforced: the
	// provider's clock and ours disagree, or it said nothing. Probe.
	rung := state.Probes
	if rung >= len(usageLimitProbeLadder) {
		rung = len(usageLimitProbeLadder) - 1
	}
	return usageLimitDecision{
		Action:  usageLimitWaitUntil,
		Until:   now.Add(usageLimitProbeLadder[rung] + jitter),
		Probing: true,
	}
}

var usageLimitProviderRE = regexp.MustCompile(`usage limit reached \(([^,)]+)`)

// parseUsageLimit reads a usage-limit error, or reports that err is not one.
// Matched on the marker, which survives every wrap the error has been through.
func parseUsageLimit(err error) (usageLimit, bool) {
	if err == nil {
		return usageLimit{}, false
	}
	msg := err.Error()
	kind, payload, ok := chatmarkers.Extract(msg)
	if !ok || kind != chatmarkers.KindProviderUsageLimit {
		return usageLimit{}, false
	}
	var limit usageLimit
	if t, perr := time.Parse(time.RFC3339, strings.TrimSpace(payload)); perr == nil {
		limit.ResetAt = t.UTC()
	}
	if m := usageLimitProviderRE.FindStringSubmatch(msg); m != nil {
		limit.Provider = strings.TrimSpace(m[1])
	}
	return limit, true
}

// usageLimitJitter derives a stable jitter from who is waiting, so a replay
// computes the same wait without a side effect.
func usageLimitJitter(thread, stepID string, probes int) time.Duration {
	h := fnv.New32a()
	_, _ = h.Write([]byte(thread + "|" + stepID + "|" + strconv.Itoa(probes)))
	return time.Duration(h.Sum32()%uint32(usageLimitJitterSpan/time.Second+1)) * time.Second
}

// usageLimitWaitRequest is one step's usage-limit failure, handed to the waiter.
type usageLimitWaitRequest struct {
	Thread string
	StepID string
	Err    error
	// Cancelled reports that the user cancelled this thread (sub-agents).
	Cancelled func() bool
}

// usageLimitOutcome is how a wait ended.
type usageLimitOutcome struct {
	// Handled is true when the failure was a usage limit this run waited out
	// (or was stopped while waiting). False sends the caller down its ordinary
	// retry-exhaustion path.
	Handled bool
	// Err, with Handled, ends the caller instead of retrying the step: the
	// thread was cancelled or the run was.
	Err error
	// MessageRead is true when a message to the thread ended the wait. The
	// retried step reads it, so the caller re-bases its loop-exit gate.
	MessageRead bool
}

// usageLimitProbe is one thread-step's probe ladder position.
type usageLimitProbe struct {
	probes int
	last   time.Time
}

// usageLimitWaiter holds a run's steps for usage-limit resets. One per run,
// shared by every thread: the root thread and each sub-agent wait in place on
// their own durable timers, and "Retry now" wakes them all.
type usageLimitWaiter struct {
	pauser       *pauseCoordinator
	tracker      *ChildWorkflowTracker
	chatID       string
	workflowID   string
	workflowName string

	listening  bool
	retryEpoch int
	probes     map[string]*usageLimitProbe
}

func newUsageLimitWaiter(pauser *pauseCoordinator, tracker *ChildWorkflowTracker, chatID, workflowID, workflowName string) *usageLimitWaiter {
	return &usageLimitWaiter{
		pauser:       pauser,
		tracker:      tracker,
		chatID:       chatID,
		workflowID:   workflowID,
		workflowName: workflowName,
		probes:       map[string]*usageLimitProbe{},
	}
}

// startListening begins counting "Retry now" signals. It starts on the first
// wait, behind the version gate, so a run that never meets a usage limit has
// exactly the coroutines it had before. Signals that arrived earlier are
// dropped: they were aimed at a wait that was not there.
func (w *usageLimitWaiter) startListening() {
	if w.listening {
		return
	}
	w.listening = true
	ch := workflow.GetSignalChannel(w.pauser.root, UsageLimitRetrySignalName)
	for ch.ReceiveAsync(nil) {
	}
	workflow.Go(w.pauser.root, func(gCtx workflow.Context) {
		for {
			if !ch.Receive(gCtx, nil) {
				return
			}
			w.retryEpoch++
		}
	})
}

// wait holds the step until its usage limit resets, writing the durable marker
// first, and reports whether the caller should retry the step.
//
// It parks on a workflow timer (workflow.AwaitWithTimeout) that ends early when
// any of these happens: "Retry now"; a message to the waiting thread (a send
// that also changed the model or provider is the way out of a limit, and one
// that did not costs a single refused request, after which the run parks again
// on the new reset); a user pause; the thread being cancelled; the run being
// cancelled. The step is NOT re-dispatched here: the caller does that, and if
// the limit is still there the step fails the same way and parks again.
func (w *usageLimitWaiter) wait(ctx workflow.Context, req usageLimitWaitRequest, pc *PauseController) usageLimitOutcome {
	limit, ok := parseUsageLimit(req.Err)
	if !ok {
		return usageLimitOutcome{}
	}
	if workflow.GetVersion(ctx, usageLimitWaitChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return usageLimitOutcome{}
	}
	logger := workflow.GetLogger(ctx)
	w.startListening()

	key := req.Thread + "|" + req.StepID
	now := workflow.Now(ctx)
	probe := w.probes[key]
	if probe == nil || now.Sub(probe.last) > usageLimitQuietReset {
		probe = &usageLimitProbe{}
		w.probes[key] = probe
	}
	decision := decideOnUsageLimit(limit, usageLimitState{Probes: probe.probes}, now,
		usageLimitJitter(req.Thread, req.StepID, probe.probes))
	if decision.Probing {
		probe.probes++
	} else {
		probe.probes = 0
	}

	// The marker goes down BEFORE the park, or a supervisor reading the chat
	// cannot tell this run from a wedged one. See db.ProviderBackoff.
	w.recordMarker(ctx, req.Thread, limit.Provider, decision)

	startEpoch := w.retryEpoch
	startWakes := w.tracker.threadWakeCount(req.Thread)
	startPauses := w.pauser.pauseSignals
	cancelled := func() bool { return req.Cancelled != nil && req.Cancelled() }
	if !workflow.IsReplaying(ctx) {
		logger.Info("[UsageLimit] Provider usage limit reached; holding the step until it resets",
			"workflowID", w.workflowID,
			"thread", req.Thread,
			"stepID", req.StepID,
			"provider", limit.Provider,
			"until", decision.Until,
			"probing", decision.Probing,
		)
	}

	woken, err := workflow.AwaitWithTimeout(ctx, decision.Until.Sub(now), func() bool {
		return w.retryEpoch > startEpoch ||
			w.tracker.threadWakeCount(req.Thread) > startWakes ||
			w.pauser.pauseSignals > startPauses ||
			cancelled()
	})
	probe.last = workflow.Now(ctx)
	w.clearMarker(ctx, req.Thread)

	if err != nil || ctx.Err() != nil {
		// The run was cancelled while it waited. The marker is already
		// released; end the caller the way any cancellation does.
		cause := err
		if cause == nil {
			cause = ctx.Err()
		}
		return usageLimitOutcome{Handled: true, Err: cause}
	}
	if cancelled() {
		return usageLimitOutcome{Handled: true, Err: ErrThreadCancelled}
	}
	if woken && !workflow.IsReplaying(ctx) {
		logger.Info("[UsageLimit] Woken before the reset; retrying the step",
			"workflowID", w.workflowID, "thread", req.Thread, "stepID", req.StepID)
	}
	// A message that cut the wait short is read by the step retried below (its
	// call_llm drains the mailbox), so it is not owed another turn.
	messageRead := w.tracker.threadWakeCount(req.Thread) > startWakes
	if messageRead {
		w.tracker.recordTurnStart(req.Thread)
	}

	// A user pause that cut the wait short holds the retry until a resume, as
	// any other step boundary would.
	pc.DoCheckPause(ctx)
	// Yield so signals delivered with the wake (a send's model change) are
	// applied before the caller re-reads its inputs.
	_ = workflow.Sleep(ctx, 0)
	if ctx.Err() != nil {
		return usageLimitOutcome{Handled: true, Err: ctx.Err()}
	}
	return usageLimitOutcome{Handled: true, MessageRead: messageRead}
}

// markerContext returns a context the marker write survives the run's
// cancellation on: the clear that follows a cancel is the one that matters.
func markerContext(ctx workflow.Context) workflow.Context {
	base := ctx
	if ctx.Err() != nil {
		base, _ = workflow.NewDisconnectedContext(ctx)
	}
	return workflow.WithActivityOptions(base, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	})
}

// recordMarker writes the wait's durable marker. A failed write is logged, not
// fatal: the wait is worth more than the marker, and the activity retries first.
func (w *usageLimitWaiter) recordMarker(ctx workflow.Context, thread, provider string, d usageLimitDecision) {
	err := workflow.ExecuteActivity(markerContext(ctx), "UsageLimitWait", map[string]interface{}{
		"chat_id":   w.chatID,
		"thread":    thread,
		"provider":  provider,
		"resume_at": d.Until.UTC().Format(time.RFC3339),
		"probing":   d.Probing,
	}).Get(ctx, nil)
	if err != nil && !errors.Is(err, workflow.ErrCanceled) {
		workflow.GetLogger(ctx).Warn("[UsageLimit] Could not write the wait marker; waiting anyway",
			"workflowID", w.workflowID, "thread", thread, "error", err)
	}
}

// clearMarker releases the wait's marker once the wait is over.
func (w *usageLimitWaiter) clearMarker(ctx workflow.Context, thread string) {
	err := workflow.ExecuteActivity(markerContext(ctx), "UsageLimitWait", map[string]interface{}{
		"chat_id": w.chatID,
		"thread":  thread,
		"clear":   true,
	}).Get(ctx, nil)
	if err != nil && !errors.Is(err, workflow.ErrCanceled) {
		workflow.GetLogger(ctx).Warn("[UsageLimit] Could not clear the wait marker",
			"workflowID", w.workflowID, "thread", thread, "error", err)
	}
}

// waitOutUsageLimit is the executors' one call at a retry-exhaustion site: if
// the exhausted step failed on a provider usage limit, hold it until the limit
// resets and report that the step should be dispatched again (or that the thread
// must end). Not Handled means "not a usage limit, or not this run's shape":
// carry on with the ordinary exhaustion path.
//
// Nil-safe, like every PauseController method.
func (pc *PauseController) waitOutUsageLimit(ctx workflow.Context, thread, stepID string, err error) usageLimitOutcome {
	if pc == nil || pc.UsageLimit == nil {
		return usageLimitOutcome{}
	}
	return pc.UsageLimit.wait(ctx, usageLimitWaitRequest{
		Thread:    thread,
		StepID:    stepID,
		Err:       err,
		Cancelled: pc.Cancelled,
	}, pc)
}
