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
	"github.com/reliant-labs/reliant/internal/workflow/capacitywake"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// usageLimitWaitChangeID gates holding a run until a provider's usage limit
// resets (workflow.GetVersion). It is consulted only where the old and new
// shapes first differ: a step that exhausted its retries on a
// chatmarkers.KindProviderUsageLimit error. A history recorded before it took
// the pause path there (a WorkflowError, a "paused" status, a park on the pause
// coordinator), and keeps replaying that way.
const usageLimitWaitChangeID = "usage-limit-wait"

// usageLimitFailoverChangeID gates acting on the facts an activity attaches to a
// usage-limit failure (types.UsageLimitFacts): retrying at once on another
// account of the same provider, and continuing on another provider for a
// lease. It is consulted where the old and new shapes first differ: a usage
// limit whose decision is not "wait". A history recorded before it has no
// marker there, so it ignores the facts and waits exactly as it did, whatever
// the activity now reports.
const usageLimitFailoverChangeID = "usage-limit-failover"

// usageLimitStayChangeID gates two refinements of acting on a usage limit,
// consulted in usageLimitWaiter.wait right after usage-limit-failover: a probe
// bench is a recheck time and not a reset (types.UsageLimitFacts.
// ProviderResetIsProbe), and a stay on a fallback model survives a refused
// attempt to return, instead of lapsing and being re-announced at every rung.
// A history recorded before it has no marker there and keeps the old
// behaviour.
const usageLimitStayChangeID = "usage-limit-stay"

// CapacityWakeSignalName is the chat-wide capacity wake: there may be capacity
// again (an account was added, re-authed, un-paused or read as no longer
// limited, the chat's model or provider changed, the user pressed Resume or
// "Retry now"), so every thread blocked on a provider retries now. It wakes
// every thread parked on a usage limit. In a run that recorded
// capacityWakeChangeID it also releases a self-pause armed on a provider
// failure and resumes every sub-agent that stopped on one (capacity_wake.go).
// A wake that finds nothing blocked is a no-op.
//
// Aliased from the capacitywake leaf package, as ThreadWakeSignalName is from
// threadwake, so senders that cannot import this package use the same name.
const CapacityWakeSignalName = capacitywake.SignalName

// CapacityWakeSignal is the payload of CapacityWakeSignalName.
type CapacityWakeSignal = capacitywake.Signal

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
	// usageLimitSwitchQuiet is how long a step goes without a usage limit
	// before its count of immediate account switches starts over.
	usageLimitSwitchQuiet = 2 * time.Minute
	// usageLimitMaxSwitches bounds the account switches a step makes in a row
	// with no wait between them. Every switch benches the refused account, so
	// the walk ends by itself once the provider's accounts are spent; this is
	// the backstop for a bench that did not take (a store error), which would
	// otherwise retry against the same account in a tight loop. Past it the
	// step waits.
	usageLimitMaxSwitches = 6
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
	// Facts is what the activity that was refused learned about the provider's
	// other accounts and the chat's fallback; zero when the failure carried
	// none (a driver error that never went through the account router).
	Facts types.UsageLimitFacts
}

// usageLimitState is what the run remembers about this step's earlier waits.
type usageLimitState struct {
	// Probes counts the consecutive waits on this step whose reset was unknown.
	Probes int
	// Switches counts the consecutive immediate retries on another account.
	Switches int
}

// usageLimitAction is what to do about a usage limit.
type usageLimitAction int

const (
	// usageLimitWaitUntil parks the step until Until, then retries it.
	usageLimitWaitUntil usageLimitAction = iota
	// usageLimitRetryNow retries the step at once: another account of the same
	// provider can serve it, and the refused one is already benched, so the
	// next resolution picks the other.
	usageLimitRetryNow
	// usageLimitSwitchModel retries the step at once on Model, another
	// provider, until Until (the original provider's earliest reset), then
	// returns to the chat's own model at the next turn.
	usageLimitSwitchModel
)

// usageLimitDecision is the answer to "what now?" for one usage limit.
type usageLimitDecision struct {
	Action usageLimitAction
	// Until is when to retry (usageLimitWaitUntil), or when the fallback lease
	// ends (usageLimitSwitchModel).
	Until time.Time
	// Probing is true when the provider named no reset, so Until is only the
	// next rung of the probe ladder and not a promise.
	Probing bool
	// Model is the "<model>@<driver>" to continue on (usageLimitSwitchModel).
	Model string
}

// decideOnUsageLimit is the ONE place that chooses what happens when a step
// hits a provider's usage limit. In order:
//
//  1. another account of the same provider can serve it: retry now (the refused
//     account is benched, so the next resolution moves the thread);
//  2. the chat allows a cross-provider fallback and it is servable: switch to
//     it until the original provider's earliest reset;
//  3. otherwise wait until the provider's earliest reset (any of its accounts
//     resetting unblocks the thread), or climb the probe ladder when no reset
//     is known.
//
// Cross-provider fallback is opt-in, so (2) needs facts.FallbackModel, which
// the activity only sets when the chat's policy asks for it.
//
// It must stay a pure function of its arguments: it runs in workflow code, so
// the time comes in as `now` (workflow.Now) and the jitter as `jitter`, derived
// deterministically by the caller.
func decideOnUsageLimit(limit usageLimit, facts types.UsageLimitFacts, state usageLimitState, now time.Time, jitter time.Duration) usageLimitDecision {
	if facts.AlternativeAccount && state.Switches < usageLimitMaxSwitches {
		return usageLimitDecision{Action: usageLimitRetryNow}
	}
	until, probing := usageLimitUntil(limit, facts, state, now, jitter)
	if facts.FallbackModel != "" {
		return usageLimitDecision{Action: usageLimitSwitchModel, Until: until, Probing: probing, Model: facts.FallbackModel}
	}
	return usageLimitDecision{Action: usageLimitWaitUntil, Until: until, Probing: probing}
}

// usageLimitUntil is when the provider is expected to serve again: the
// earliest reset across its accounts when the activity reported one, else this
// failure's own reset, else the next rung of the probe ladder. When the only
// time the activity knows is a probe bench's end, that is the next look.
func usageLimitUntil(limit usageLimit, facts types.UsageLimitFacts, state usageLimitState, now time.Time, jitter time.Duration) (until time.Time, probing bool) {
	// A bench with no reset behind it ends at a recheck time. That is when to
	// look again, not when the limit lifts, so it takes no margin and is
	// reported as a probe.
	if facts.ProviderResetIsProbe && facts.ProviderEarliestReset.After(now) {
		return facts.ProviderEarliestReset.Add(jitter), true
	}
	for _, reset := range []time.Time{facts.ProviderEarliestReset, limit.ResetAt} {
		if reset.After(now) {
			return reset.Add(usageLimitMargin + jitter), false
		}
	}
	// No reset, or one already in the past with the limit still enforced: the
	// provider's clock and ours disagree, or it said nothing. Probe.
	rung := state.Probes
	if rung >= len(usageLimitProbeLadder) {
		rung = len(usageLimitProbeLadder) - 1
	}
	return now.Add(usageLimitProbeLadder[rung] + jitter), true
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
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) && appErr.HasDetails() {
		var facts types.UsageLimitFacts
		if appErr.Details(&facts) == nil {
			limit.Facts = facts
		}
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
	probes   int
	switches int
	last     time.Time
}

// fallbackLease is a thread's stay on another provider: while workflow time is
// before Until its call_llm steps run on Model, and the first turn after it
// returns to the chat's own model.
type fallbackLease struct {
	Model    string
	Provider string
	Until    time.Time
	// Returning is true from the first turn after Until, which tries the
	// chat's own model again. A refused attempt extends the stay; the next
	// turn after one that was not refused ends it.
	Returning bool
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

	// useModel is the model the latest "Use <model> now" named, valid for the
	// waits that began before useModelEpoch was reached.
	useModel      string
	useModelEpoch int
	// leases are the threads currently on a fallback model.
	leases map[string]*fallbackLease
	// keepStays is the usage-limit-stay version of this run: a lease outlives
	// the first turn after its end until that turn is not refused.
	keepStays bool
	// capacityWake is the capacity-wake version of this run
	// (capacityWakeChangeID): it listens from the start and a wake also
	// resumes the run's other blocked threads (listenFromStart).
	capacityWake bool
}

func newUsageLimitWaiter(pauser *pauseCoordinator, tracker *ChildWorkflowTracker, chatID, workflowID, workflowName string) *usageLimitWaiter {
	return &usageLimitWaiter{
		pauser:       pauser,
		tracker:      tracker,
		chatID:       chatID,
		workflowID:   workflowID,
		workflowName: workflowName,
		probes:       map[string]*usageLimitProbe{},
		leases:       map[string]*fallbackLease{},
	}
}

// startListening begins counting capacity wakes for a run recorded before
// capacityWakeChangeID. It starts on the first wait, behind the version gate,
// so such a run that never meets a usage limit has exactly the coroutines it
// had before. Signals that arrived earlier are dropped: they were aimed at a
// wait that was not there. A run that records the gate listens from its start
// instead (listenFromStart), and this is a no-op.
func (w *usageLimitWaiter) startListening() {
	if w.listening {
		return
	}
	w.listening = true
	ch := workflow.GetSignalChannel(w.pauser.root, CapacityWakeSignalName)
	for ch.ReceiveAsync(nil) {
	}
	w.listen(ch, nil)
}

// listenFromStart listens for capacity wakes from the run's start, and on each
// one, after advancing the waits' epoch, calls onWake: the run's other blocked
// threads go on too (wakeBlockedThreads). Called once, at run start, by a run
// that recorded capacityWakeChangeID.
func (w *usageLimitWaiter) listenFromStart(onWake func(workflow.Context, CapacityWakeSignal)) {
	w.listening, w.capacityWake = true, true
	w.listen(workflow.GetSignalChannel(w.pauser.root, CapacityWakeSignalName), onWake)
}

func (w *usageLimitWaiter) listen(ch workflow.ReceiveChannel, onWake func(workflow.Context, CapacityWakeSignal)) {
	workflow.Go(w.pauser.root, func(gCtx workflow.Context) {
		for {
			var sig CapacityWakeSignal
			if !ch.Receive(gCtx, &sig) {
				return
			}
			w.retryEpoch++
			w.useModel, w.useModelEpoch = sig.UseModel, w.retryEpoch
			if onWake != nil {
				onWake(gCtx, sig)
			}
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
	// Acting on the activity's facts is gated separately from waiting at all:
	// a history recorded before failover took the wait path whatever the facts
	// said.
	failover := workflow.GetVersion(ctx, usageLimitFailoverChangeID, workflow.DefaultVersion, 1) != workflow.DefaultVersion
	// The refinements of the decision are gated on their own: a history that
	// recorded the failover gate before them keeps the old behaviour.
	w.keepStays = failover && workflow.GetVersion(ctx, usageLimitStayChangeID, workflow.DefaultVersion, 1) != workflow.DefaultVersion
	facts := limit.Facts
	if !failover {
		facts = types.UsageLimitFacts{}
	}
	if !w.keepStays {
		facts.ProviderResetIsProbe = false
	}
	if now.Sub(probe.last) > usageLimitSwitchQuiet {
		probe.switches = 0
	}
	decision := decideOnUsageLimit(limit, facts, usageLimitState{Probes: probe.probes, Switches: probe.switches}, now,
		usageLimitJitter(req.Thread, req.StepID, probe.probes))
	if decision.Probing {
		probe.probes++
	} else {
		probe.probes = 0
	}
	if decision.Action == usageLimitRetryNow {
		probe.switches++
	} else {
		probe.switches = 0
	}
	probe.last = now
	// A refused attempt to return is not a return: the stay goes on (or the
	// wait below starts), and the attempt is made again afterwards.
	if lease := w.leases[req.Thread]; lease != nil && w.keepStays {
		lease.Returning = false
	}
	switch decision.Action {
	case usageLimitRetryNow:
		return w.continueNow(ctx, req, pc, limit, "another account of the same provider can serve it; retrying now")
	case usageLimitSwitchModel:
		w.startLease(ctx, req.Thread, limit, facts, decision.Model, decision.Until, decision.Probing)
		return w.continueNow(ctx, req, pc, limit, "continuing on "+decision.Model+" until the provider resets")
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
	// "Use <model> now" answers the wait with a stay on that model until the
	// wait's own end instead of a retry on the same provider. A capacity wake
	// carries one when the user switched the chat to another provider; it is a
	// way out only for a thread that waits on a different provider than the
	// model's, so a thread already on that provider just retries.
	alreadyOnIt := w.capacityWake && sameProvider(w.useModel, facts.Provider)
	if failover && w.useModel != "" && w.useModelEpoch > startEpoch && decision.Until.After(workflow.Now(ctx)) && !alreadyOnIt {
		w.startLease(ctx, req.Thread, limit, facts, w.useModel, decision.Until, decision.Probing)
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

// continueNow ends a usage-limit failure without waiting: the caller
// re-dispatches the step. It still honours a user pause and a cancellation,
// as any step boundary would.
func (w *usageLimitWaiter) continueNow(ctx workflow.Context, req usageLimitWaitRequest, pc *PauseController, limit usageLimit, why string) usageLimitOutcome {
	if !workflow.IsReplaying(ctx) {
		workflow.GetLogger(ctx).Info("[UsageLimit] Provider usage limit reached; not waiting",
			"workflowID", w.workflowID, "thread", req.Thread, "stepID", req.StepID,
			"provider", limit.Provider, "why", why)
	}
	if req.Cancelled != nil && req.Cancelled() {
		return usageLimitOutcome{Handled: true, Err: ErrThreadCancelled}
	}
	pc.DoCheckPause(ctx)
	_ = workflow.Sleep(ctx, 0)
	if ctx.Err() != nil {
		return usageLimitOutcome{Handled: true, Err: ctx.Err()}
	}
	return usageLimitOutcome{Handled: true}
}

// startLease puts thread on model until `until` and tells the user. The lease
// is workflow state: call_llm reads it back through fallbackFor, so a replay
// derives the same model for the same step.
//
// A thread already on a stay on model (the attempt to return was refused, or
// the stay's end has not been acted on yet) goes on with it: the user was told
// at the switch and nothing has changed since, so the stay is extended and not
// announced again. probing says until is the next recheck, not a reset.
func (w *usageLimitWaiter) startLease(ctx workflow.Context, thread string, limit usageLimit, facts types.UsageLimitFacts, model string, until time.Time, probing bool) {
	if lease := w.leases[thread]; lease != nil && w.keepStays && lease.Model == model {
		lease.Until, lease.Returning = until, false
		return
	}
	provider := facts.Provider
	if provider == "" {
		provider = limit.Provider
	}
	w.leases[thread] = &fallbackLease{Model: model, Provider: provider, Until: until}
	err := workflow.ExecuteActivity(markerContext(ctx), "UsageLimitWait", map[string]interface{}{
		"chat_id":     w.chatID,
		"thread":      thread,
		"workflow_id": w.workflowID,
		"notice":      "switch_provider",
		"provider":    provider,
		"model":       model,
		"resume_at":   until.UTC().Format(time.RFC3339),
		"probing":     probing,
	}).Get(ctx, nil)
	if err != nil && !errors.Is(err, workflow.ErrCanceled) {
		workflow.GetLogger(ctx).Warn("[UsageLimit] Could not post the fallback notice; continuing anyway",
			"workflowID", w.workflowID, "thread", thread, "error", err)
	}
}

// fallbackFor is the thread's stay on a fallback model: the model while it is
// live, or — on the first turn after it — what the chat is returning from, so
// that turn can say so once it has gone through on its own model.
//
// The stay does not end at that turn. If the turn is refused (the limit is
// still there; for an unknown reset the end was only a recheck) wait() puts
// the stay back, and the attempt is made again at its next end. It ends at the
// next turn that finds the attempt not refused.
func (w *usageLimitWaiter) fallbackFor(ctx workflow.Context, thread string) (model string, ret *types.FallbackReturn) {
	lease := w.leases[thread]
	if lease == nil {
		return "", nil
	}
	if workflow.Now(ctx).Before(lease.Until) {
		return lease.Model, nil
	}
	if !w.keepStays {
		delete(w.leases, thread)
		return "", &types.FallbackReturn{Provider: lease.Provider, Model: lease.Model}
	}
	if lease.Returning {
		delete(w.leases, thread)
		return "", nil
	}
	lease.Returning = true
	return "", &types.FallbackReturn{Provider: lease.Provider, Model: lease.Model}
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

// liveFallback is the model the thread is on while its fallback lease lasts,
// without consuming the lease's end the way fallbackFor does. A stay whose
// return attempt went through is over by the time anything else asks.
func (pc *PauseController) liveFallback(ctx workflow.Context, thread string) string {
	if pc == nil || pc.UsageLimit == nil {
		return ""
	}
	w := pc.UsageLimit
	lease := w.leases[thread]
	if lease == nil {
		return ""
	}
	if workflow.Now(ctx).Before(lease.Until) {
		return lease.Model
	}
	if w.keepStays && lease.Returning {
		delete(w.leases, thread)
	}
	return ""
}

// fallbackFor is usageLimitWaiter.fallbackFor, nil-safe like every
// PauseController method.
func (pc *PauseController) fallbackFor(ctx workflow.Context, thread string) (string, *types.FallbackReturn) {
	if pc == nil || pc.UsageLimit == nil {
		return "", nil
	}
	return pc.UsageLimit.fallbackFor(ctx, thread)
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
