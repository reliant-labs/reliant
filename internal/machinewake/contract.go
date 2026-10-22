// Copyright (c) 2025 Reliant Labs

// Package machinewake is the one step a signed-in user's own daemon-bound
// request takes when the machine it needs is asleep: the Files tab, file
// previews and file edits (internal/handlers/file_system), and the worktree
// operations (WorktreeService).
//
// It runs after the request has FAILED to reach its machine, never before, so a
// request to a connected machine pays nothing. "Not connected" comes from the
// transport (no NATS responder on the machine's subject, or resolution finding
// it suspended), not from an attachment timestamp, which can call a
// connected-but-idle machine offline.
//
// It does not wait for the machine to come up. A cold start takes tens of
// seconds, and holding a browser request open that long ties up a connection
// while showing nothing. It starts the wake and fails the request with a
// *wakingError, which the client sees as Unavailable with a DaemonWaking
// detail: the UI says "Waking up…" and retries on its usual cadence, and the
// retry succeeds once the machine is back. (internal/mcpserver's PollingWaker
// made the same choice for connector requests.)
//
// A run's tools never come through here: tool-time traffic does not wake a
// machine (#406). The run's preflight and the attended send do.
//
// Built by hand, by internal/app's apiMountOptions (providers_api.go), and the
// worktree and file_system handlers: never a node compose.go constructs from
// Deps.
//
//forge:provided
package machinewake

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Service is the wake step a daemon-bound handler takes after a request
// failed to reach its machine.
type Service interface {
	// AfterFailure is given the error a daemon-bound request failed with. When
	// err says the target machine is asleep and this request may wake it, it
	// wakes the machine and returns an error naming it (IsWaking reports it).
	// Otherwise it returns err unchanged.
	AfterFailure(ctx context.Context, userID string, target Target, err error) error
}

// Owners is what the wake step reads to check that the machine a request
// names belongs to the user making it.
type Owners interface {
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
}

// Resumer is the part of the daemon router that may resume a suspended
// machine. Only toolexec.NATSDaemonRouter has it; in OSS mode there is no
// control plane, nothing can be resumed, and the wake step does nothing.
type Resumer interface {
	Wake(ctx context.Context, userID string, selector *toolexec.DaemonSelector) (toolexec.WakeResult, error)
}

// Deps are the wake step's collaborators.
type Deps struct {
	// Resumer resumes the machine a request needs. Nil wakes nothing; pass
	// ResumerOf(router) for the router the handler sends on.
	Resumer Resumer
	// Owners resolves a named machine's owner. Nil wakes no named machine.
	Owners Owners
}

// Target is the machine a request needs.
type Target struct {
	// DaemonID is the machine the request was sent to, or "" when default
	// resolution picked it.
	DaemonID string
	// NoMachine is set when the request was made for a chat that has no
	// machine by design. Such a request never wakes anything.
	NoMachine bool
}
