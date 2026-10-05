// Copyright (c) 2025 Reliant Labs

// Package ghaccess keeps each GitHub trigger owner's access snapshot fresh:
// which repositories, in which GitHub App installations, the owner's OWN
// token can see. Access-gated routing (internal/integrations/webhook) sends a
// GitHub event about a repository only to triggers whose owner holds a fresh
// grant for it, so this snapshot is the line between "an org member's
// trigger fires on the org's repositories they can see" and "it fires on all
// of them".
//
// forge:outbound-io
//
// A refresh asks GitHub, with the user's GitHub App user token:
//
//	GET /user                                         who the token is (subject id)
//	GET /user/installations                           installations it can reach
//	GET /user/installations/{id}/repositories         repositories in each it can see
//
// The last is GitHub's own intersection of "the App is installed on it" and
// "this user has explicit access to it" — exactly the routing predicate.
// Personal access tokens cannot call these endpoints, so only App user
// tokens are used: control-plane's delegated token in hosted deployments, a
// saved oauth2 GitHub connection when self-hosted.
//
// Refreshes happen when a trigger is activated (TriggerService), and
// periodically for every user with an enabled GitHub trigger (Run). Webhook
// revocations cut access between refreshes; a refresh restores what remains.
package ghaccess

import (
	"context"
	"errors"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// IntegrationID is the integration whose access this package tracks.
const IntegrationID = "github"

// TokenSource returns a user's current GitHub App user token.
type TokenSource interface {
	// Token returns the token, or an error wrapping ErrNotConnected /
	// ErrNeedsReconnect when the user must act first; anything else is
	// transient.
	Token(ctx context.Context, userID string) (string, error)
}

// Store is the persistence a refresher needs. *db.Repo satisfies it.
type Store interface {
	ReplaceIntegrationAccess(ctx context.Context, userID, integrationID, subjectID string, at time.Time, grants []core.IntegrationAccessGrant) error
	ListIntegrationTriggerOwners(ctx context.Context, integration string) ([]string, error)
	ClaimIntegrationAccessRefresh(ctx context.Context, userID, integrationID string, now, leaseUntil, dueBefore time.Time) (bool, error)
	FinishIntegrationAccessRefresh(ctx context.Context, userID, integrationID string, at time.Time, refreshErr error) error
	PruneIntegrationAccess(ctx context.Context, before time.Time) (int64, error)
}

// Permanent token outcomes: the user has to act (connect, reconnect) before
// any refresh can succeed, so their grants are cleared rather than kept.
var (
	ErrNotConnected   = errors.New("ghaccess: GitHub is not connected")
	ErrNeedsReconnect = errors.New("ghaccess: GitHub needs to be reconnected")
	// ErrTokenRejected is GitHub refusing the token (401).
	ErrTokenRejected = errors.New("ghaccess: GitHub rejected the token")
	// ErrUnsupportedToken is a token kind that cannot list installations.
	ErrUnsupportedToken = errors.New("ghaccess: this GitHub connection cannot list App installations")
)

// IsPermanent reports whether a refresh error means the user must act.
func IsPermanent(err error) bool {
	return errors.Is(err, ErrNotConnected) || errors.Is(err, ErrNeedsReconnect) ||
		errors.Is(err, ErrTokenRejected) || errors.Is(err, ErrUnsupportedToken)
}
