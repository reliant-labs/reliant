// Copyright (c) 2025 Reliant Labs
package auth

import (
	"context"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/authz"
)

// Credential is what kind of credential authenticated a request. The
// validator that verified the bearer decides it (JWTValidator, the API-key
// validator, ClaimsForAPIToken) — never anything the client says about itself —
// and the auth interceptor and HTTP middleware put it on the context next to
// the caller's identity.
//
// It exists so a few actions can demand a person: an approval card means "a
// person said yes", and an agent holding the owner's login must not be able to
// say it. The only client-side agent marker, RELIANT_AGENT_SHELL, is set by
// the process it describes, so it proves nothing (reliant #2123).
type Credential int

const (
	// CredentialUnknown is a context no validator classified. It is the zero
	// value, so a path that forgets to classify fails closed.
	CredentialUnknown Credential = iota
	// CredentialSession is the identity provider's session JWT: what the web
	// app and the desktop app hold after someone signs in in a browser. No
	// agent, daemon or CLI login is issued one: the CLI logs in for an `rlat_`
	// access token, and daemon.json holds only `rlat_` tokens.
	CredentialSession
	// CredentialOAuthClient is an identity-provider JWT issued to a
	// third-party OAuth client (it carries a client_id claim): for example an
	// MCP client the user connected. It acts as the user, but software holds
	// it.
	CredentialOAuthClient
	// CredentialAccessToken is an `rlat_` access token carrying reliant:api:
	// the CLI login (`reliant auth login`) and API tokens (`reliant auth token
	// create`). Every agent shell inherits the CLI login.
	CredentialAccessToken
	// CredentialSharedKey is the self-host shared bearer key
	// (AUTH_MODE=apikey). Every client of that deployment — the web app, the
	// CLI, an agent's shell — presents the same key.
	CredentialSharedKey
)

// String names c for logs and errors.
func (c Credential) String() string {
	switch c {
	case CredentialSession:
		return "app session"
	case CredentialOAuthClient:
		return "OAuth client token"
	case CredentialAccessToken:
		return "access token"
	case CredentialSharedKey:
		return "shared API key"
	default:
		return "unclassified credential"
	}
}

type credentialKey struct{}

// WithCredential records on ctx the kind of credential that authenticated it,
// and tells internal/authz whether a person holds it (authz.WithCredentialKind):
// only a person may read what another org member shared. The person class is
// the one RequirePerson admits.
func WithCredential(ctx context.Context, c Credential) context.Context {
	ctx = context.WithValue(ctx, credentialKey{}, c)
	kind := authz.CredentialKindSoftware
	if isPerson(c) {
		kind = authz.CredentialKindPerson
	}
	return authz.WithCredentialKind(ctx, kind)
}

// isPerson is the credential class RequirePerson admits.
func isPerson(c Credential) bool {
	return c == CredentialSession || c == CredentialSharedKey
}

// CredentialFromContext is the kind of credential that authenticated ctx, or
// CredentialUnknown when none was recorded.
func CredentialFromContext(ctx context.Context) Credential {
	c, _ := ctx.Value(credentialKey{}).(Credential)
	return c
}

// ReasonPersonRequired is the error reason of a human-only action refused
// because the request was made with a credential software can hold.
const ReasonPersonRequired = "person_required"

// RequirePerson refuses, with PermissionDenied and ReasonPersonRequired, a
// request whose credential an agent or daemon can hold. action names what was
// refused, for the message ("approve this request").
//
// Accepted:
//   - CredentialSession: someone signed in to the app in a browser.
//   - CredentialSharedKey: a self-host API-key deployment has exactly one
//     credential for every client, so it cannot tell a person from an agent at
//     all. Refusing it would make approvals impossible there rather than
//     human-only, so that mode is trusted as its single operator; a deployment
//     that needs approvals to be provably human runs with an identity
//     provider.
//
// Everything else is refused, CredentialUnknown included.
func RequirePerson(ctx context.Context, action string) error {
	switch c := CredentialFromContext(ctx); {
	case isPerson(c):
		return nil
	default:
		return svcerr.WithReason(svcerr.PermissionDenied(
			"Only a person signed in to the Reliant app can "+action+". This request was made with "+
				article(c)+c.String()+", which an agent or a script can hold (such as the CLI login): "+
				"open it in the Reliant app, on the web or desktop, to decide it."),
			ReasonPersonRequired)
	}
}

func article(c Credential) string {
	switch c {
	case CredentialOAuthClient, CredentialAccessToken, CredentialUnknown:
		return "an "
	default:
		return "a "
	}
}

// ReasonMachineCredentialCannotDecide is the error reason of an approval
// decision refused because the access token is bound to a machine (a daemon).
const ReasonMachineCredentialCannotDecide = "machine_credential_cannot_decide"

// ScopeMissing is the refusal of a credential that lacks scope: PermissionDenied
// with reason scope_missing and the scope named in the error metadata.
func ScopeMissing(scope fat.Scope) error {
	return svcerr.WithMetadata(
		svcerr.WithReason(svcerr.PermissionDenied("this credential lacks the "+string(scope)+" scope"), fat.ReasonScopeMissing),
		fat.ScopesHeader, fat.ScopesHeaderValue([]fat.Scope{scope}))
}

// MachineCredentialRefusal is the refusal of an approval decision made with a
// machine-bound token.
func MachineCredentialRefusal() error {
	return svcerr.WithReason(svcerr.PermissionDenied(
		"This credential belongs to a machine (a daemon), and a machine never decides an approval, "+
			"whatever scopes it holds. Decide it in the Reliant app or with a token you minted for yourself."),
		ReasonMachineCredentialCannotDecide)
}

// MachineBound reports whether p is a credential a machine holds: bound to a
// resource (a daemon, a port, a connector, an environment), holding every scope
// (a daemon credential's wildcard), or carrying a daemon scope. A run executes
// with such a credential, so it must never decide the approvals of its own
// run, whatever scopes it holds; an unbound daemon:connect token is the
// registration PAT of a machine.
func MachineBound(p *fat.Principal) bool {
	return p != nil && (p.Resource != nil || p.AllScopes ||
		p.Scopes.Has(fat.ScopeDaemonConnect) || p.Scopes.Has(fat.ScopeDaemonResume))
}

// RequireApprovalDecider is the one gate on deciding an approval. It admits:
//
//   - a person: CredentialSession, and CredentialSharedKey (see RequirePerson);
//   - an access token that holds approvals:decide AS A GRANTED SCOPE and is not
//     bound to a machine.
//
// reliant:api does NOT stand in for approvals:decide here, even though
// Set.Permits implies the inbox scopes elsewhere: every token minted before
// approvals:decide existed, a workflow's included, stays refused. A token
// bound to a machine is refused even when it holds approvals:decide
// (ReasonMachineCredentialCannotDecide), and the check runs before the scope
// check so a daemon credential's wildcard scopes never reach it. Every other
// credential is refused as RequirePerson refuses it.
func RequireApprovalDecider(ctx context.Context, action string) error {
	if CredentialFromContext(ctx) != CredentialAccessToken {
		return RequirePerson(ctx, action)
	}
	p, ok := MachineTokenFromContext(ctx)
	if !ok {
		return RequirePerson(ctx, action) // an access token with no principal: fail closed
	}
	if MachineBound(p) {
		return MachineCredentialRefusal()
	}
	if !p.Scopes.Has(fat.ScopeApprovalsDecide) {
		return ScopeMissing(fat.ScopeApprovalsDecide)
	}
	return nil
}
