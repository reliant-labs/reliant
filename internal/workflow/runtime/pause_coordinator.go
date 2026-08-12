// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"time"

	"go.temporal.io/sdk/workflow"
)

// resumeHoldVersionGate is the workflow.GetVersion changeID for the
// resume-hold behavior in the pause coordinator (see newPauseCoordinator).
// Histories recorded before this change must keep the old consume-immediately
// behavior on replay; new executions get the hold behavior.
const resumeHoldVersionGate = "resume-hold"

// selfPauseBackoff is the escalating wait before an UNATTENDED run resumes
// itself from a self-pause. A self-pause is the workflow's own decision — a
// retryable error exhausted Temporal's retry budget, or the daemon-offline
// breaker tripped — and it parks on workflow.Await with no timeout, so the only
// thing that can restart it is a human sending a resume. On an unattended run
// there is no human, and the run is dead where it stands for as long as anyone
// leaves it: not failed, not finished, holding its workspace.
//
// Both causes are overwhelmingly transient. A 429 clears, a daemon reconnects.
// Waiting and trying again is what the human would do, so an unattended run
// does it itself, backing off because a cause that survived the last wait is
// less likely to clear in the same interval again.
//
// The ladder is FINITE on purpose. After it runs out the run parks exactly as
// it does today, because a self-pause that survived 33 minutes of escalating
// retries is no longer evidence of a rate limit — it is evidence that
// something needs a person, and burning tokens against it forever is worse
// than stopping where a person can see it.
var selfPauseBackoff = []time.Duration{
	1 * time.Minute,
	2 * time.Minute,
	5 * time.Minute,
	10 * time.Minute,
	15 * time.Minute,
}

// selfPauseLadderReset is how long a run must go without needing a self-resume
// before the ladder starts over. Without it the ladder counts a run's LIFETIME
// self-pauses, so five unrelated rate limits spread over an eight-hour run
// exhaust it and the sixth parks forever — punishing a long run for being
// long. The ladder is meant to bound one episode of a persistent cause, and an
// episode that has been quiet this long is over.
const selfPauseLadderReset = 30 * time.Minute

// pauseCoordinator owns the workflow's cooperative pause/resume machinery:
// the signal.pause / signal.resume channels, the shared cancellable activity
// context, and the epoch-based broadcast that wakes paused goroutines.
//
// Pause/resume coordination uses an epoch-based broadcast pattern. A single
// resume-coordinator goroutine consumes the resume signal and increments the
// epoch. All goroutines (main loop + inline spawns) block on workflow.Await()
// waiting for the epoch to advance, which wakes them ALL — unlike
// resumeCh.Receive(), which only unblocks one consumer.
//
// Resume-hold (gated by resumeHoldVersionGate): a resume signal that arrives
// while NO pause is armed is HELD until one arms, instead of being consumed
// as a no-op epoch bump. Without the hold, reset-and-replay of a self-paused
// workflow loses the resume: the reset appends signal.resume to history, and
// on replay the coordinator consumes it BEFORE the re-executed
// retry-exhaustion branch re-arms the pause — the workflow then parks against
// an epoch the resume has already advanced past and waits forever for a
// resume that was already spent. A held resume is discarded if a NEW
// signal.pause arrives first (an explicit user pause must never be undone by
// a stale queued resume); a self-pause (requestPause/arm — retry exhaustion,
// daemon-offline breaker) is released by a held resume by design, since that
// is exactly the reset-and-replay ordering the hold exists for.
//
// Known residual gap: if TWO executors independently arm (e.g. parallel
// spawns both exhausting) and a reset-and-replay carries a single resume,
// the release after the first arm lets the second arm re-park with no resume
// left — the original timeline coalesced both into one pause episode, and
// that coalescing cannot be reconstructed from replay order. A further
// resume (the paused DB status routes SendMessage there) recovers it.
//
// Unattended self-resume: on a run with no human (see unattended.go), a
// SELF-pause resumes itself on the selfPauseBackoff ladder instead of parking
// forever. A USER pause never does — someone who paused a run has said what
// they want, and "unattended" means nobody is watching, not that nobody may
// instruct.
type pauseCoordinator struct {
	root       workflow.Context
	workflowID string

	// holdResume enables the resume-hold behavior. False when replaying a
	// history recorded before resumeHoldVersionGate.
	holdResume bool

	// unattended enables the self-resume ladder. Set from the run's
	// `unattended` input; a run with a human at the keyboard never
	// self-resumes, because that human is the resume.
	unattended bool

	requested bool
	epoch     int
	// pauseSignals counts user-initiated pause signals (signal.pause). A held
	// resume snapshots it and discards itself when it changes, so an explicit
	// pause always wins over a stale queued resume.
	pauseSignals int
	// selfPaused distinguishes the workflow pausing ITSELF (RequestPause/Arm —
	// retry exhaustion, daemon-offline breaker) from a human sending
	// signal.pause. Only a self-pause is eligible for the self-resume ladder,
	// and any user pause clears it: if someone pauses a run that had already
	// self-paused, it is now stopped because they said so.
	selfPaused bool
	// selfResumes counts consecutive rungs climbed on selfPauseBackoff;
	// lastSelfResume is when the last rung fired, so a stretch quieter than
	// selfPauseLadderReset starts the ladder over.
	selfResumes    int
	lastSelfResume time.Time

	activityCtx workflow.Context
	cancelAll   workflow.CancelFunc

	pauseCh  workflow.ReceiveChannel
	resumeCh workflow.ReceiveChannel
}

// pauseOptions configures a pauseCoordinator. It is a struct rather than
// positional bools because `newPauseCoordinator(ctx, id, true, false)` says
// nothing about which behavior each flag turns on.
type pauseOptions struct {
	// HoldResume enables the resume-hold behavior. False when replaying a
	// history recorded before resumeHoldVersionGate.
	HoldResume bool
	// Unattended enables the self-resume ladder for self-pauses.
	Unattended bool
}

// newPauseCoordinator sets up the pause/resume infrastructure on the root
// workflow context and starts its coordinator goroutines. Must be called
// exactly once per workflow execution, before any executor can dispatch
// activities.
func newPauseCoordinator(ctx workflow.Context, workflowID string, opts pauseOptions) *pauseCoordinator {
	pc := &pauseCoordinator{
		root:       ctx,
		workflowID: workflowID,
		holdResume: opts.HoldResume,
		unattended: opts.Unattended,
	}
	pc.pauseCh = workflow.GetSignalChannel(ctx, "signal.pause")
	pc.resumeCh = workflow.GetSignalChannel(ctx, "signal.resume")

	// Shared cancellable context for all activity dispatch. One cancelAll()
	// call cancels every in-flight activity at any nesting depth — including
	// those in inline workflow executors and loop executors.
	pc.activityCtx, pc.cancelAll = workflow.WithCancel(ctx)

	// Background goroutine to listen for pause signals at any time. This
	// ensures the flag is set even while the workflow is blocked in
	// waitForAnyCompletion().
	workflow.Go(ctx, func(gCtx workflow.Context) {
		for {
			pc.pauseCh.Receive(gCtx, nil)
			pc.requested = true
			pc.pauseSignals++
			// A human asked for this pause, so it is no longer a self-pause and
			// the self-resume ladder must not touch it — even if a self-pause
			// was already armed when the signal landed.
			pc.selfPaused = false
			// cancelAll() is a Temporal SDK command (not a side effect)
			// and MUST execute during replay to maintain determinism.
			pc.cancelAll()
		}
	})

	// Resume coordinator goroutine: consumes the resume signal and broadcasts
	// to all paused goroutines by advancing the epoch counter and refreshing
	// the shared activity context.
	workflow.Go(ctx, func(gCtx workflow.Context) {
		for {
			pc.resumeCh.Receive(gCtx, nil)
			if pc.holdResume && !pc.requested {
				// No pause armed: hold the resume until one arms, or discard
				// it if a newer explicit pause supersedes it (see type doc).
				held := pc.pauseSignals
				_ = workflow.Await(gCtx, func() bool {
					return pc.requested || pc.pauseSignals != held
				})
				if pc.pauseSignals != held {
					continue
				}
			}
			pc.broadcastResume()
			if !workflow.IsReplaying(gCtx) {
				workflow.GetLogger(pc.root).Info("[Workflow Runtime] Resume coordinator: broadcast resume to all goroutines",
					"workflowID", pc.workflowID,
					"pauseEpoch", pc.epoch,
				)
			}
		}
	})

	// Self-resume coordinator: on an unattended run, a SELF-pause that nobody
	// is coming to clear resumes itself on selfPauseBackoff. Only started when
	// unattended, so an attended run's command stream is byte-for-byte what it
	// was before this existed.
	//
	// It is a single goroutine and the only other writer of the epoch, so a
	// timer firing while ten executors sit in CheckPause produces ONE resume,
	// not ten. Doing this inside CheckPause instead would arm one timer per
	// blocked goroutine and race them to bump the epoch.
	if pc.unattended {
		workflow.Go(ctx, pc.selfResumeLoop)
	}

	return pc
}

// broadcastResume clears the armed pause and wakes every goroutine blocked in
// CheckPause. The context refresh and the epoch bump are Temporal SDK
// operations that must also happen during replay, so this runs unconditionally
// — never inside an IsReplaying guard.
//
// Both resume paths (the signal coordinator and the self-resume ladder) go
// through here so there is exactly one definition of what resuming does.
func (pc *pauseCoordinator) broadcastResume() {
	pc.requested = false
	pc.selfPaused = false
	pc.activityCtx, pc.cancelAll = workflow.WithCancel(pc.root)
	pc.epoch++
}

// selfResumeLoop is the unattended self-resume ladder. Started only when the
// run is unattended (see newPauseCoordinator).
//
// It waits for a self-pause to arm, then waits out the current backoff rung.
// If a real resume (or a user pause) lands first, the wait ends early and
// nothing is done — the ladder never overrides a decision someone else made.
// If the rung elapses with the self-pause still armed, it resumes the workflow
// itself.
func (pc *pauseCoordinator) selfResumeLoop(gCtx workflow.Context) {
	for {
		// Park until the workflow pauses ITSELF. A user pause never satisfies
		// this, so a human-paused run stays paused.
		_ = workflow.Await(gCtx, func() bool { return pc.requested && pc.selfPaused })

		// A quiet stretch means the previous episode is over: start again at
		// the bottom of the ladder rather than punishing a long run for having
		// survived earlier, unrelated rate limits.
		if !pc.lastSelfResume.IsZero() && workflow.Now(gCtx).Sub(pc.lastSelfResume) >= selfPauseLadderReset {
			pc.selfResumes = 0
		}
		if pc.selfResumes >= len(selfPauseBackoff) {
			// Ladder exhausted. Park exactly as an attended run does — a cause
			// that outlived every rung needs a person, not another retry.
			if !workflow.IsReplaying(gCtx) {
				workflow.GetLogger(pc.root).Warn("[Workflow Runtime] Unattended self-resume ladder exhausted, staying paused for a human",
					"workflowID", pc.workflowID,
					"selfResumes", pc.selfResumes,
				)
			}
			_ = workflow.Await(gCtx, func() bool { return !pc.requested })
			continue
		}

		wait := selfPauseBackoff[pc.selfResumes]
		// Ends early if the pause clears (a real resume) or stops being a
		// self-pause (a user pause superseded it).
		resolved, _ := workflow.AwaitWithTimeout(gCtx, wait, func() bool {
			return !pc.requested || !pc.selfPaused
		})
		if resolved {
			continue
		}

		pc.selfResumes++
		pc.lastSelfResume = workflow.Now(gCtx)
		if !workflow.IsReplaying(gCtx) {
			workflow.GetLogger(pc.root).Warn("[Workflow Runtime] Unattended run resuming itself from a self-pause",
				"workflowID", pc.workflowID,
				"waited", wait,
				"attempt", pc.selfResumes,
				"remainingRungs", len(selfPauseBackoff)-pc.selfResumes,
			)
		}
		pc.broadcastResume()
	}
}

// ActivityCtx returns the current (possibly refreshed) cancellable context.
// All executors call this when dispatching activities, so after resume they
// automatically pick up the fresh context.
func (pc *pauseCoordinator) ActivityCtx() workflow.Context {
	return pc.activityCtx
}

// PauseArmed reports whether a pause is currently armed (by either a user
// signal or the workflow itself). Read by the ContinueAsNew boundary check: a
// continuation started while a pause is armed would drop the resume signal
// that is the only thing able to restart this run, and come back running —
// silently undoing the pause.
func (pc *pauseCoordinator) PauseArmed() bool {
	return pc.requested
}

// RequestPause triggers a self-pause from within the workflow, cancelling all
// in-flight activities. Used by executors when a retryable error (like a rate
// limit) exhausts retries.
func (pc *pauseCoordinator) RequestPause() {
	pc.requested = true
	pc.selfPaused = true
	pc.cancelAll()
}

// Arm marks the pause as requested WITHOUT cancelling in-flight activities.
// Used by pause sites that either want running activities to finish naturally
// (daemon-offline breaker) or rely on CheckPause to do the cancel.
func (pc *pauseCoordinator) Arm() {
	pc.requested = true
	pc.selfPaused = true
}

// CheckPause checks for a pending pause and blocks until resume if paused.
// Called at step boundaries to provide cooperative pause/resume. Multiple
// goroutines can call this concurrently — they all block via workflow.Await
// on the epoch counter rather than competing over a single signal channel
// receive.
func (pc *pauseCoordinator) CheckPause(callerCtx workflow.Context) {
	// Non-blocking drain of any pending pause signals
	for pc.pauseCh.ReceiveAsync(nil) {
		pc.requested = true
		pc.pauseSignals++
		// Same reason as the pause listener: a human asked, so this is no
		// longer a self-pause and the unattended ladder must leave it alone.
		pc.selfPaused = false
	}
	if !pc.requested {
		return
	}
	// cancelAll() is a Temporal SDK command (not a side effect)
	// and MUST execute during replay to maintain determinism.
	pc.cancelAll()
	if !workflow.IsReplaying(pc.root) {
		workflow.GetLogger(pc.root).Info("[Workflow Runtime] Pause requested, cancelling activities and blocking until resume signal",
			"workflowID", pc.workflowID,
		)
	}
	// Snapshot current epoch, then wait for the resume coordinator
	// to advance it. workflow.Await wakes ALL blocked goroutines.
	// IMPORTANT: callerCtx (not root ctx) is used here so that goroutines
	// spawned via workflow.Go() block on their own coroutine, avoiding
	// Temporal's "trying to block on coroutine which is already blocked" panic.
	myEpoch := pc.epoch
	_ = workflow.Await(callerCtx, func() bool { return pc.epoch > myEpoch })
	if !workflow.IsReplaying(pc.root) {
		workflow.GetLogger(pc.root).Info("[Workflow Runtime] Resume signal received, continuing with fresh activity context",
			"workflowID", pc.workflowID,
		)
	}
}
