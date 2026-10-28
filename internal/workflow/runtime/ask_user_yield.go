package runtime

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// AN ask_user WAIT YIELDS TO NEW INPUT FOR ITS THREAD.
//
// A thread blocked in ask_user used to wait on exactly two things: the
// question's answer signal and a 24h timer. Everything else the user could do
// to that thread was a no-op. A message to it rang its doorbell (thread_wake),
// which only bumps a counter that parked loop gates read; the mailbox is
// drained by call_llm, which a thread inside a tool call never reaches. An
// interrupt advanced its interrupt epoch, which cancels activity contexts —
// and the wait is not an activity. Prod, chat adcee226 (2026-10-21): a spawn
// asked a question nobody saw, the user typed "continue" to it twice and hit
// "send now" seven times, and none of it reached the agent.
//
// So the wait now also ends when the user messages or interrupts the thread
// after it began. The question is resolved unanswered (it stops showing as
// pending), and the tool result tells the model the user did not answer and
// why; the loop's next call_llm then drains the mailbox and reads history,
// which is where the user's message is.
//
// Both are read through the mechanisms every other parked wait uses — the
// tracker's per-thread wake counts and ThreadInterrupt.InterruptedSince,
// snapshotted when the wait begins and compared against the live value — so
// there is one doorbell and one interrupt epoch per thread, not a second
// channel for questions. Of the doorbell's rings it counts only the user's
// (ChildWorkflowTracker.userInputCount, ThreadWakeSignal.FromUser): another
// agent's spawn_send rings the same doorbell, and a peer's message is no
// reason to take the user's question away from them. Interrupts come only
// from the user.

// askUserYieldChangeID gates the yield. It is versioned per OBSERVATION, not
// once per run, so a wait that was already parked when this shipped becomes
// interruptible too — which is the case that matters, since the run that
// motivated it was parked in exactly this wait at deploy time.
//
// A single change id cannot do that. Replaying a history recorded before the
// change, the watcher sees the interrupts that history already holds (its
// doorbells predate FromUser, so they are not counted); the first GetVersion
// it asks returns DefaultVersion (no marker), and the SDK caches that answer
// for the rest of the execution, so a later live message would be ignored
// too. A fresh id per observation keeps each answer independent: every
// observation an old history recorded replays as DefaultVersion (keep
// waiting, as the old code did), and the first one made live records a marker
// and ends the wait.
//
// The ids are bounded by the number of waits that actually yielded: an
// observation answered with DefaultVersion records nothing, and a wait stops
// observing once one is answered with version 1.
const askUserYieldChangeID = "ask-user-yields-to-thread-input"

// askUserYieldVersionID is the change id for the n-th observation (1-based)
// of new input during the wait on questionID.
func askUserYieldVersionID(questionID string, observation int) string {
	return fmt.Sprintf("%s/%s/%d", askUserYieldChangeID, questionID, observation)
}

// askUserYield is why an ask_user wait ended without an answer.
type askUserYield int

const (
	askUserYieldNone askUserYield = iota
	// askUserYieldMessage: the user rang the thread's doorbell — a message
	// was queued to its mailbox or saved to its history.
	askUserYieldMessage
	// askUserYieldInterrupt: the thread was interrupted.
	askUserYieldInterrupt
)

// askUserThreadInput is what an ask_user wait reads to notice new input for
// its thread. Either may be nil; a nil source never fires.
type askUserThreadInput struct {
	tracker   *ChildWorkflowTracker
	interrupt *ThreadInterrupt
	// park, when set, lets the wait park its background spawn for a
	// continue-as-new handoff (ask_user_handoff.go).
	park *askUserPark
}

// awaitAskUserYield watches for new input for thread from the moment it is
// called, and delivers one askUserYield on the returned channel when the wait
// should end for it. Cancel ctx to stop watching.
//
// It issues no command until it decides to yield, and then only the version
// marker, so a wait that ends any other way replays exactly as before.
func awaitAskUserYield(ctx workflow.Context, questionID, thread string, in askUserThreadInput) workflow.ReceiveChannel {
	yieldCh := workflow.NewBufferedChannel(ctx, 1)
	if in.tracker == nil && in.interrupt == nil {
		return yieldCh
	}
	workflow.Go(ctx, func(gCtx workflow.Context) {
		wakes := in.tracker.userInputCount(thread)
		epoch := in.interrupt.Epoch()
		for observation := 1; ; observation++ {
			var why askUserYield
			if err := workflow.Await(gCtx, func() bool {
				switch {
				case in.interrupt.InterruptedSince(epoch):
					why = askUserYieldInterrupt
				case in.tracker.userInputCount(thread) > wakes:
					why = askUserYieldMessage
				default:
					return false
				}
				return true
			}); err != nil {
				return
			}
			if workflow.GetVersion(gCtx, askUserYieldVersionID(questionID, observation), workflow.DefaultVersion, 1) == workflow.DefaultVersion {
				// Recorded before the yield existed: that run kept waiting
				// through this input, so the replay does too.
				wakes = in.tracker.userInputCount(thread)
				epoch = in.interrupt.Epoch()
				continue
			}
			workflow.GetLogger(gCtx).Info("[AskUser] New input for the asking thread; ending the wait unanswered",
				"questionID", questionID, "thread", thread, "reason", why.String())
			yieldCh.Send(gCtx, why)
			return
		}
	})
	return yieldCh
}

func (y askUserYield) String() string {
	switch y {
	case askUserYieldMessage:
		return "message"
	case askUserYieldInterrupt:
		return "interrupt"
	default:
		return "none"
	}
}

// askUserYieldedToMessage and askUserYieldedToInterrupt are the tool results
// for a question the user did not answer because they did something else. The
// model reads them, so they say plainly that there is no answer and where the
// user's input is.
const (
	askUserYieldedToMessage = "The user did not answer this question. They sent you a message instead, " +
		"which follows in the conversation. Read it and respond to it; if it does not settle what you asked, " +
		"you may ask again."
	askUserYieldedToInterrupt = "The user interrupted this question without answering it. Check the conversation " +
		"for anything new from them and continue from there; ask again only if you still need the answer."
)

// resolveYieldedAskUser closes a question whose wait yielded to new input and
// returns the tool result for it.
//
// The question is resolved with no answer so it stops showing as pending, and
// so a late answer to it is refused (the answer RPC requires a pending
// question) rather than signalled into a wait that no longer exists. If the
// user's answer beat this to the row, the activity reports it and that answer
// is returned instead: it is what the model asked for.
func resolveYieldedAskUser(ctx workflow.Context, toolCallID, questionID string, yield askUserYield, logger log.Logger) map[string]interface{} {
	resolveCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	})
	var resolveOutput struct {
		AlreadyResolved bool   `json:"already_resolved"`
		ResponseData    string `json:"response_data"`
	}
	if err := workflow.ExecuteActivity(resolveCtx, "QuestionResolve", map[string]interface{}{
		"question_id":   questionID,
		"response_data": "",
	}).Get(ctx, &resolveOutput); err != nil {
		// The wait is over either way. A row left pending is a stale prompt
		// in the UI, not a stuck run, and the conversation must go on.
		logger.Warn("[AskUser] Failed to resolve question the user did not answer",
			"questionID", questionID, "reason", yield.String(), "error", err)
	}
	if resolveOutput.AlreadyResolved && resolveOutput.ResponseData != "" {
		return map[string]interface{}{
			"tool_call_id": toolCallID,
			"content":      formatAskUserResponse("reply", resolveOutput.ResponseData),
		}
	}

	content := askUserYieldedToMessage
	if yield == askUserYieldInterrupt {
		content = askUserYieldedToInterrupt
	}
	return map[string]interface{}{
		"tool_call_id": toolCallID,
		"content":      content,
	}
}
