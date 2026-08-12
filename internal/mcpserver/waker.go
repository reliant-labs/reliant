// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Wake behaviour.
//
// A connector request is often the first traffic a workspace has seen in
// hours, so the cold path is the common path, not the exception. Waking a
// suspended workspace means scheduling a pod and waiting for the daemon to
// dial back in — tens of seconds.
//
// This does NOT wait for that. An earlier design blocked through the cold
// start on the theory that one slow call beats several confusing ones; in
// practice it holds an MCP tool call open for the better part of a minute
// while the client shows a bare spinner with no explanation, and any
// intermediary with a shorter timeout turns it into an error anyway.
//
// So: start the wake, say what is happening, and return immediately. The
// caller retries. What makes this work rather than becoming a retry storm is
// that the three outcomes are distinguishable — a workspace that is starting
// says so and gives a time to come back; one that CANNOT start says that and
// does not suggest retrying at all.
const (
	// wakeRetryHint is how long to tell a caller to wait before retrying. It
	// is deliberately longer than a typical cold start, so the retry usually
	// succeeds rather than landing mid-boot and repeating the cycle.
	wakeRetryHint = 1 * time.Minute
)

// ErrWorkspaceStarting means a wake is underway and the caller should retry.
var ErrWorkspaceStarting = errors.New("workspace is starting")

// ErrWorkspaceUnavailable means the workspace is not running and cannot be
// started — a self-hosted machine that is offline, or a deployment with no
// orchestrator. Retrying will not help, and callers must not suggest it.
var ErrWorkspaceUnavailable = errors.New("workspace cannot be started")

// DaemonReadiness reports and requests daemon availability. It is deliberately
// narrow so this package does not depend on the daemon registry or the control
// plane directly — in OSS mode there is nothing to resume, and Resume can be a
// no-op that simply reports the daemon is not routable.
type DaemonReadiness interface {
	// IsReady reports whether the daemon is connected and can accept commands.
	IsReady(ctx context.Context, userID, daemonID string) (bool, error)

	// Resume asks the platform to wake a suspended daemon. It returns quickly;
	// readiness is then observed through IsReady rather than promised here.
	Resume(ctx context.Context, userID, daemonID string) error
}

// PollingWaker triggers a resume and reports what happened, without waiting
// for the workspace to finish starting.
//
// The name is historical: it no longer polls. It is kept because it is the
// exported constructor every deployment wires up, and renaming it would be
// churn for no behavioural gain.
type PollingWaker struct {
	readiness DaemonReadiness
	logger    *slog.Logger
}

// NewPollingWaker builds a waker over readiness.
func NewPollingWaker(readiness DaemonReadiness, logger *slog.Logger) *PollingWaker {
	if logger == nil {
		logger = slog.Default()
	}
	return &PollingWaker{readiness: readiness, logger: logger}
}

// EnsureAwake reports whether the daemon can accept commands right now, and
// starts it if it cannot.
//
// It returns nil when the command should proceed. Otherwise it returns an
// error wrapping either ErrWorkspaceStarting (a wake is underway — retry) or
// ErrWorkspaceUnavailable (nothing can start this — do not retry). It never
// waits for a starting workspace to become ready.
//
// The fast path — an already-running workspace — costs one readiness check and
// no resume, which matters because most calls in a live session take it.
func (w *PollingWaker) EnsureAwake(ctx context.Context, userID, daemonID string) error {
	if w == nil || w.readiness == nil {
		// No readiness source configured: assume reachable and let the command
		// itself report a routing failure. Better than refusing to try.
		return nil
	}
	if daemonID == "" {
		return fmt.Errorf("%w: no workspace is bound to this connector",
			ErrWorkspaceUnavailable)
	}

	ready, err := w.readiness.IsReady(ctx, userID, daemonID)
	if err != nil {
		// A readiness lookup failure should not, by itself, fail the call: the
		// command may well succeed. Attempt it and let the real error surface.
		w.logger.Warn("mcpserver: readiness check failed; attempting the command anyway",
			"daemonID", daemonID, "error", err)
		return nil
	}
	if ready {
		return nil
	}

	w.logger.Info("mcpserver: waking suspended workspace for connector request",
		"daemonID", daemonID)

	if err := w.readiness.Resume(ctx, userID, daemonID); err != nil {
		// Resume failing is the "cannot start" case: no orchestrator, a
		// self-hosted machine that is simply offline, or a refusal from the
		// platform. The wrapped text carries the specific reason.
		return fmt.Errorf("%w: %v", ErrWorkspaceUnavailable, err)
	}

	// The resume was accepted. The workspace is now booting, and this call
	// does not wait for it.
	return ErrWorkspaceStarting
}
