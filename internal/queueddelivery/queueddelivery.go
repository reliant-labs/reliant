// Copyright (c) 2025 Reliant Labs

// Package queueddelivery delivers messages that were sent while the chat's
// machine was down, once the machine is back.
//
// A message sent while the machine is starting is held by its run, which waits
// in preflight (runtime.waitForMachine). Two things that wait cannot do on its
// own, and this package does for it, in the api-server:
//
//   - Wake it. The waiting run sleeps on a timer between checks; when one of
//     the user's machines connects, MachineConnected signals every run of
//     theirs that is parked waiting (machinewait), so it checks at once.
//   - Resume it. A run that was connected but lost its machine mid-turn is
//     paused by the daemon-offline circuit breaker after two minutes of failed
//     tool calls, and stays parked until something resumes it. The breaker
//     marks its pause (chats.paused_for_machine_at); when the machine
//     connects, MachineConnected resumes every chat so marked, exactly as the
//     user's own message would. A chat the user paused carries no mark and is
//     never touched.
//   - Outlive it. A run whose machine failed to start, was removed, or was
//     still down after hours ends, and its preflight marks the chat as holding
//     a message queued for the machine (chats.queued_for_machine_at), and
//     records WHICH machine it waited on (chat_queued_deliveries): the
//     workflow's pinned machine, which is not always the chat's own. When
//     that machine connects, MachineConnected starts the run that delivers
//     the message (ChatService.ContinueQueued); Sweep does the same for any
//     connect whose event was missed.
//
// Delivery is bounded. A run it starts can fail at the same step again (the
// machine was reported back, but the run's preflight still cannot reach it),
// which queues the message again; unbounded, that is a loop, and one ran
// every 30s for 27 hours (research/pr-review-failure-storm-2026-10-29.md).
// So each run delivery starts is counted, successive ones are spaced by a
// doubling backoff, and after core.MaxQueuedDeliveryAttempts the message stops
// being retried on its own: the chat tells its owner, and the message is
// tried once more only when the machine it waits for has connected afresh
// since (checked on a connect, and by the sweep once the hold after the
// give-up has passed) or the owner sends a message. A run that gets past
// preflight ends the count.
//
// Connects arrive on the DAEMON_EVENTS JetStream stream through one durable
// consumer, so each is handled by one api-server replica. Delivery itself is
// idempotent (ContinueQueued claims the message under the chat's run-control
// lock), so a connect handled twice, or raced by the sweep, still delivers
// once.
//
// forge:outbound-io
//
// Run is a NATS JetStream consumer: it creates or updates the durable consumer
// on DAEMON_EVENTS and consumes its connect events.
//
// Built by hand, by internal/app's NewQueuedDeliverer provider
// (providers_api.go): never a node compose.go constructs from Deps.
//
//forge:provided
package queueddelivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/reliant-labs/reliant/internal/daemonevents"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/machinewait"
)

// service delivers messages queued for a machine. See the package doc.
type service struct {
	store     Store
	machines  Machines
	deliverer Deliverer
	signaler  Signaler
	now       func() time.Time
}

// New builds the service.
func New(deps Deps) Service {
	return &service{store: deps.Store, machines: deps.Machines, deliverer: deps.Deliverer, signaler: deps.Signaler, now: time.Now}
}

const (
	// sweepBatch bounds one sweep: queued chats are rare (each is a machine
	// that failed or vanished), and a backlog drains over a few passes rather
	// than in one long one.
	sweepBatch = 200
	// perUserLimit bounds one connect's deliveries for a single user.
	perUserLimit = 100
	// handleTimeout bounds handling one connect event.
	handleTimeout = 2 * time.Minute

	// consumerName is the durable consumer every api-server replica shares,
	// so each connect is handled once.
	consumerName = "api-server-queued-delivery"
)

// MachineConnected implements Service.
func (s *service) MachineConnected(ctx context.Context, userID string) int {
	s.wakeWaitingRuns(ctx, userID)
	n := 0
	paused, err := s.store.ListChatsPausedForMachine(ctx, userID, perUserLimit)
	if err != nil {
		logging.WarnContext(ctx, "[QueuedDelivery] Could not list the user's chats paused for their machine", "userID", userID, "error", err)
	} else {
		n += s.resumePaused(ctx, paused)
	}
	queued, err := s.store.ListChatsQueuedForMachine(ctx, userID, perUserLimit, true)
	if err != nil {
		logging.WarnContext(ctx, "[QueuedDelivery] Could not list the user's queued messages", "userID", userID, "error", err)
		return n
	}
	return n + s.deliver(ctx, queued, true)
}

// Sweep implements Service.
func (s *service) Sweep(ctx context.Context) (int, error) {
	n := 0
	paused, pausedErr := s.store.ListChatsPausedForMachine(ctx, "", sweepBatch)
	if pausedErr == nil {
		n += s.resumePaused(ctx, paused)
	}
	queued, err := s.store.ListChatsQueuedForMachine(ctx, "", sweepBatch, true)
	if err != nil {
		return n, fmt.Errorf("list chats queued for their machine: %w", err)
	}
	n += s.deliver(ctx, queued, false)
	if pausedErr != nil {
		return n, fmt.Errorf("list chats paused for their machine: %w", pausedErr)
	}
	return n, nil
}

// ContinueQueued implements Service.
func (s *service) ContinueQueued(ctx context.Context, chatID string) (bool, error) {
	return s.deliverer.ContinueQueued(ctx, chatID)
}

// wakeWaitingRuns signals every run of the user's that is parked waiting for a
// machine, so it checks now rather than at its next recheck. A run waiting for
// a different machine checks, finds it still down, and goes back to waiting.
func (s *service) wakeWaitingRuns(ctx context.Context, userID string) {
	waiting, err := s.store.ListChatsWaitingForMachine(ctx, userID)
	if err != nil {
		logging.WarnContext(ctx, "[QueuedDelivery] Could not list the user's runs waiting for a machine", "userID", userID, "error", err)
		return
	}
	for _, run := range waiting {
		if err := s.signaler.SignalWorkflow(ctx, run.WorkflowID, "", machinewait.SignalName, machinewait.Signal{
			Reason: machinewait.ReasonMachineConnected,
		}); err != nil {
			logging.WarnContext(ctx, "[QueuedDelivery] Could not tell a waiting run its user's machine connected",
				"chatID", run.ChatID, "workflowID", run.WorkflowID, "error", err)
		}
	}
}

// selectorFor is the machine a chat's queued or paused work waits for: the one
// the run waited on when it recorded it, else the chat's own machine, else the
// user's default (nil).
func selectorFor(target db.QueuedMachineTarget, activeDaemonID *string) *toolexec.DaemonSelector {
	if !target.IsZero() {
		return &toolexec.DaemonSelector{ID: target.DaemonID, Name: target.DaemonName, Type: target.DaemonType, Labels: target.Labels}
	}
	if activeDaemonID != nil && *activeDaemonID != "" {
		return &toolexec.DaemonSelector{ID: *activeDaemonID}
	}
	return nil
}

// resumePaused resumes each chat the offline breaker paused whose machine is
// connected. One whose machine is still down stays paused for its own connect.
func (s *service) resumePaused(ctx context.Context, paused []db.PausedForMachineChat) int {
	resumed := 0
	for _, chat := range paused {
		online, err := s.machines.IsDaemonOnline(ctx, chat.UserID, selectorFor(db.QueuedMachineTarget{}, chat.ActiveDaemonID))
		if err != nil || !online {
			continue
		}
		ok, err := s.deliverer.ResumePausedForMachine(ctx, chat.ChatID)
		if err != nil {
			logging.WarnContext(ctx, "[QueuedDelivery] Could not resume a chat paused for its machine; it stays paused",
				"chatID", chat.ChatID, "error", err)
			continue
		}
		if ok {
			resumed++
			logging.InfoContext(ctx, "[QueuedDelivery] The chat's machine is back; resumed the run the offline breaker paused",
				"chatID", chat.ChatID, "userID", chat.UserID, "pausedFor", time.Since(chat.PausedAt).Round(time.Second))
		}
	}
	return resumed
}

// deliver starts the run for each queued chat whose machine is connected.
// One whose machine is still down stays queued for its own connect.
//
// "Its machine" is the one the run that queued the message waited on, not the
// chat's own: a run restarted because the chat's machine was up, but waiting on
// a workflow-pinned machine that is down, would only fail at the same step and
// queue the message again.
//
// connect is true when a machine of the user's just connected rather than the
// sweep running: that event skips the backoff of a message still being retried
// (the machine being back is what the backoff was waiting for). The attempt
// limit still bounds a machine that connects but cannot be reached. A message
// delivery stopped on is not touched by either until its hold has passed and
// its machine has connected since (see the GaveUpAt branch).
func (s *service) deliver(ctx context.Context, queued []db.QueuedForMachineChat, connect bool) int {
	started := 0
	for _, chat := range queued {
		online, err := s.machines.IsDaemonOnline(ctx, chat.UserID, selectorFor(chat.Target, chat.ActiveDaemonID))
		if err != nil || !online {
			continue
		}
		now := s.now()
		backedOff := chat.NextAttemptAt != nil && now.Before(*chat.NextAttemptAt)
		if chat.GaveUpAt != nil {
			// Delivery stopped retrying this message. It tries once more when
			// the machine it waits for has connected AFTER it gave up (a new
			// session, which is news) and the hold has passed — whether that
			// connect came inside the hold (the sweep notices once the hold
			// expires) or later. Without a connect since, the machine is the
			// same one that already failed us, so it stays stopped. Either
			// way a machine that connects and drops again buys one try per
			// hold, not one per reconnect.
			if backedOff || !s.connectedSince(ctx, chat, *chat.GaveUpAt) {
				continue
			}
			revived, err := s.store.ReviveQueuedDelivery(ctx, chat.ChatID)
			if err != nil || !revived {
				continue
			}
			chat.Attempts, chat.NextAttemptAt = core.MaxQueuedDeliveryAttempts-1, nil
		} else if backedOff && !connect {
			// A connect is news the backoff did not know; the sweep is not.
			continue
		}
		if chat.Attempts >= core.MaxQueuedDeliveryAttempts {
			s.giveUp(ctx, chat)
			continue
		}
		// Counted before the run starts: the count is what bounds the loop, so
		// it must survive a crash between the start and the write, and a start
		// that fails below is simply retried after its backoff.
		next := now.Add(core.QueuedDeliveryBackoff(chat.Attempts + 1))
		if err := s.store.RecordQueuedDeliveryAttempt(ctx, chat.ChatID, next); err != nil {
			logging.WarnContext(ctx, "[QueuedDelivery] Could not count a delivery attempt; not delivering, so the count cannot be lost",
				"chatID", chat.ChatID, "error", err)
			continue
		}
		ok, err := s.deliverer.ContinueQueued(ctx, chat.ChatID)
		if err != nil {
			logging.WarnContext(ctx, "[QueuedDelivery] Could not deliver a message queued for its machine; it stays queued",
				"chatID", chat.ChatID, "error", err)
			continue
		}
		if ok {
			started++
			logging.InfoContext(ctx, "[QueuedDelivery] The chat's machine is back; delivering the message queued for it",
				"chatID", chat.ChatID, "userID", chat.UserID, "queuedFor", time.Since(chat.QueuedAt).Round(time.Second),
				"attempt", chat.Attempts+1, "of", core.MaxQueuedDeliveryAttempts)
		}
	}
	return started
}

// connectedSince reports whether the machine the chat's message waits for began
// a stream session after t.
func (s *service) connectedSince(ctx context.Context, chat db.QueuedForMachineChat, t time.Time) bool {
	since, ok, err := s.machines.AttachedSince(ctx, chat.UserID, selectorFor(chat.Target, chat.ActiveDaemonID))
	return err == nil && ok && since.After(t)
}

// giveUp stops retrying a message whose delivery runs keep ending the same way,
// and tells the chat's owner. The queued marker is cleared (the chat reads as a
// failed run, not as waiting) and the record is parked; the machine it waits
// for connecting afresh — or the owner sending a message — starts delivery
// again.
func (s *service) giveUp(ctx context.Context, chat db.QueuedForMachineChat) {
	stopped, err := s.store.GiveUpQueuedDelivery(ctx, chat.ChatID)
	if err != nil {
		logging.WarnContext(ctx, "[QueuedDelivery] Could not stop retrying a queued message", "chatID", chat.ChatID, "error", err)
		return
	}
	if !stopped {
		return
	}
	logging.WarnContext(ctx, "[QueuedDelivery] Delivery of a queued message kept failing; no longer retrying it",
		"chatID", chat.ChatID, "userID", chat.UserID, "attempts", chat.Attempts, "since", chat.Since, "lastFailure", chat.LastFailure)
	if err := s.deliverer.NotifyQueuedDeliveryStopped(ctx, chat); err != nil {
		logging.WarnContext(ctx, "[QueuedDelivery] Could not tell the chat its queued message is no longer retried", "chatID", chat.ChatID, "error", err)
	}
}

// Run implements Service.
func (s *service) Run(ctx context.Context, nc *nats.Conn) error {
	if nc == nil {
		return errors.New("queued delivery: nil NATS connection")
	}
	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("create jetstream context: %w", err)
	}
	stream, err := js.Stream(ctx, daemonevents.StreamDaemonEvents)
	if err != nil {
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			logging.WarnContext(ctx, "[QueuedDelivery] DAEMON_EVENTS stream does not exist yet; delivering from the sweep only")
			return nil
		}
		return fmt.Errorf("look up %s stream: %w", daemonevents.StreamDaemonEvents, err)
	}
	consumer, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       consumerName,
		FilterSubject: daemonevents.SubjectConnected,
		// A connect from before this consumer existed has nothing for it:
		// whatever it would have delivered, the sweep delivers.
		DeliverPolicy: jetstream.DeliverNewPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
		// Longer than a connect can take to handle, so a slow one is not
		// redelivered to another replica while it is still being handled.
		AckWait: handleTimeout + 30*time.Second,
	})
	if err != nil {
		return fmt.Errorf("create/update %s consumer: %w", consumerName, err)
	}
	cc, err := consumer.Consume(func(msg jetstream.Msg) {
		// Acked whatever happens: a connect that could not be handled is
		// the sweep's, and redelivering it would only repeat the failure.
		defer func() { _ = msg.Ack() }()
		var evt daemonevents.Event
		if err := json.Unmarshal(msg.Data(), &evt); err != nil || evt.UserID == "" {
			return
		}
		handleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), handleTimeout)
		defer cancel()
		s.MachineConnected(handleCtx, evt.UserID)
	})
	if err != nil {
		return fmt.Errorf("consume %s: %w", daemonevents.SubjectConnected, err)
	}
	defer cc.Stop()
	logging.InfoContext(ctx, "[QueuedDelivery] Listening for machine connects",
		"stream", daemonevents.StreamDaemonEvents, "subject", daemonevents.SubjectConnected, "durable", consumerName)
	<-ctx.Done()
	return nil
}
