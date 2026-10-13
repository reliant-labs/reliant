// Package ghdelegated authenticates the `github` integration with a token
// DELEGATED by control-plane, the GitHub token authority, instead of one
// reliant stores.
//
// There is ONE GitHub App, control-plane's. In hosted mode (a control plane is
// configured) each user's GitHub App user token lives in control-plane's
// git_credentials, and control-plane renews it. reliant never stores it: at
// call time the worker resolves the RUN OWNER (from the run record, never from
// anything the workflow supplies), asks control-plane for that user's current
// token, applies it as a bearer to exactly one host, and scrubs it from
// everything the call returns.
//
// The `github` manifest declares `delegated: { broker: controlplane-github }`
// first among its auth methods. The worker registers the broker (New) under
// BrokerID with connauth.Brokers when a control plane is configured (hosted);
// the credential source asks it whenever the run owner has no saved GitHub
// connection. Self-hosted registers nothing, and the manifest's oauth2 method
// (RELIANT_OAUTH_GITHUB_CLIENT_ID/SECRET) or a pasted token is used instead.
//
//forge:not-a-component: built by reliant's hand-wired worker (internal/serverworker), not forge's composition root — until P3c (research/forge-migration/PLAN.md)
package ghdelegated

import (
	"context"
	"log/slog"

	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
)

// Service is the delegated broker: it turns a run owner into a GitHub
// credential pinned to APIHost. It satisfies connauth.DelegatedBroker.
type Service interface {
	// Credential fetches the owner's token from control-plane, every call.
	// The token is not cached here: control-plane renews it under a lock and
	// is the one place that knows whether it was revoked, and a GitHub call
	// is already a network round trip.
	Credential(ctx context.Context, ownerUserID string) (httpaction.Credential, error)
}

// TokenSource is what the broker needs from control-plane.
// gitcredentialclient's client satisfies it.
type TokenSource interface {
	UserAccessToken(ctx context.Context, externalUserID, provider string) (gitcredentialclient.Token, error)
}

// Deps are the broker's collaborators.
type Deps struct {
	// Tokens fetches the owner's current GitHub App user token.
	Tokens TokenSource
	// Logger receives transient control-plane failures. nil discards them.
	Logger *slog.Logger
}
