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
//     a message queued for the machine (chats.queued_for_machine_at). When the
//     machine connects, MachineConnected starts the run that delivers it
//     (ChatService.ContinueQueued); Sweep does the same for any connect whose
//     event was missed.
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

	// redeliveries holds back a chat whose delivered run queued its message
	// again (see redeliveryBackoff); now is its clock.
	redeliveries *redeliveryBackoff
	now          func() time.Time
}

// New builds the service.
func New(deps Deps) Service {
	return &service{
		store: deps.Store, machines: deps.Machines, deliverer: deps.Deliverer, signaler: deps.Signaler,
		redeliveries: newRedeliveryBackoff(), now: time.Now,
	}
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
	queued, err := s.store.ListChatsQueuedForMachine(ctx, userID, perUserLimit)
	if err != nil {
		logging.WarnContext(ctx, "[QueuedDelivery] Could not list the user's queued messages", "userID", userID, "error", err)
		return n
	}
	return n + s.deliver(ctx, queued)
}

// Sweep implements Service.
func (s *service) Sweep(ctx context.Context) (int, error) {
	n := 0
	paused, pausedErr := s.store.ListChatsPausedForMachine(ctx, "", sweepBatch)
	if pausedErr == nil {
		n += s.resumePaused(ctx, paused)
	}
	queued, err := s.store.ListChatsQueuedForMachine(ctx, "", sweepBatch)
	if err != nil {
		return n, fmt.Errorf("list chats queued for their machine: %w", err)
	}
	n += s.deliver(ctx, queued)
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

// resumePaused resumes each chat the offline breaker paused whose machine is
// connected. One whose machine is still down stays paused for its own connect.
func (s *service) resumePaused(ctx context.Context, paused []db.PausedForMachineChat) int {
	resumed := 0
	for _, chat := range paused {
		var selector *toolexec.DaemonSelector
		if chat.ActiveDaemonID != nil && *chat.ActiveDaemonID != "" {
			selector = &toolexec.DaemonSelector{ID: *chat.ActiveDaemonID}
		}
		online, err := s.machines.IsDaemonOnline(ctx, chat.UserID, selector)
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
func (s *service) deliver(ctx context.Context, queued []db.QueuedForMachineChat) int {
	started := 0
	now := s.now()
	for _, chat := range queued {
		online, err := s.machines.IsDaemonOnline(ctx, chat.UserID, queuedSelector(chat))
		if err != nil || !online {
			continue
		}
		if wait, held := s.redeliveries.hold(chat.ChatID, now); held {
			logging.DebugContext(ctx, "[QueuedDelivery] The run delivered for this chat queued it again; holding the next delivery",
				"chatID", chat.ChatID, "retryIn", wait.Round(time.Second))
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
			s.redeliveries.delivered(chat.ChatID, now)
			logging.InfoContext(ctx, "[QueuedDelivery] The chat's machine is back; delivering the message queued for it",
				"chatID", chat.ChatID, "userID", chat.UserID, "queuedFor", time.Since(chat.QueuedAt).Round(time.Second))
		}
	}
	return started
}

// queuedSelector is the machine a queued chat waits for: the one its run
// waited for when it queued the message, else the chat's pinned machine, else
// (nil) any of the user's. Asking about any other machine than the run's own
// is how one chat whose workflow named a machine that never connected was
// delivered, failed its preflight and re-queued every few seconds for days.
func queuedSelector(chat db.QueuedForMachineChat) *toolexec.DaemonSelector {
	if m := chat.Machine; m != nil {
		return &toolexec.DaemonSelector{ID: m.ID, Name: m.Name, Type: m.Type, Labels: m.Labels}
	}
	if chat.ActiveDaemonID != nil && *chat.ActiveDaemonID != "" {
		return &toolexec.DaemonSelector{ID: *chat.ActiveDaemonID}
	}
	return nil
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
