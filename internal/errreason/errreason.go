// Copyright (c) 2025 Reliant Labs

// Package errreason is the vocabulary of reasons this server puts in
// x-forge-error-reason (svcerr.ReasonHeader) for the failures the UI branches
// on. One place, so a producer and a reviewer can see the whole contract.
//
// The web's copy is the closed union in frontends/web/src/lib/errorReasons.ts;
// the spelling here is the source of truth, and a value is never reused for a
// different meaning. A producer stamps a reason with svcerr.WithReason and
// leaves its message alone: desktop builds from before the reason still match
// on the text (research/forge-migration/13-p9-frontend-plan.md §7).
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
	// DaemonReconnecting: the machine registered a fresh stream but has not
	// answered on it yet (toolexec.ErrDaemonReconnecting). Unavailable; the
	// request was never sent, retry shortly.
	DaemonReconnecting = "daemon_reconnecting"
	// NoDaemon: no machine exists for the request at all
	// (toolexec.ErrNoDaemon). FailedPrecondition; nothing to wait for.
	NoDaemon = "no_daemon"
	// WorktreeRecovering: the machine refused new work in a worktree for a
	// moment, while it finalizes that worktree's recovery
	// (toolexec.ErrRecoveryFinalizing). Unavailable; nothing ran, retry
	// shortly. Not a connecting reason: the machine is up.
	WorktreeRecovering = "worktree_recovering"

	// ChatNotStarted: SendMessage on a chat whose root run has not started.
	// FailedPrecondition; the client calls StartChat.
	ChatNotStarted = "chat_not_started"
	// RunNoListeningAgent: input to a live run in which no agent is running
	// right now (a workflow between its agent steps). FailedPrecondition;
	// nothing was queued. SignalRun reports it as delivered=false with this
	// value in not_delivered_reason (and NotDeliveredHeader) instead.
	RunNoListeningAgent = "run_no_listening_agent"
	// RunRecipientAmbiguous: input to a run with several agents running in
	// parallel that names none of them. FailedPrecondition; the message lists
	// them, and the caller resends naming one (target thread).
	RunRecipientAmbiguous = "run_recipient_ambiguous"
	// StaleRevision: a save raced a newer revision of the same definition.
	// Aborted; reload and retry.
	StaleRevision = "stale_revision"
	// ProcessNotRunning: killing a background process that already exited.
	// FailedPrecondition; the desired state already holds.
	ProcessNotRunning = "process_not_running"
	// ForgeAPIDisabled: the operator turned the forge rpc surface off
	// (RELIANT_DISABLE_FORGE_API). FailedPrecondition.
	ForgeAPIDisabled = "forge_api_disabled"
	// FileTooLargeToDownload: DownloadFile on a file over its whole-file cap
	// (25 MB). FailedPrecondition; the message names the size and the cap.
	FileTooLargeToDownload = "file_too_large_to_download"
	// MachineBrowseUnsupported: ListMachineDirectory on a machine whose
	// daemon predates it. FailedPrecondition; updating Reliant on that
	// machine fixes it, and the Files tab says so instead of the machine view.
	MachineBrowseUnsupported = "machine_browse_unsupported"
	// NotADirectory: ListMachineDirectory on a path that is a file.
	// FailedPrecondition; the client opens the path as a file instead.
	NotADirectory = "not_a_directory"

	// ScopeMissing is forge's accesstoken.ReasonScopeMissing: authenticated,
	// but the credential lacks a scope; the scopes ride
	// accesstoken.ScopesHeader. PermissionDenied.
	ScopeMissing = "scope_missing"
	// NoControlPlaneCredential: forge on the machine holds no usable
	// control-plane credential, or this server issues none. Signing in fixes
	// it, unless the server has no control plane at all.
	NoControlPlaneCredential = "no_control_plane_credential"

	// UpgradeRequired: the client named in the Reliant-Client header is older
	// than the minimum this server supports, or names no version while the
	// server refuses unidentified clients (ClientVersionPolicy).
	// FailedPrecondition, with a google.rpc.ErrorInfo detail (reason
	// UPGRADE_REQUIRED, domain reliantlabs.io) whose metadata carries
	// client_kind, client_version, min_version, upgrade_url and
	// upgrade_command. Retrying cannot help; the client upgrades.
	UpgradeRequired = "upgrade_required"
)

// NotDeliveredHeader is the response header SignalRun sets, beside
// delivered=false, to say why a live run did not take the message: one of the
// reasons above (RunNoListeningAgent). Absent for a run that was not live.
//
// New readers use SignalRunResponse.not_delivered_reason, which carries the
// same value; the header is deprecated for them. SignalRun keeps setting it
// for clients that predate the field, and a reader falls back to it only
// against a server that sends no field.
const NotDeliveredHeader = "X-Reliant-Not-Delivered-Reason"
