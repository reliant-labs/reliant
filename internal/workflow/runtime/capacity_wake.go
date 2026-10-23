// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"sort"
	"strings"

	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
)

// A chat-wide capacity wake (CapacityWakeSignalName) is how a chat whose
// provider ran out goes on as a whole, with one user action, once there is
// capacity again: an account was added, re-authed or un-paused, the chat's
// model or provider was switched, or the user pressed Resume or "Retry now".
//
// Every thread of a chat runs in its one execution, and before this each kind
// of blocked thread woke on something different:
//
//   - a thread in a usage-limit wait woke on its own reset timer, a message to
//     it, a pause, or "Retry now" (which the server only sent while the ROOT
//     waited);
//   - a sub-agent that stopped on a provider failure (spent credit, a rejected
//     key, a model no provider could serve) woke only on a message to it;
//   - a root that self-paused on such a failure woke only on signal.resume.
//
// So with the root and two sub-agents blocked, adding an account woke nothing,
// and Resume woke the root alone: the user had to message every sub-agent.
//
// A capacity wake answers all three at once (wakeBlockedThreads): it advances
// the usage-limit waiter's retry epoch (every wait retries now, and the account
// router moves a thread off a benched account onto a usable one), releases a
// self-pause armed on a provider-capacity failure, and resumes every sub-agent
// that stopped on one. It never undoes a user's pause, and never resumes an
// agent that stopped for any other reason: those are a person's or the
// parent's to decide on.
//
// A sub-agent the execution only knows about from durable state (it stopped in
// an execution that has since completed, or before a coarse restart) is not
// resumed by a wake: the cause it stopped on is not recorded durably. A message
// to it still resumes it (subagent_resume.go).
//
// A wake sent for the user's Resume (CapacityWakeSignal.AllStopped) resumes
// every stopped sub-agent this execution holds, whatever it stopped on: the
// user asked the whole chat to go on.

// capacityWakeChangeID gates the chat-wide capacity wake (workflow.GetVersion).
// It is consulted once, at run start: a run that records it listens for the
// wake from its first moment and acts on it as above. A history recorded
// before it has no marker, so it listens only from its first usage-limit wait
// and only advances the waits' epoch, as it always did
// (frozen/2026-10-23-capacity-wake-ignored).
const capacityWakeChangeID = "capacity-wake-resumes-chat"

// providerCapacityError reports whether err is a failure that capacity can
// fix — another account, another provider, a re-auth, more credit — rather than
// a failure of the request itself. Those are what a capacity wake retries.
//
// Every error in the chain is read: a stopped sub-agent's error
// (subAgentFailedError) says the cause in one summarized line, and the
// provider's own text is the error it wraps.
func providerCapacityError(err error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if capacityErrorText(err) {
			return true
		}
	}
	return false
}

func capacityErrorText(err error) bool {
	if _, ok := parseUsageLimit(err); ok {
		return true
	}
	if drivererrors.IsProviderCreditExhaustion(err) {
		return true
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	if extractResolutionSummary(msg) != "" || extractProviderReconnectSummary(lower) != "" {
		return true
	}
	for _, signal := range []string{"rate limit", "rate_limit", "too many requests", "overloaded"} {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}

// sameProvider reports whether modelAtDriver ("<model>@<driver>") is served by
// driver. Unknown either way is not the same.
func sameProvider(modelAtDriver, driver string) bool {
	i := strings.LastIndex(modelAtDriver, "@")
	return driver != "" && i >= 0 && strings.EqualFold(modelAtDriver[i+1:], driver)
}

// wakeBlockedThreads is what a capacity wake does beyond advancing the
// usage-limit waits' epoch (usageLimitWaiter.listen does that first): release a
// self-pause armed on a provider-capacity failure, then resume every sub-agent
// that stopped on one — or, with allStopped (the user pressed Resume), every
// sub-agent that stopped on any failure — parents before children.
func wakeBlockedThreads(ctx workflow.Context, pauser *pauseCoordinator, tracker *ChildWorkflowTracker, workflowID string, allStopped bool) {
	logger := workflow.GetLogger(ctx)
	if pauser.releaseCapacityPause() && !workflow.IsReplaying(ctx) {
		logger.Info("[CapacityWake] Released a pause the run took on a provider failure",
			"workflowID", workflowID)
	}
	if tracker == nil || tracker.resumeStopped == nil {
		return
	}
	for _, thread := range tracker.stoppedThreadsToWake(allStopped) {
		if tracker.resumeStopped(ctx, thread) && !workflow.IsReplaying(ctx) {
			logger.Info("[CapacityWake] Resumed a sub-agent that stopped on a provider failure",
				"workflowID", workflowID, "thread", thread)
		}
	}
}

// stoppedThreadsToWake are the threads of the sub-agents this execution holds
// as stopped on a provider-capacity failure (or on any failure, with all),
// parents before children and then by thread, so a wake resumes them in the
// same order on replay and an issuing agent is running before the agents it
// issued.
func (t *ChildWorkflowTracker) stoppedThreadsToWake(all bool) []string {
	if t == nil {
		return nil
	}
	recs := make([]*detachedSpawnRecord, 0, len(t.stoppedSpawns))
	for _, rec := range t.stoppedSpawns {
		if all || rec.handoff.CapacityStop {
			recs = append(recs, rec)
		}
	}
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].handoff.SpawnDepth != recs[j].handoff.SpawnDepth {
			return recs[i].handoff.SpawnDepth < recs[j].handoff.SpawnDepth
		}
		return recs[i].ChildThread < recs[j].ChildThread
	})
	threads := make([]string, len(recs))
	for i, rec := range recs {
		threads[i] = rec.ChildThread
	}
	return threads
}
