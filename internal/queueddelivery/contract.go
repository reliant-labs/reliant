// Copyright (c) 2025 Reliant Labs

package queueddelivery

import (
	"context"

	"github.com/nats-io/nats.go"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Service delivers messages queued for a machine. See the package doc.
type Service interface {
	// MachineConnected handles one of userID's machines connecting: it wakes
	// every run of theirs parked waiting for a machine, resumes every chat of
	// theirs the offline breaker paused, and starts the run for every message
	// of theirs queued for a machine that is now connected. It returns the
	// number of chats it resumed or started delivering to.
	MachineConnected(ctx context.Context, userID string) int
	// Sweep resumes every chat the offline breaker paused, and starts the run
	// for every message queued for a machine, whose machine is now connected —
	// the backstop for a connect whose event was missed (a replica
	// restarting, the stream unavailable). It returns how many it resumed or
	// started.
	Sweep(ctx context.Context) (int, error)
	// ContinueQueued starts the run that delivers a chat's undelivered
	// message; the reconciler calls it for a run it ended as wedged.
	ContinueQueued(ctx context.Context, chatID string) (bool, error)
	// Run consumes machine connects from the DAEMON_EVENTS stream until ctx
	// ends. The daemon-gateway owns the stream; if it does not exist yet this
	// returns and the sweep alone delivers until the next api-server start.
	Run(ctx context.Context, nc *nats.Conn) error
}

// Store is the chat state this package reads.
type Store interface {
	ListChatsQueuedForMachine(ctx context.Context, userID string, limit int) ([]db.QueuedForMachineChat, error)
	ListChatsWaitingForMachine(ctx context.Context, userID string) ([]db.WaitingForMachineRun, error)
	ListChatsPausedForMachine(ctx context.Context, userID string, limit int) ([]db.PausedForMachineChat, error)
}

// Machines answers whether the machine a chat runs on is connected.
type Machines interface {
	IsDaemonOnline(ctx context.Context, userID string, selector *toolexec.DaemonSelector) (bool, error)
}

// Deliverer starts the run that delivers a chat's undelivered message, and
// reports whether it started one (ChatService.ContinueQueued).
type Deliverer interface {
	ContinueQueued(ctx context.Context, chatID string) (bool, error)
	// ResumePausedForMachine resumes a chat the daemon-offline breaker paused,
	// and reports whether it did (ChatService.ResumePausedForMachine).
	ResumePausedForMachine(ctx context.Context, chatID string) (bool, error)
}

// Signaler signals a workflow (the Temporal client).
type Signaler interface {
	SignalWorkflow(ctx context.Context, workflowID, runID, signalName string, arg interface{}) error
}

// Deps are the service's collaborators.
type Deps struct {
	// Store lists the chats waiting on, paused for, or queued for a machine.
	Store Store
	// Machines reports whether a chat's machine is connected.
	Machines Machines
	// Deliverer resumes and continues chats (the ChatService).
	Deliverer Deliverer
	// Signaler wakes runs parked waiting for a machine (the Temporal client).
	Signaler Signaler
}
