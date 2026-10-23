// Copyright (c) 2025 Reliant Labs

// Package errreason is the vocabulary of reasons this server puts in
// x-forge-error-reason (svcerr.ReasonHeader) for the failures the UI branches
// on. One place, so a producer and a reviewer can see the whole contract.
//
// The web's copy is the closed union in frontends/web/src/lib/errorReasons.ts;
// the spelling here is the source of truth, and a value is never reused for a
// different meaning. A producer stamps a reason with svcerr.WithReason and
// leaves its message alone: clients from before the reason still match on the
// text until R3w2 (research/forge-migration/13-p9-frontend-plan.md §7).
package errreason

const (
	// DaemonStarting: a daemon record exists but has not registered yet
	// (toolexec.ErrDaemonPending). Unavailable; wait and retry.
	DaemonStarting = "daemon_starting"
	// DaemonSuspended: the machine is asleep (toolexec.ErrDaemonSuspended).
	// Unavailable; a message wakes it.
	DaemonSuspended = "daemon_suspended"
	// DaemonWaking: the server woke the machine for this request
	// (machinewake). Unavailable; retry shortly.
	DaemonWaking = "daemon_waking"
	// DaemonNotConnected: the machine's daemon has no responder on NATS —
	// closed or restarting. Unavailable.
	DaemonNotConnected = "daemon_not_connected"
	// NoDaemon: no machine exists for the request at all
	// (toolexec.ErrNoDaemon). FailedPrecondition; nothing to wait for.
	NoDaemon = "no_daemon"

	// ChatNotStarted: SendMessage on a chat whose root run has not started.
	// FailedPrecondition; the client calls StartChat.
	ChatNotStarted = "chat_not_started"
	// StaleRevision: a save raced a newer revision of the same definition.
	// Aborted; reload and retry.
	StaleRevision = "stale_revision"
	// ProcessNotRunning: killing a background process that already exited.
	// FailedPrecondition; the desired state already holds.
	ProcessNotRunning = "process_not_running"
	// ForgeAPIDisabled: the operator turned the forge rpc surface off
	// (RELIANT_DISABLE_FORGE_API). FailedPrecondition.
	ForgeAPIDisabled = "forge_api_disabled"

	// ScopeMissing is forge's accesstoken.ReasonScopeMissing: authenticated,
	// but the credential lacks a scope; the scopes ride
	// accesstoken.ScopesHeader. PermissionDenied.
	ScopeMissing = "scope_missing"
	// NoControlPlaneCredential: forge on the machine holds no usable
	// control-plane credential, or this server issues none. Signing in fixes
	// it, unless the server has no control plane at all.
	NoControlPlaneCredential = "no_control_plane_credential"
)
