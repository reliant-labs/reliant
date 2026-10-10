// Copyright (c) 2025 Reliant Labs
package reconciliation

import (
	"context"

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

// wedgeContinuedChatMessage is what the chat is told when the reconciler ended
// a wedged run and a fresh one is already answering the user's message.
const wedgeContinuedChatMessage = "This conversation's workflow was interrupted by an update. It has restarted from where it left off and is answering your message."

// continueAfterWedge hands a just-ended wedged run's undelivered message —
// the user's "continue" that the run took in and never answered — to a fresh
// run at its checkpoint, so the user does not have to send it again. It
// reports whether a run was started. ContinueQueued is idempotent and starts
// nothing for a chat with nothing owed.
func (r *Reconciler) continueAfterWedge(ctx context.Context, wf *db.Workflow) bool {
	q := r.queuedDelivery()
	if q == nil {
		return false
	}
	started, err := q.ContinueQueued(ctx, wf.ChatID)
	if err != nil {
		logging.Warn("[Reconciler] Could not continue a wedged run's undelivered message; the user's next message continues it",
			"workflowID", wf.ID, "chatID", wf.ChatID, "error", err)
		return false
	}
	if started {
		logging.Info("[Reconciler] Continued a wedged run's undelivered message in a fresh run",
			"workflowID", wf.ID, "chatID", wf.ChatID)
	}
	return started
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
