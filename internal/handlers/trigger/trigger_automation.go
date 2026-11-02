// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"strings"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/automationcred"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// automationGrants keeps one delegated `daemon:resume` token per (user, daemon)
// that has at least one enabled trigger naming it, or that is a member of a
// machine pool an enabled trigger names. A nil client (self-hosted: no control
// plane, no suspendable workspaces) makes every method a no-op.
//
// Minting needs the user's live JWT, so it can only happen while they are
// signed in. A failure is logged and never fails the trigger write: the trigger
// still exists, and its fire reports "automation access not granted" instead.
type automationGrants struct {
	client   AutomationTokens
	keys     AutomationKeyStore
	triggers TriggerLister
}

// ensure mints and stores the token for (userID, daemonID).
func (g *automationGrants) ensure(ctx context.Context, userID, daemonID string) {
	if deferActivationEffect(ctx, func(ctx context.Context) error { g.ensure(ctx, userID, daemonID); return nil }) {
		return
	}
	if g == nil || g.client == nil || daemonID == "" {
		return
	}
	jwt, ok := auth.GetUserJWT(userID)
	if !ok || strings.TrimSpace(jwt) == "" {
		logging.WarnContext(ctx, "automation access not granted: no signed-in session to mint a daemon:resume token",
			"user_id", userID, "daemon_id", daemonID)
		return
	}
	tok, err := g.client.MintDaemonResumeToken(ctx, jwt, daemonID, automationcred.KeyName)
	if err != nil || strings.TrimSpace(tok.Plaintext) == "" {
		logging.WarnContext(ctx, "automation access not granted: could not mint daemon:resume token",
			"user_id", userID, "daemon_id", daemonID, "error", err)
		return
	}
	if err := g.keys.SetAutomationToken(ctx, userID, daemonID, tok.Plaintext); err != nil {
		logging.WarnContext(ctx, "automation access not granted: could not store daemon:resume token",
			"user_id", userID, "daemon_id", daemonID, "error", err)
	}
}

// releaseIfUnused revokes and forgets the token for (userID, daemonID) unless
// an enabled trigger of that user still names the daemon.
func (g *automationGrants) releaseIfUnused(ctx context.Context, userID, daemonID string) {
	if deferActivationEffect(ctx, func(ctx context.Context) error { g.releaseIfUnused(ctx, userID, daemonID); return nil }) {
		return
	}
	if g == nil || g.client == nil || daemonID == "" {
		return
	}
	all, err := g.triggers.ListTriggers(ctx, core.TriggerFilters{UserID: userID})
	if err != nil {
		logging.WarnContext(ctx, "could not check remaining triggers before revoking automation token",
			"user_id", userID, "daemon_id", daemonID, "error", err)
		return
	}
	for _, t := range all {
		if t.Enabled && t.DaemonID == daemonID {
			return
		}
		// A pool trigger may wake any member: keep the grant of a machine
		// that might be one. Over-keeping a token is safe; revoking one a pool
		// still needs strands its sleeping member.
		if t.Enabled && t.MachinePoolID != "" {
			return
		}
	}
	if jwt, ok := auth.GetUserJWT(userID); ok && strings.TrimSpace(jwt) != "" {
		if err := g.client.RevokeDaemonResumeTokens(ctx, jwt, daemonID); err != nil {
			logging.WarnContext(ctx, "could not revoke daemon:resume token at the control plane",
				"user_id", userID, "daemon_id", daemonID, "error", err)
		}
	} else {
		logging.WarnContext(ctx, "daemon:resume token forgotten locally but not revoked remotely: no signed-in session",
			"user_id", userID, "daemon_id", daemonID)
	}
	if err := g.keys.DeleteAutomationToken(ctx, userID, daemonID); err != nil {
		logging.WarnContext(ctx, "could not delete stored automation token", "user_id", userID, "daemon_id", daemonID, "error", err)
	}
}
