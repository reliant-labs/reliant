// Copyright (c) 2025 Reliant Labs

// Package daemoncmd forwards a typed request to a registered command on the
// caller's daemon (e.g. "pkg.list_commands"). It serves the gRPC services that,
// on a cloud daemon, must run their work on the daemon's filesystem rather than
// the api-server's.
//
// Its reason to exist is UNIFORM, LOUD error mapping: every failure mode —
// daemon unreachable, dispatch timeout, command not registered, malformed
// response — becomes a Connect error, never a silently-empty success. A silent
// empty result is precisely the class of bug this was written to prevent
// (PackageCommandsService.ListCommands returning [] on a cloud daemon because
// discovery ran on the wrong filesystem).
//
// The existing FileSystem/Background/Terminal proxies predate it and still
// carry their own copies of getUserID/sendCommand; they are structured so they
// can adopt it later (tracked as a follow-up).
//
//forge:not-a-component: built by reliant's hand-wired handlers (the handlers under internal/handlers), not forge's composition root — until P3c (research/forge-migration/PLAN.md)
package daemoncmd

import "context"

// Service forwards typed requests to the caller's daemon.
type Service interface {
	// Dispatch marshals req, forwards it to the daemon as commandType, and
	// unmarshals the reply into resp. Any failure is mapped onto a loud Connect
	// error via daemonerr.DispatchConnectError; Dispatch never returns nil
	// while leaving resp unpopulated, so callers can rely on "no error => resp
	// is valid".
	Dispatch(ctx context.Context, userID, commandType string, req, resp any, timeoutMs int32) error
}

// Sender is the one daemon-router capability Dispatch needs.
type Sender interface {
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// Deps are the forwarder's collaborators.
type Deps struct {
	// Sender delivers a command to the user's daemon.
	Sender Sender
}
