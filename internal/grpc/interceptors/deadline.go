// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"maps"
	"time"

	"github.com/reliant-labs/forge/pkg/serverkit"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
)

// reliant's RPC budgets, for the Deadline slot of its chains
// (serverkit.Deadline). The slot sits outside authentication, so an `rlat_`
// introspection or a token-store query runs inside the RPC's budget, and a
// budget is a ceiling: a caller's shorter deadline still applies, a longer one
// does not lift it.

// UnaryTimeout bounds a unary RPC that ProcedureTimeouts does not name.
// It is 10s because browsers limit HTTP/1.1 to ~6 concurrent connections per
// origin, and browsers don't support h2c (HTTP/2 cleartext), so local dev is
// on HTTP/1.1: short budgets keep a wedged call from starving the rest.
const UnaryTimeout = 10 * time.Second

const worktreeOperationTimeout = 120 * time.Second

// ProcedureTimeouts is the per-procedure budget table (serverkit
// Deadline.Procedures): the methods that need longer than UnaryTimeout, and
// those that wait on a person (0, no server deadline).
//
// owned adds budgets owned by another package. A service whose handlers
// dispatch work with their own budget (the forge daemon commands run 15–110s)
// must also be granted that budget HERE, or UnaryTimeout cancels the request
// first and the handler's own budget is dead code. Taking the map from the
// owning package — rather than copying numbers into the table below — keeps
// the two from drifting.
//
// Every key must name an existing rpc — serverkit checks only a key's shape,
// so a renamed rpc silently falls back to UnaryTimeout — and, since an entry
// bounds a streaming rpc too, a unary one
// (TestUserChainDeadlineTableNamesUnaryRPCs).
func ProcedureTimeouts(owned ...map[string]time.Duration) map[string]time.Duration {
	timeouts := map[string]time.Duration{
		// Chat operations that involve workflows - initial setup can take time
		"/reliant.v1.ChatService/StartChat":   30 * time.Second,
		"/reliant.v1.ChatService/SendMessage": 30 * time.Second,
		// Provider API key validation - Gemini requires minimum 10s deadline
		"/reliant.v1.SettingsService/ValidateProviderAPIKey":   20 * time.Second,
		"/reliant.v1.ProviderAccountService/AddProviderAPIKey": 20 * time.Second,
		// Worktree operations can involve git worktree add/remove, diffing, and file copying.
		"/reliant.v1.WorktreeService/CreateWorktree":           worktreeOperationTimeout,
		"/reliant.v1.WorktreeService/DeleteWorktree":           worktreeOperationTimeout,
		"/reliant.v1.WorktreeService/ArchiveWorktree":          worktreeOperationTimeout,
		"/reliant.v1.WorktreeService/UnarchiveWorktree":        worktreeOperationTimeout,
		"/reliant.v1.WorktreeService/ImportWorktree":           worktreeOperationTimeout,
		"/reliant.v1.WorktreeService/DiscoverWorktrees":        worktreeOperationTimeout,
		"/reliant.v1.WorktreeService/PruneWorktrees":           worktreeOperationTimeout,
		"/reliant.v1.WorktreeService/RecreateWorktree":         worktreeOperationTimeout,
		"/reliant.v1.WorktreeService/GetWorktreeChanges":       worktreeOperationTimeout,
		"/reliant.v1.WorktreeService/GetWorktreeGitStatus":     worktreeOperationTimeout,
		"/reliant.v1.WorktreeService/GetWorktreeCommits":       worktreeOperationTimeout,
		"/reliant.v1.WorktreeService/ListWorktreeRepoStatuses": worktreeOperationTimeout,
		"/reliant.v1.WorktreeService/StageFiles":               worktreeOperationTimeout,
		"/reliant.v1.WorktreeService/UnstageFiles":             worktreeOperationTimeout,
		"/reliant.v1.WorktreeService/CommitWorktree":           worktreeOperationTimeout,
		"/reliant.v1.WorktreeService/PushWorktree":             worktreeOperationTimeout,
		"/reliant.v1.WorktreeService/PullWorktree":             worktreeOperationTimeout,
		"/reliant.v1.WorktreeService/GetWorktreePR":            worktreeOperationTimeout,
		"/reliant.v1.WorktreeService/CreateWorktreePR":         worktreeOperationTimeout,
		"/reliant.v1.WorktreeService/RevertFiles":              worktreeOperationTimeout,
		// Starting a storage clean-up lists accepted work and returns; the
		// work itself runs detached. The call still reads the held set and
		// writes progress for every accepted worktree.
		"/reliant.v1.InboxService/CleanupStorage": worktreeOperationTimeout,
		// MCP operations - external process management can be slow
		"/reliant.v1.MCPService/InstallServer":      60 * time.Second,
		"/reliant.v1.MCPService/RestartServer":      60 * time.Second,
		"/reliant.v1.MCPService/UpdateServerConfig": 60 * time.Second,
		"/reliant.v1.MCPService/UninstallServer":    60 * time.Second,
		// OAuth flows — no timeout, user can take as long as needed.
		// Cancellation is handled by the frontend AbortController.
		"/reliant.v1.DaemonService/StartOAuthFlow":   0,
		"/reliant.v1.SystemService/StartOAuthSignIn": 0,
		// OAuth token exchange - external network call
		"/reliant.v1.SettingsService/CompleteClaudeOAuth":      30 * time.Second,
		"/reliant.v1.SettingsService/CompleteCodexOAuth":       30 * time.Second,
		"/reliant.v1.SettingsService/CompleteAntigravityOAuth": 30 * time.Second,
	}
	for _, m := range owned {
		maps.Copy(timeouts, m)
	}
	return timeouts
}

// ReportSlowRPC is reliant's slow-RPC reporter (serverkit Deadline.OnSlow):
// every logging/metrics layer in the chain observes an RPC only when it
// COMPLETES, so a hung handler is invisible for as long as it hangs — during
// the 2026-07-09 worktree incident "forever, silently, while the whole
// renderer starved behind it". The slot's watchdog reports a unary RPC while
// it is still running; this logs each report as a [SlowRPC] warning and
// counts it in reliant_grpc_slow_rpc_total by procedure and stage.
func ReportSlowRPC(ctx context.Context, call serverkit.SlowCall) {
	if !call.Done {
		logging.WarnContext(ctx, "[SlowRPC] unary handler still in flight",
			"procedure", call.Procedure,
			"elapsed", call.Elapsed.Round(time.Second).String())
		observability.SlowRPCTotal.WithLabelValues(call.Procedure, "in_flight").Inc()
		return
	}
	logging.WarnContext(ctx, "[SlowRPC] unary handler completed slowly",
		"procedure", call.Procedure,
		"elapsed", call.Elapsed.Round(time.Millisecond).String(),
		"failed", call.Err != nil)
	observability.SlowRPCTotal.WithLabelValues(call.Procedure, "completed").Inc()
}
