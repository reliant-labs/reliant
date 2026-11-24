// Copyright (c) 2025 Reliant Labs
package auth

import (
	"context"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/authn"
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
	// CredentialSession is Supabase's session JWT, presented as a bearer:
	// what the web app and the desktop app hold after someone signs in in a
	// browser, until they move to the managed identity's cookie session
	// (design-auth-swap R6–R8). No agent, daemon or CLI login is issued one:
	// the CLI logs in for an `rlat_` access token, and daemon.json holds only
	// `rlat_` tokens.
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
	// CredentialIdentityToken is a token the managed identity (Zitadel,
	// forge.ManagedIdentity) issued, held by software: its access token as a
	// bearer, or a server-side session id presented anywhere but the sign-in
	// cookie. The app never sends one — a browser holds only the cookie — so
	// whoever does is a program holding the user's token, and it never acts
	// as a person.
	CredentialIdentityToken
	// CredentialBrowserSession is a person's sign-in session in the app: a
	// server-side session id (forge/pkg/session) that the login broker set
	// in its HttpOnly cookie, read from that cookie, and resolved to a live
	// session of the managed identity (forge's authn.Method.IsBrowserSession).
	// Only the browser that signed in holds it: the id is never handed to
	// script, and no token held by software resolves to one.
	CredentialBrowserSession
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
	case CredentialIdentityToken:
		return "identity token"
	case CredentialBrowserSession:
		return "browser session"
	default:
		return "unclassified credential"
	}
}

// Label names c in metrics (reliant_auth_credentials_total): a fixed,
// bounded set.
func (c Credential) Label() string {
	switch c {
	case CredentialSession:
		return "supabase_session"
	case CredentialOAuthClient:
		return "oauth_client"
	case CredentialAccessToken:
		return "access_token"
	case CredentialSharedKey:
		return "shared_key"
	case CredentialIdentityToken:
		return "identity_token"
	case CredentialBrowserSession:
		return "browser_session"
	default:
		return "unknown"
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
	return c == CredentialSession || c == CredentialSharedKey || c == CredentialBrowserSession
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
//   - CredentialBrowserSession: someone signed in to the app in a browser,
//     with the managed identity's sign-in cookie.
//   - CredentialSession: someone signed in to the app with Supabase, until
//     the app moves to the managed identity (design-auth-swap R8).
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

type approvedCardKey struct{}

// WithApprovedCard records on ctx that a person approved, on an approval card,
// exactly the change ctx is about to make: toolCallID is the agent's call the
// card was raised for. The card was decided through RequireApprovalDecider,
// and the tool that sets this has checked that the approved hash covers this
// very change, so the decision is a person's even though the write itself
// runs in the worker, with no request credential at all.
//
// No edge sets it, so no request can carry it: only in-process code that has
// verified an approved card does. RequirePersonOrApprovedCard reads it.
func WithApprovedCard(ctx context.Context, toolCallID string) context.Context {
	return context.WithValue(ctx, approvedCardKey{}, toolCallID)
}

// ApprovedCardFromContext is the tool call id of the approved card ctx acts
// on (WithApprovedCard), and whether there is one.
func ApprovedCardFromContext(ctx context.Context) (string, bool) {
	id, _ := ctx.Value(approvedCardKey{}).(string)
	return id, id != ""
}

// RequirePersonOrApprovedCard is RequirePerson for a change an agent may also
// make once a person approves its card: it admits what RequirePerson admits,
// and a ctx carrying an approved card (WithApprovedCard). Everything else is
// refused as RequirePerson refuses it — a CLI login, an API token, a daemon's
// credential, an OAuth client, and an unclassified context, which is what a
// worker call with no approved card is.
func RequirePersonOrApprovedCard(ctx context.Context, action string) error {
	if _, ok := ApprovedCardFromContext(ctx); ok {
		return nil
	}
	return RequirePerson(ctx, action)
}

func article(c Credential) string {
	switch c {
	case CredentialOAuthClient, CredentialAccessToken, CredentialIdentityToken, CredentialUnknown:
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

// RequireApprovalDecider is the one gate on deciding an approval: an
// approval means "a person said yes" (owner decision 2026-11-19: approvals
// need a cookie session; bearer and rlat_ tokens never approve). It admits:
//
//   - a browser session (CredentialBrowserSession) whose request forge's
//     authn.Method also calls one (IsBrowserSession: a server-side session
//     id read from the sign-in cookie). The credential and the method are
//     both stamped by the authenticator; requiring both means a context that
//     carries one without the other fails closed;
//   - a Supabase session (CredentialSession), the app's sign-in until it
//     moves to the managed identity's cookie session. This admission is the
//     bridge across the migration and ends with Supabase (design-auth-swap
//     R8 cutover, removed in R10);
//   - the self-host shared key (CredentialSharedKey): that mode has one
//     credential and no sign-in at all, so it is trusted as its single
//     operator (RequirePerson).
//
// Everything else is refused: an `rlat_` access token whatever scopes it
// holds (approvals:decide included; one bound to a machine with
// ReasonMachineCredentialCannotDecide), the managed identity's token as a
// bearer, a session id presented as a bearer, a token presented as the
// cookie (refused before it gets here), an OAuth client's token, and an
// unclassified context.
func RequireApprovalDecider(ctx context.Context, action string) error {
	switch CredentialFromContext(ctx) {
	case CredentialBrowserSession:
		if m, ok := authn.AuthMethod(ctx); ok && m.IsBrowserSession() {
			return nil
		}
	case CredentialSession, CredentialSharedKey:
		return nil
	case CredentialAccessToken:
		if p, ok := MachineTokenFromContext(ctx); ok && MachineBound(p) {
			return MachineCredentialRefusal()
		}
	}
	return svcerr.WithReason(svcerr.PermissionDenied(
		"Only a person signed in to the Reliant app can "+action+". This request was made with "+
			article(CredentialFromContext(ctx))+CredentialFromContext(ctx).String()+", which software can hold "+
			"(an API token or the CLI login included, whatever its scopes): open it in the Reliant app, "+
			"on the web or desktop, to decide it."),
		ReasonPersonRequired)
}
