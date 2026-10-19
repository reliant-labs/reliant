// Copyright (c) 2025 Reliant Labs

// Package automationcred holds the delegated credential an unattended trigger
// fire uses to wake the ONE daemon its trigger names.
//
// The token is a daemon-bound `daemon:resume` rlat_ minted while the user is
// signed in, stored in api_keys under a reserved per-daemon provider name. It
// is read only through BearerFor, only for an exact daemon id.
//
//forge:not-a-component: built by reliant's hand-wired worker (internal/serverworker), not forge's composition root — until P3c (research/forge-migration/PLAN.md)
package automationcred

import "context"

// Service picks the Bearer for a control-plane call.
type Service interface {
	// BearerFor returns, in order: the user's live JWT; the stored automation
	// token for exactly daemonID (only when daemonID != "" and ctx is
	// Allow-marked); otherwise "".
	BearerFor(ctx context.Context, userID, daemonID string) (string, error)
}

// KeyStore reads the stored automation tokens. *db.Repo satisfies it.
type KeyStore interface {
	GetAutomationToken(ctx context.Context, userID, daemonID string) (string, error)
}

// Deps are the resolver's collaborators.
type Deps struct {
	// Keys holds each daemon's stored automation token. nil serves only the
	// user's live JWT.
	Keys KeyStore
}
