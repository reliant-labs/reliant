// Copyright (c) 2025 Reliant Labs

// Package capacitywake defines the chat-wide capacity wake: the signal sent to
// a chat's run when its provider may have capacity again, so that every
// thread blocked on the provider re-routes and goes on. Without it, each
// blocked thread would wait for its own reset or for a message. It also holds
// the one fan-out that sends the wake to every chat of a user.
//
// It is a leaf package for the same reason threadwake is. The receiver is
// internal/workflow/runtime. The senders are the chat handlers, the account
// handlers, and the usage poller that runs on the worker, and they sit on both
// sides of runtime's imports. With one definition, every sender and the
// receiver agree on the name and payload. A signal sent to a name nothing
// listens on is not an error. It is a wake that silently never lands.
package capacitywake

import (
	"context"

	"github.com/reliant-labs/reliant/internal/logging"
)

// SignalName is the capacity wake's Temporal signal. The wire name predates
// the broader meaning: it began as "Retry now" on a usage-limit wait. It stays
// because the histories of runs in flight carry it.
const SignalName = "signal.usage_limit.retry"

// Signal is the payload of SignalName. A zero value is a plain wake.
type Signal struct {
	// UseModel answers a usage-limit wait with a stay on another provider,
	// "<model>@<driver>", until the wait's own end. It is set for "Use <model>
	// now", and when the user switches the chat to another provider. It does
	// not change the chat's policy.
	UseModel string `json:"use_model,omitempty"`
	// AllStopped resumes every sub-agent that stopped on a failure, not only
	// those a provider stopped. It is set when the user pressed Resume on the
	// chat, which asks all of the chat to go on. Account and provider changes
	// leave it unset: they fix capacity, not a bad request.
	AllStopped bool `json:"all_stopped,omitempty"`
}

// TargetLister lists the root runs of a user's chats that a provider may have
// blocked. *db.Repo satisfies it (ListCapacityWakeTargets).
type TargetLister interface {
	ListCapacityWakeTargets(ctx context.Context, userID string) ([]string, error)
}

// Signaler sends one Temporal signal. A Temporal client satisfies it.
type Signaler interface {
	SignalWorkflow(ctx context.Context, workflowID, runID, signalName string, arg interface{}) error
}

// WakeUser sends a plain capacity wake to every chat of userID whose run may
// have a thread blocked on a provider, and reports how many it reached. It is
// called when the user's providers gain capacity: an account added, re-authed
// or un-paused, or a usage reading that lifts an account's limit.
//
// Each chat is best-effort. A run that cannot be signalled (it ended
// meanwhile) is logged and skipped. A run with nothing blocked treats the
// wake as a no-op. why is for the log.
func WakeUser(ctx context.Context, targets TargetLister, signals Signaler, userID, why string) int {
	if targets == nil || signals == nil || userID == "" {
		return 0
	}
	ids, err := targets.ListCapacityWakeTargets(ctx, userID)
	if err != nil {
		logging.WarnContext(ctx, "Could not list the chats to wake after a provider change", "userID", userID, "why", why, "error", err)
		return 0
	}
	woken := 0
	for _, workflowID := range ids {
		if err := signals.SignalWorkflow(ctx, workflowID, "", SignalName, Signal{}); err != nil {
			logging.InfoContext(ctx, "Could not wake a chat's run after a provider change", "workflowID", workflowID, "why", why, "error", err)
			continue
		}
		woken++
	}
	if len(ids) > 0 {
		logging.InfoContext(ctx, "Woke the user's chats blocked on a provider", "userID", userID, "why", why, "woken", woken, "candidates", len(ids))
	}
	return woken
}
