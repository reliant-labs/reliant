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

// Store is the chat state this package reads.
type Store interface {
	ListChatsQueuedForMachine(ctx context.Context, userID string, limit int) ([]db.QueuedForMachineChat, error)
	ListChatsWaitingForMachine(ctx context.Context, userID string) ([]db.WaitingForMachineRun, error)
}

// Machines answers whether the machine a chat runs on is connected.
type Machines interface {
	IsDaemonOnline(ctx context.Context, userID string, selector *toolexec.DaemonSelector) (bool, error)
}

// Deliverer starts the run that delivers a chat's undelivered message, and
// reports whether it started one (ChatService.ContinueQueued).
type Deliverer interface {
	ContinueQueued(ctx context.Context, chatID string) (bool, error)
}

// Signaler signals a workflow (the Temporal client).
type Signaler interface {
	SignalWorkflow(ctx context.Context, workflowID, runID, signalName string, arg interface{}) error
}

// Service delivers messages queued for a machine. See the package doc.
type Service struct {
	store     Store
	machines  Machines
	deliverer Deliverer
	signaler  Signaler
}

// New builds the service.
func New(store Store, machines Machines, deliverer Deliverer, signaler Signaler) *Service {
	return &Service{store: store, machines: machines, deliverer: deliverer, signaler: signaler}
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

// MachineConnected handles one of userID's machines connecting: it wakes every
// run of theirs parked waiting for a machine, and starts the run for every
// message of theirs queued for a machine that is now connected. It returns the
// number of messages it started delivering.
func (s *Service) MachineConnected(ctx context.Context, userID string) int {
	s.wakeWaitingRuns(ctx, userID)
	queued, err := s.store.ListChatsQueuedForMachine(ctx, userID, perUserLimit)
	if err != nil {
		logging.Warn("[QueuedDelivery] Could not list the user's queued messages", "userID", userID, "error", err)
		return 0
	}
	return s.deliver(ctx, queued)
}

// Sweep starts the run for every message queued for a machine that is now
// connected — the backstop for a connect whose event was missed (a replica
// restarting, the stream unavailable). It returns how many it started.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	queued, err := s.store.ListChatsQueuedForMachine(ctx, "", sweepBatch)
	if err != nil {
		return 0, fmt.Errorf("list chats queued for their machine: %w", err)
	}
	return s.deliver(ctx, queued), nil
}

// ContinueQueued starts the run that delivers a chat's undelivered message;
// the reconciler calls it for a run it ended as wedged.
func (s *Service) ContinueQueued(ctx context.Context, chatID string) (bool, error) {
	return s.deliverer.ContinueQueued(ctx, chatID)
}

// wakeWaitingRuns signals every run of the user's that is parked waiting for a
// machine, so it checks now rather than at its next recheck. A run waiting for
// a different machine checks, finds it still down, and goes back to waiting.
func (s *Service) wakeWaitingRuns(ctx context.Context, userID string) {
	waiting, err := s.store.ListChatsWaitingForMachine(ctx, userID)
	if err != nil {
		logging.Warn("[QueuedDelivery] Could not list the user's runs waiting for a machine", "userID", userID, "error", err)
		return
	}
	for _, run := range waiting {
		if err := s.signaler.SignalWorkflow(ctx, run.WorkflowID, "", machinewait.SignalName, machinewait.Signal{
			Reason: machinewait.ReasonMachineConnected,
		}); err != nil {
			logging.Warn("[QueuedDelivery] Could not tell a waiting run its user's machine connected",
				"chatID", run.ChatID, "workflowID", run.WorkflowID, "error", err)
		}
	}
}

// deliver starts the run for each queued chat whose machine is connected.
// One whose machine is still down stays queued for its own connect.
func (s *Service) deliver(ctx context.Context, queued []db.QueuedForMachineChat) int {
	started := 0
	for _, chat := range queued {
		var selector *toolexec.DaemonSelector
		if chat.ActiveDaemonID != nil && *chat.ActiveDaemonID != "" {
			selector = &toolexec.DaemonSelector{ID: *chat.ActiveDaemonID}
		}
		online, err := s.machines.IsDaemonOnline(ctx, chat.UserID, selector)
		if err != nil || !online {
			continue
		}
		ok, err := s.deliverer.ContinueQueued(ctx, chat.ChatID)
		if err != nil {
			logging.Warn("[QueuedDelivery] Could not deliver a message queued for its machine; it stays queued",
				"chatID", chat.ChatID, "error", err)
			continue
		}
		if ok {
			started++
			logging.Info("[QueuedDelivery] The chat's machine is back; delivering the message queued for it",
				"chatID", chat.ChatID, "userID", chat.UserID, "queuedFor", time.Since(chat.QueuedAt).Round(time.Second))
		}
	}
	return started
}

// Run consumes machine connects from the DAEMON_EVENTS stream until ctx ends.
// The daemon-gateway owns the stream; if it does not exist yet this returns
// and the sweep alone delivers until the next api-server start.
func (s *Service) Run(ctx context.Context, nc *nats.Conn) error {
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
			logging.Warn("[QueuedDelivery] DAEMON_EVENTS stream does not exist yet; delivering from the sweep only")
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
	logging.Info("[QueuedDelivery] Listening for machine connects",
		"stream", daemonevents.StreamDaemonEvents, "subject", daemonevents.SubjectConnected, "durable", consumerName)
	<-ctx.Done()
	return nil
}
