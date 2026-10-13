// Copyright (c) 2025 Reliant Labs
//
// Package mcpserver exposes a daemon as an MCP server, so third-party clients
// (ChatGPT, Claude, and their mobile apps) can run tools inside a user's cloud
// workspace.
//
// The value is specific to mobile: a phone cannot spawn a stdio MCP server, so
// a hosted daemon is the only way to give a phone-based assistant a real shell
// and filesystem.
//
// Two deliberate narrowings relative to the daemon's own surface:
//
//   - The daemon registers ~74 commands, most of them UI plumbing. This catalog
//     exposes a curated subset. Large tool lists measurably degrade model tool
//     selection, and commands like worktree.reconcile mean nothing to an
//     outside caller.
//   - Every call carries a policy that the daemon enforces at dispatch. This
//     package decides what to OFFER; it is not the security boundary. See
//     internal/daemonpolicy.
//
// This file declares the interfaces the package both consumes and implements:
// the interchangeable parts of waking a workspace (WorkspaceWaker,
// DaemonReadiness, DaemonResumer), recording and re-resolving a grant
// (AuditSink, GrantResolver) and limiting it (CallLimiter). Each has its
// production implementation here; tests substitute others, and
// controlplane.DaemonClient satisfies DaemonResumer directly. `forge generate`
// writes a mock of each into mock_gen.go. The interfaces the package only
// consumes (CommandSender, AttachmentReader, BindingStore,
// CredentialIntrospector, OAuthTokenValidator) stay beside their consumers.
//
//forge:not-a-component: built by reliant's hand-wired server (internal/grpc), not forge's composition root — until P3c (research/forge-migration/PLAN.md)
package mcpserver

import (
	"context"
	"time"
)

// WorkspaceWaker ensures a suspended workspace is running before a command is
// sent. A phone-initiated request is often the first traffic a workspace has
// seen in hours, so the common case is a cold one. *PollingWaker is the
// production implementation.
type WorkspaceWaker interface {
	// EnsureAwake blocks until the daemon is ready to accept commands, or
	// returns an error if it cannot be woken in time.
	EnsureAwake(ctx context.Context, userID, daemonID string) error
}

// DaemonReadiness reports and requests daemon availability. It is deliberately
// narrow so this package does not depend on the daemon registry or the control
// plane directly — in OSS mode there is nothing to resume, and Resume can be a
// no-op that simply reports the daemon is not routable. *AttachmentReadiness
// is the production implementation.
type DaemonReadiness interface {
	// IsReady reports whether the daemon is connected and can accept commands.
	IsReady(ctx context.Context, userID, daemonID string) (bool, error)

	// Resume asks the platform to wake a suspended daemon. It returns quickly;
	// readiness is then observed through IsReady rather than promised here.
	Resume(ctx context.Context, userID, daemonID string) error
}

// DaemonResumer asks the platform to wake a suspended daemon.
//
// Waking is a control-plane capability: in OSS mode there is no orchestrator
// to schedule a pod, so this is optional and its absence is reported honestly
// rather than papered over with a wait that could never succeed.
type DaemonResumer interface {
	ResumeDaemon(ctx context.Context, userID, daemonID string) error
}

// AuditSink records what a connector did. Recording is not optional in
// practice: when a third-party model runs commands in someone's workspace,
// "what did it actually do" is the first question asked, and it cannot be
// answered retroactively.
//
// Recording is two-phase because a call that is only recorded after it returns
// is lost precisely when the server dies mid-command — the case where the
// record matters most. Begin writes durably BEFORE dispatch; Complete resolves
// it. A record left unresolved is the signal that a command was issued and
// never accounted for.
type AuditSink interface {
	// Begin records an attempt and returns an id for Complete. An empty id
	// means the attempt could not be recorded; Complete then does nothing.
	Begin(ctx context.Context, entry AuditEntry) string

	// Complete resolves a previously begun record.
	Complete(ctx context.Context, id string, denied bool, errMsg string, duration time.Duration)
}

// GrantResolver re-reads a session's current grant.
//
// MCP sessions are long-lived and the SDK builds one server per session, so
// the Session captured when the connection opened would otherwise stay
// authoritative for its whole lifetime. Revocation is already immediate (the
// HTTP layer re-authenticates every request), but NARROWING a grant — removing
// a tool, tightening the path root — would not take effect until the client
// reconnected. Re-resolving per call closes that gap.
type GrantResolver interface {
	// Resolve returns the session's current policy and tool list, or an error
	// if the grant is no longer usable.
	Resolve(ctx context.Context, grantID string) (*Session, error)
}

// CallLimiter admits or refuses one tool call for a grant. *Limiter is the
// production implementation; a nil *Limiter admits every call.
type CallLimiter interface {
	// Acquire reserves capacity for one call. The returned release function
	// must be called when the call finishes; it is a no-op on error.
	Acquire(grantID string) (release func(), err error)
}
