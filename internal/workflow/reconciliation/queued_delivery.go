// Copyright (c) 2025 Reliant Labs
package reconciliation

import (
	"context"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// QueuedDelivery starts the runs that deliver messages no run is going to
// read (internal/queueddelivery, over ChatService.ContinueQueued). Declared
// here, at the consumer.
type QueuedDelivery interface {
	// ContinueQueued starts a run for the chat's undelivered message, if it
	// has one and no run is live; it reports whether it started one.
	ContinueQueued(ctx context.Context, chatID string) (bool, error)
	// Sweep delivers every message queued for a machine that is now
	// connected, and reports how many it started.
	Sweep(ctx context.Context) (int, error)
}

type queuedDeliveryHolder struct{ delivery QueuedDelivery }

// SetQueuedDelivery installs the deliverer. Unset, a wedged run is ended
// without being continued, and the queued-for-machine sweep does not run.
// Atomic for the same reason as SetBackgroundProcessDaemons: the server builds
// it after the poll loop has started.
func (r *Reconciler) SetQueuedDelivery(q QueuedDelivery) {
	if q == nil {
		r.queued.Store(nil)
		return
	}
	r.queued.Store(&queuedDeliveryHolder{delivery: q})
}

func (r *Reconciler) queuedDelivery() QueuedDelivery {
	if h := r.queued.Load(); h != nil {
		return h.delivery
	}
	return nil
}

// recoveredContinuingChatMessage is what the chat is told when the reconciler
// ended a run that could not go on — a wedge, or an end nothing reported — and
// a fresh run at its checkpoint is already answering the user's message.
const recoveredContinuingChatMessage = "Recovered from an internal error; continuing from your last message."

const (
	// autoContinueLimit runs per autoContinueWindow is how often the
	// reconciler continues one chat on its own. A run restarted at its
	// checkpoint runs current code on an empty history, so it does not wedge
	// the same way again — unless the workflow code itself panics at that
	// point, which would otherwise wedge, end and restart every two minutes
	// forever. Past the limit the chat is told to send a message instead.
	autoContinueLimit  = 2
	autoContinueWindow = 30 * time.Minute

	// continueTimeout is the continuation's own budget. It is detached from
	// the reconcile pass's 30s context, which it would otherwise share with
	// every other repair in the pass: ContinueQueued reads the ended run's
	// inputs with a query Temporal answers by replaying the whole history
	// (12s a task for chat 66a045ce's 10k events) before it starts the run.
	continueTimeout = 90 * time.Second
)

// continueAfterEnd hands a run's undelivered message — the user's "continue"
// that the run took in and never answered — to a fresh run at its checkpoint,
// right after the reconciler saw that run end (it ended a wedge, or found a
// run that ended without saying so), so the user does not have to send it
// again. It reports whether a run was started.
//
// It delivers at most once: ContinueQueued holds the chat's run-control lock,
// claims the message, and starts nothing unless the root run is FAILED in the
// database and closed in Temporal; the run it starts is recorded running
// before the lock is released. A chat with nothing owed starts nothing.
func (r *Reconciler) continueAfterEnd(ctx context.Context, wf *db.Workflow) bool {
	q := r.queuedDelivery()
	if q == nil {
		return false
	}
	if !r.autoContinueAllowed(wf.ChatID, time.Now()) {
		logging.Error("[Reconciler] Not continuing a chat again: it was already continued automatically and ended again; the user's next message continues it",
			"workflowID", wf.ID, "chatID", wf.ChatID,
			"limit", autoContinueLimit, "window", autoContinueWindow)
		return false
	}
	continueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), continueTimeout)
	defer cancel()
	started, err := q.ContinueQueued(continueCtx, wf.ChatID)
	if err != nil {
		logging.Warn("[Reconciler] Could not continue an ended run's undelivered message; the user's next message continues it",
			"workflowID", wf.ID, "chatID", wf.ChatID, "error", err)
		return false
	}
	if started {
		r.recordAutoContinue(wf.ChatID, time.Now())
		logging.Info("[Reconciler] Continued an ended run's undelivered message in a fresh run",
			"workflowID", wf.ID, "chatID", wf.ChatID)
	}
	return started
}

// autoContinueAllowed reports whether the chat has been continued
// automatically fewer than autoContinueLimit times in the last
// autoContinueWindow.
func (r *Reconciler) autoContinueAllowed(chatID string, now time.Time) bool {
	r.continueMu.Lock()
	defer r.continueMu.Unlock()
	recent := r.autoContinues[chatID][:0]
	for _, at := range r.autoContinues[chatID] {
		if now.Sub(at) < autoContinueWindow {
			recent = append(recent, at)
		}
	}
	if len(recent) == 0 {
		delete(r.autoContinues, chatID)
		return true
	}
	r.autoContinues[chatID] = recent
	return len(recent) < autoContinueLimit
}

func (r *Reconciler) recordAutoContinue(chatID string, at time.Time) {
	r.continueMu.Lock()
	defer r.continueMu.Unlock()
	r.autoContinues[chatID] = append(r.autoContinues[chatID], at)
}

// postRecoveredNote tells the chat that the run it was waiting on ended and a
// fresh one is answering its last message.
func (r *Reconciler) postRecoveredNote(ctx context.Context, wf *db.Workflow) {
	if _, err := r.repo.SaveMessageToThread(ctx, wf.ChatID, wf.Thread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), recoveredContinuingChatMessage, &wf.ID, nil, nil); err != nil {
		logging.Warn("[Reconciler] Failed to tell the chat its run was continued",
			"error", err,
			"workflowID", wf.ID,
		)
	}
}

// sweepQueuedDeliveries is the backstop for a machine connect whose event was
// missed: every message queued for a machine that is now connected gets its
// run.
func (r *Reconciler) sweepQueuedDeliveries(ctx context.Context) error {
	q := r.queuedDelivery()
	if q == nil {
		return nil
	}
	started, err := q.Sweep(ctx)
	if started > 0 {
		logging.Info("[Reconciler] Delivered messages queued for machines that are back", "started", started)
	}
	return err
}

// chatHoldsQueuedMessage reports whether a chat holds a message queued for its
// machine (activity QUEUED_FOR_MACHINE). Its mailbox is owed to the run that
// delivery starts, not to the orphaned-mailbox sweep.
func (r *Reconciler) chatHoldsQueuedMessage(ctx context.Context, chatID string) bool {
	chat, err := r.repo.GetChat(ctx, chatID)
	if err != nil || chat == nil || chat.Activity == nil {
		return false
	}
	return reliantv1.ChatActivity(*chat.Activity) == reliantv1.ChatActivity_CHAT_ACTIVITY_QUEUED_FOR_MACHINE
}
