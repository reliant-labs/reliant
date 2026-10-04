// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"strings"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/automationcred"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// automationGrants keeps one delegated `daemon:resume` token per (user, daemon)
// that has at least one enabled trigger. A nil client (self-hosted: no control
// plane, no suspendable workspaces) makes every method a no-op.
//
// Minting needs the user's live JWT, so it can only happen while they are
// signed in. A failure is logged and never fails the trigger write: the trigger
// still exists, and its fire reports "automation access not granted" instead.
type automationGrants struct {
	client controlplane.Client
	keys   interface {
		SetProviderAPIKey(ctx context.Context, userID, provider, apiKey string) error
		DeleteProviderAPIKey(ctx context.Context, userID, provider string) error
	}
	triggers interface {
		ListTriggers(ctx context.Context, f core.TriggerFilters) ([]*core.Trigger, error)
	}
}

// ensure mints and stores the token for (userID, daemonID).
func (g *automationGrants) ensure(ctx context.Context, userID, daemonID string) {
	if g == nil || g.client == nil || daemonID == "" {
		return
	}
	jwt, ok := auth.GetUserJWT(userID)
	if !ok || strings.TrimSpace(jwt) == "" {
		logging.Warn("automation access not granted: no signed-in session to mint a daemon:resume token",
			"user_id", userID, "daemon_id", daemonID)
		return
	}
	tok, err := g.client.MintDaemonResumeToken(ctx, jwt, daemonID, automationcred.KeyName)
	if err != nil || strings.TrimSpace(tok.Plaintext) == "" {
		logging.Warn("automation access not granted: could not mint daemon:resume token",
			"user_id", userID, "daemon_id", daemonID, "error", err)
		return
	}
	if err := g.keys.SetProviderAPIKey(ctx, userID, automationcred.Provider(daemonID), tok.Plaintext); err != nil {
		logging.Warn("automation access not granted: could not store daemon:resume token",
			"user_id", userID, "daemon_id", daemonID, "error", err)
	}
}

// releaseIfUnused revokes and forgets the token for (userID, daemonID) unless
// an enabled trigger of that user still names the daemon.
func (g *automationGrants) releaseIfUnused(ctx context.Context, userID, daemonID string) {
	if g == nil || g.client == nil || daemonID == "" {
		return
	}
	all, err := g.triggers.ListTriggers(ctx, core.TriggerFilters{UserID: userID})
	if err != nil {
		logging.Warn("could not check remaining triggers before revoking automation token",
			"user_id", userID, "daemon_id", daemonID, "error", err)
		return
	}
	for _, t := range all {
		if t.Enabled && t.DaemonID == daemonID {
			return
		}
	}
	if jwt, ok := auth.GetUserJWT(userID); ok && strings.TrimSpace(jwt) != "" {
		if err := g.client.RevokeDaemonResumeTokens(ctx, jwt, daemonID); err != nil {
			logging.Warn("could not revoke daemon:resume token at the control plane",
				"user_id", userID, "daemon_id", daemonID, "error", err)
		}
	} else {
		logging.Warn("daemon:resume token forgotten locally but not revoked remotely: no signed-in session",
			"user_id", userID, "daemon_id", daemonID)
	}
	if err := g.keys.DeleteProviderAPIKey(ctx, userID, automationcred.Provider(daemonID)); err != nil {
		logging.Warn("could not delete stored automation token", "user_id", userID, "daemon_id", daemonID, "error", err)
	}
}
