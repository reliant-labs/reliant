// Package connauth adapts the connections resolver, and any delegated
// authority, to the credential source the declarative HTTP runtime consumes.
// httpaction never imports connections, and a credential reaches a request
// only through Apply.
//
// Resolution for one call, first hit wins (INTEGRATIONS.md §4.3):
//
//  1. the connection the call names explicitly;
//  2. the run owner's default saved connection for the integration;
//  3. the integration's `delegated` broker, when its manifest declares one and
//     this process registered it (GitHub through the control plane).
//
// The owner is always read from the run record, never from the request. A
// poll, which has no run, resolves by trigger id and reads the owner and the
// connection from the trigger record instead (ForTrigger).
//
//forge:not-a-component: built by reliant's hand-wired worker (internal/serverworker), not forge's composition root — until P3c (research/forge-migration/PLAN.md)
package connauth

import (
	"context"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
)

// Service is the credential source integration calls authenticate with. It
// satisfies httpaction.CredentialSource, and triggers.PollCredentials through
// ForTrigger.
type Service interface {
	// Credential resolves the connection for the run's owner. The integration
	// id must match the connection's, and a daemon-placed call is refused.
	Credential(ctx context.Context, req httpaction.CredentialRequest) (httpaction.Credential, error)

	// UsableIntegrations reports which of the integrations a call from this
	// run could find a credential for now, walking Credential's order for a
	// call that names no connection: the owner's default saved connection,
	// else — only when there is none at all — the integration's delegated
	// authority. integrations maps each id to its connection spec, which says
	// whether a delegated broker backs it.
	//
	// It is what decides whether an integration's tools are offered to the
	// run, so it must agree with what a call will meet:
	//
	//   - A saved connection is checked without reading its secret or
	//     recording a use (connections.Resolver.Usable). One that needs
	//     re-auth is not usable, and does NOT fall back to the broker, as
	//     Credential does not.
	//   - A delegated authority is asked for the owner's credential, which is
	//     then dropped: "has one" and "would hand one out" are the same
	//     question there (for GitHub, control-plane says not connected, or
	//     answers). A failure to ask is not usable: an absent tool is better
	//     than one that fails.
	//
	// The owner is read from the run record, never supplied. An owner that
	// cannot be read (an unknown run, a run with none) makes nothing usable,
	// and is returned as the error for the caller to log.
	UsableIntegrations(ctx context.Context, runID string, integrations map[string]*reliantv1.ConnectionSpec) (map[string]bool, error)

	// ForTrigger resolves the credential a polled trigger authenticates with.
	// It takes the trigger id and nothing else: the owner, the connection and
	// the integration are read from the trigger row
	// (connections.Resolver.ForTrigger), so a poller cannot be pointed at
	// another user's account by anything it is handed. A poll is always
	// server-placed.
	//
	// There is no delegated fall-back: a polled trigger listens through the
	// one connection its owner picked, and a missing one stays missing.
	ForTrigger(ctx context.Context, triggerID string) (httpaction.Credential, error)

	// Broker returns the delegated broker this source falls back on under id,
	// so a caller can judge an integration usable by the brokers this process
	// actually registered.
	Broker(id string) (DelegatedBroker, bool)
}

// Resolver resolves saved connections. *connections.Resolver satisfies it.
type Resolver interface {
	ForCall(ctx context.Context, call connections.CallSite, ref connections.Ref) (*connections.Resolved, error)
	ForTrigger(ctx context.Context, site connections.TriggerSite) (*connections.Resolved, error)
	OwnerOf(ctx context.Context, runID string) (string, error)
	Usable(ctx context.Context, call connections.CallSite, refs []connections.Ref) (map[connections.Ref]error, error)
}

// ActingBroker is a DelegatedBroker that can also speak as an identity other
// than the owner: the GitHub App's bot, for an automation whose activation
// chose it. Optional, so a broker that has no second identity (every other
// integration's) stays a plain DelegatedBroker.
//
// The request is the call itself, because what a non-owner credential may do
// depends on what the call touches: an installation token is scoped to one
// repository, which only the call's params name.
type ActingBroker interface {
	DelegatedBroker
	// ActingCredential returns the credential for req.ActingIdentity, on
	// behalf of ownerUserID. Errors are *httpaction.CredentialError; one with
	// CodeIdentityUnavailable means this deployment cannot provide the
	// identity at all (not that it failed to), and is the only case a caller
	// may answer with the owner's own credential.
	ActingCredential(ctx context.Context, ownerUserID string, req httpaction.CredentialRequest) (httpaction.Credential, error)
}

// DelegatedBroker turns a run owner into a credential obtained from an
// external authority at call time. ownerUserID is reliant's user id for the
// run's owner (the IdP subject). The returned credential must pin itself to
// the hosts the authority's token is for: the runtime pins a credential to
// the host a call started at, and the broker decides where it may start.
type DelegatedBroker interface {
	Credential(ctx context.Context, ownerUserID string) (httpaction.Credential, error)
}

// Deps are the credential source's collaborators.
type Deps struct {
	// Resolver resolves saved connections. nil yields a source that answers
	// every request with a FailedPrecondition, so a process without
	// connections refuses rather than silently running unauthenticated.
	Resolver Resolver
	// Broker looks up the delegated authority registered under an id, the
	// one a manifest's `delegated: { broker: <id> }` names: a Brokers
	// registry's Get. nil has none to fall back on.
	Broker func(id string) (DelegatedBroker, bool)
}
