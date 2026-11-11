// Copyright (c) 2025 Reliant Labs
package token

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/credentials"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/cliauth"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

// =============================================================================
// ExchangeToken — a Reliant session becomes forge's control-plane credential.
//
// A user signed in to Reliant must not have to run `forge login` as well. The
// session credential they already hold (a daemon's, or the CLI's) acts as them
// everywhere in Reliant, but it must never be PRESENTED to the control plane's
// deploy API: it is permanent, it can also connect as their daemon, and a
// forge deploy hands its credential to a registry login and to subprocesses.
// So reliant's credential helper trades it here for a token that can do
// nothing but what forge needs, for an hour.
//
// ── ATTENUATION, NEVER ESCALATION ────────────────────────────────────
//
// The granted scopes are the intersection of what was requested, what is
// exchangeable, and what the CALLING token holds (for a wildcard credential,
// its ceiling: see exchangeCeiling, which also names the environment-bindable
// scopes for the control plane to keep or clip). A token may never grant
// authority it does not hold (forge/pkg/accesstoken), so a credential issued
// without deploy permission cannot become one that has it — and the scope list
// Settings shows for a token stays the truth about what it can do, directly or
// through here. The rest only ever narrows too: same acting user, the org asked for (the
// caller's own when none is: see targetOrg), same daemon binding, ephemeral if the caller is, and an expiry no later than
// the caller's own.
//
// ── THE AUDIENCE IS CHECKED BEFORE ANYTHING IS MINTED ────────────────
//
// forge learns which control plane to talk to from a project's KCL, and a
// repository is not a trusted party: `forge.ControlPlane { endpoint = <theirs> }`
// in a cloned repo would otherwise be a way to harvest a deploy token. This
// server knows the one control plane its tokens are valid at, and refuses any
// other audience without minting.
//
// ── WHAT IT DOES NOT DO ──────────────────────────────────────────────
//
// Revoking the CALLING token does not revoke a token already exchanged from it:
// that needs a parent link the token table does not have. The exposure is
// bounded by exchangeTTL, and a daemon-bound caller's exchanged token is bound
// to the same daemon, so tearing the daemon down revokes both.
// =============================================================================

// exchangeTTL is the longest an exchanged token lives. Long enough for a slow
// hosted deploy that holds one token throughout; short enough that a leaked one
// — from a registry login's credential store, a CI trace, a core dump — is dead
// within the hour. The credential helper reuses a token only while plenty of
// this remains, so a command never starts on one about to lapse.
const exchangeTTL = time.Hour

// exchangeableScopes is what a helper may hold: the user's control-plane
// capabilities (cliauth.ForgeScopes, derived from forge's catalog at this
// binary's build), never session or specialized-resource authority. The child
// is an explicit list of what the command asked for, intersected with what the
// parent holds NOW — and a daemon's parent holds every scope its person holds
// (the control plane resolves that wildcard at each introspection), so a scope
// added to the catalog is exchangeable the release it ships in. Resource
// validation remains authoritative: user-org capabilities may ride a
// daemon-bound credential, preserving its lifecycle. Never drop that binding
// to get a mint.
var exchangeableScopes = func() []fat.Scope {
	var scopes []fat.Scope
	for _, scope := range cliauth.ForgeScopes() {
		scopes = append(scopes, fat.Scope(scope))
	}
	return scopes
}()

// environmentBindableScopes are the control-plane permissions a person may
// hold bound to ONE deploy environment (control-plane orggrants.EnvScopes,
// docs/design/orgs-and-members.md §4): a release manager's deploy:write on
// prod alone, say. Named as strings and kept only when this binary's forge
// knows the scope (storage:read and deploy:config:read arrive with a newer
// pin), so the set grows with the catalog and never names a scope that could
// not be minted.
var environmentBindableScopes = func() fat.Set {
	out := fat.Set{}
	for _, raw := range []string{"deploy:write", "secret:write", "db:query", "storage:read", "deploy:config:read"} {
		if scope, ok := fat.ParseScope(raw); ok {
			out[scope] = struct{}{}
		}
	}
	return out
}()

// exchangeCeiling is what the calling session may hand on: what it resolved to,
// plus — for a wildcard credential (a daemon's or a CLI login's, which hold
// every scope their person holds) — the environment-bindable scopes.
//
// WHY THE WILDCARD ADDS THEM. The control plane resolves a credential that may
// act on every environment to its person's ORG-WIDE grants only, so a grant
// bound to prod never shows in subject.Scopes. Intersecting with that alone
// would mint the helper's child without it, and the child could never spend it.
// A wildcard's ceiling holds every scope, so naming a bindable one is no
// widening of the ceiling; and it is never authority either: the control
// plane's MintForUser keeps it only when the person holds it on some
// environment (and the daemon's policy allows it), resolves the child to
// org-wide grants, and judges every use on its own environment. A control
// plane that predates that rule clips it away at the mint, so against an older
// control plane the child stays as narrow as before, never wider. An explicit-
// list credential's ceiling is not visible here, so it hands on only what it
// resolved to.
func exchangeCeiling(subject *fat.Principal) fat.Set {
	if !subject.AllScopes {
		return subject.Scopes
	}
	out := make(fat.Set, len(subject.Scopes)+len(environmentBindableScopes))
	for scope := range subject.Scopes {
		out[scope] = struct{}{}
	}
	for scope := range environmentBindableScopes {
		out[scope] = struct{}{}
	}
	return out
}

// environmentOnlyExchange is the refusal of an exchange whose child would
// carry only scopes held on specific environments: it would resolve to its
// person's org-wide grants, which is nothing, and never authenticate. The
// control plane refuses such a mint itself (control-plane #2400,
// "failed_precondition", passed through above); an older one mints it, and
// confirmOrg then finds it resolves to nothing. granted, when known, is named.
func environmentOnlyExchange(granted []fat.Scope) error {
	what := "the requested permissions"
	if len(granted) > 0 {
		what = fat.JoinScopes(granted)
	}
	return svcerr.FailedPrecondition(fmt.Sprintf(
		"this exchange would authorize nothing: you hold %s only on specific environments, and an exchanged "+
			"credential carries only what you hold across the organization; ask for it together with a permission "+
			"you hold across the organization (forge asks for deploy:read), or ask an organization admin for one", what))
}

// TokenControlPlane is what TokenService knows about the deployment's control
// plane.
type TokenControlPlane struct {
	// Issuer is the control plane's PUBLIC origin — the value of
	// RELIANT_AUTHORIZATION_SERVER, the issuer RFC 8414 metadata names. It is
	// the only audience ExchangeToken mints for. Empty on a self-hosted server,
	// which then issues no control-plane credentials at all.
	Issuer string
}

// ExchangeToken trades the calling access token for a short-lived one carrying
// only control-plane authority. See the block comment above.
//
// IT AUTHENTICATES ITS OWN CALLER. The API interceptor admits an `rlat_` only
// with reliant:api, and a daemon's credential is deliberately not an API
// credential — yet a daemon's session is exactly what an agent's forge must be
// able to exchange. So this method is public at the interceptor and checks the
// bearer here instead, against the authority directly (uncached), admitting
// any SESSION credential: one that acts as a person and carries daemon:connect
// or reliant:api. What it can do with that is bounded by the attenuation rule,
// not by which session it is.
func (s *Service) ExchangeToken(
	ctx context.Context, req *connect.Request[reliantv1.ExchangeTokenRequest],
) (*connect.Response[reliantv1.ExchangeTokenResponse], error) {
	// Re-read the calling token AUTHORITATIVELY: a cached answer can serve a
	// just-revoked token for a while, and minting from a revoked credential
	// is exactly the moment that must not happen.
	subject, err := s.sessionCredential(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	userID := subject.ActingUserID
	if strings.TrimSpace(s.deps.ControlPlane.Issuer) == "" {
		return nil, svcerr.WithReason(
			svcerr.FailedPrecondition("this server has no control plane, so it issues no control-plane credentials"),
			errreason.NoControlPlaneCredential)
	}
	audience := strings.TrimSpace(req.Msg.GetAudience())
	if !sameControlPlane(audience, s.deps.ControlPlane.Issuer) {
		return nil, svcerr.FailedPrecondition(fmt.Sprintf(
			"tokens from this server are valid at %s, not %q; nothing was minted",
			strings.TrimRight(s.deps.ControlPlane.Issuer, "/"), audience))
	}
	requested, err := requestedExchangeScopes(req.Msg.GetScopes())
	if err != nil {
		return nil, err
	}
	if subject.Resource != nil && subject.Resource.Kind != fat.ResourceDaemon {
		return nil, svcerr.PermissionDenied(fmt.Sprintf(
			"a token bound to a %s cannot be exchanged", subject.Resource.Kind))
	}

	ceiling := exchangeCeiling(subject)
	granted := make([]fat.Scope, 0, len(requested))
	for _, scope := range requested {
		if ceiling.Has(scope) {
			granted = append(granted, scope)
		}
	}
	if len(granted) == 0 {
		// scope_missing with the scopes in structure (fat.ScopesHeader), so the
		// deploy panel and forge's credential helper can name the grant
		// without parsing this sentence, which stays as it was.
		return nil, svcerr.WithMetadata(svcerr.WithReason(svcerr.PermissionDenied(fmt.Sprintf(
			"this Reliant credential cannot authorize %s: it holds %s. "+
				"Sign in again with `reliant auth login` (it asks for your control-plane permissions), "+
				"or grant this credential those permissions in Reliant → Settings → Access Tokens",
			fat.JoinScopes(requested), describeScopes(subject.Scopes))), fat.ReasonScopeMissing),
			fat.ScopesHeader, fat.ScopesHeaderValue(requested))
	}

	// THE ORG IS THE ONE THING AN EXCHANGE MAY MOVE, and only to another org
	// the same person belongs to. The request names it (the web's active org,
	// forge's pinned org); unset, the token lands in the calling session's own
	// org, exactly as before. The control plane judges membership and clips the
	// scopes to the person's grants IN that org, so naming an org is never a
	// way to hold more than the person does there; and the scopes are still
	// capped by what the calling session holds, so it cannot widen either.
	targetOrg := strings.TrimSpace(req.Msg.GetOrgId())
	if targetOrg == "" {
		targetOrg = subject.OrgID
	}

	expiresAt := time.Now().Add(exchangeTTL)
	if subject.ExpiresAt != nil && subject.ExpiresAt.Before(expiresAt) {
		expiresAt = *subject.ExpiresAt
	}
	minted, err := s.deps.Authority.MintForUser(ctx, tokenauthority.MintRequest{
		UserID: userID,
		// The parent's id makes the audit trail one hop: which session
		// produced this deploy credential.
		Name:      "forge (exchanged from " + subject.TokenID + ")",
		Scopes:    granted,
		Resource:  subject.Resource,
		Ephemeral: subject.Ephemeral,
		ExpiresAt: &expiresAt,
		OrgID:     targetOrg,
		// Never rotate: replacing the previous exchange would revoke a token
		// a long deploy in another process is still presenting.
	})
	if err != nil {
		// Preserve the authority's membership refusal; it is not an outage.
		var refused *accesstokenclient.RPCError
		if errors.As(err, &refused) && refused.Code == "permission_denied" {
			return nil, svcerr.WithReason(svcerr.PermissionDenied(refused.Message), refused.Reason)
		}
		// The control plane's refusal of a ceiling that would authorize
		// nothing (control-plane #2400) is the caller's to act on, not an
		// outage.
		//
		// MIXED VERSIONS. A control plane before #2400 clips a bindable
		// scope to org-wide grants at the mint, so this exchange stays as
		// narrow as before (never wider) and an environment-only request
		// ends as its permission_denied above. A control plane with #2400
		// refuses that request here. Either way nothing that cannot
		// authenticate is handed back; confirmOrg is the backstop. Rolling
		// this server back restores exchangeCeiling's old answer (the
		// resolved scopes only), the narrow behaviour, with no control-plane
		// step: the control plane only ever clips more.
		if errors.As(err, &refused) && refused.Code == "failed_precondition" {
			return nil, svcerr.WithReason(svcerr.FailedPrecondition(refused.Message), refused.Reason)
		}
		return nil, authorityError("exchange", err)
	}

	// The token must have landed in the org that was asked for (the caller's
	// own when none was): revoke it if a default that moved in between put it
	// elsewhere.
	if err := s.confirmOrg(ctx, userID, minted, targetOrg); err != nil {
		return nil, err
	}

	logging.InfoContext(ctx, "access token exchanged", "user_id", userID, "subject_token_id", subject.TokenID,
		"token_id", minted.TokenID, "scopes", strings.Join(minted.Scopes, " "), "expires_at", expiresAt.UTC().Format(time.RFC3339))
	out := &reliantv1.ExchangeTokenResponse{
		Token:       minted.Plaintext,
		TokenPrefix: minted.DisplayPrefix,
		ExpiresAt:   formatTime(&expiresAt),
		// The authority may further narrow to the owner's CURRENT org grants.
		Scopes: minted.Scopes,
		OrgId:  targetOrg,
	}
	if minted.ExpiresAt != nil {
		out.ExpiresAt = formatTime(minted.ExpiresAt)
	}
	return connect.NewResponse(out), nil
}

// sessionCredential authenticates the request's bearer against the authority,
// uncached, and requires it to be a Reliant session credential: live, acting
// as a person, carrying daemon:connect or reliant:api.
func (s *Service) sessionCredential(ctx context.Context, header http.Header) (*fat.Principal, error) {
	bearer, ok := strings.CutPrefix(header.Get("Authorization"), "Bearer ")
	bearer = strings.TrimSpace(bearer)
	if !ok || !auth.IsAccessTokenFormat(bearer) {
		return nil, svcerr.Unauthenticated("ExchangeToken needs a Reliant access token (rlat_) as its bearer; sign in to Reliant (`reliant auth login` / the app)")
	}
	subject, err := s.deps.Authority.Introspect(ctx, bearer)
	if errors.Is(err, tokenauthority.ErrInactive) {
		return nil, svcerr.Unauthenticated("this Reliant credential is no longer valid; sign in to Reliant again (`reliant auth login` / the app)")
	}
	if err != nil {
		return nil, svcerr.WithCause(svcerr.Unavailable("token service unavailable"), err)
	}
	if strings.TrimSpace(subject.ActingUserID) == "" {
		return nil, svcerr.PermissionDenied("an organization token acts as no one, so there is no session to exchange; present it to the control plane directly")
	}
	if !subject.Scopes.Has(fat.ScopeDaemonConnect) && !subject.Scopes.Has(fat.ScopeReliantAPI) {
		return nil, svcerr.PermissionDenied("only a Reliant session credential (a daemon's or the CLI's) can be exchanged")
	}
	return subject, nil
}

// confirmOrg introspects the freshly minted token and revokes it if it landed
// in a different org from the one asked for.
func (s *Service) confirmOrg(ctx context.Context, userID string, minted tokenauthority.Minted, wantOrg string) error {
	child, err := s.deps.Authority.Introspect(ctx, minted.Plaintext)
	if err == nil && child.OrgID == wantOrg {
		return nil
	}
	if revokeErr := s.deps.Authority.RevokeForUser(ctx, userID, minted.TokenID); revokeErr != nil {
		logging.ErrorContext(ctx, "could not revoke a mis-scoped exchanged token", "token_id", minted.TokenID,
			"error", tokenauthority.Describe(revokeErr))
	}
	if errors.Is(err, accesstokenclient.ErrNoRecognisedScope) {
		// An older control plane minted a child that resolves to nothing:
		// every scope it kept is held only on specific environments.
		// Revoked above; say why rather than report an outage.
		return environmentOnlyExchange(nil)
	}
	if err != nil {
		return svcerr.WithCause(svcerr.Unavailable("token service unavailable"), err)
	}
	logging.WarnContext(ctx, "exchange refused: minted org is not the requested one", "user_id", userID, "want_org", wantOrg)
	return svcerr.FailedPrecondition("the credential landed in a different organization than the one requested; try again, or sign in to Reliant again")
}

// requestedExchangeScopes parses a request's scopes. Empty means every
// exchangeable scope; anything not exchangeable is dropped rather than refused
// (RFC 6749 §3.3 — the response says what was granted), but a string that is
// not a scope at all is a malformed request.
func requestedExchangeScopes(raw []string) ([]fat.Scope, error) {
	if len(raw) == 0 {
		return exchangeableScopes, nil
	}
	asked, err := fat.NewSet(raw)
	if err != nil {
		// fat's refusal names the scope it does not know: the caller's to fix.
		return nil, svcerr.InvalidArgument(err.Error())
	}
	out := make([]fat.Scope, 0, len(exchangeableScopes))
	for _, scope := range exchangeableScopes {
		if asked.Has(scope) {
			out = append(out, scope)
		}
	}
	if len(out) == 0 {
		return nil, svcerr.InvalidArgument(fmt.Sprintf("none of the requested scopes is exchangeable (exchangeable: %s)", fat.JoinScopes(exchangeableScopes)))
	}
	return out, nil
}

func describeScopes(set fat.Set) string {
	if len(set) == 0 {
		return "no permissions"
	}
	return set.String()
}

// sameControlPlane reports whether audience names the control plane at issuer:
// the same normalized origin, with loopback aliases (localhost, 127.0.0.1,
// ::1) on the same scheme and port treated as one — a dev stack reaches one
// server by both spellings, and nothing outside the machine can be reached
// through either.
func sameControlPlane(audience, issuer string) bool {
	a, errA := credentials.Normalize(audience)
	i, errI := credentials.Normalize(issuer)
	if errA != nil || errI != nil {
		return false
	}
	if a == i {
		return true
	}
	au, errA := url.Parse(a)
	iu, errI := url.Parse(i)
	if errA != nil || errI != nil {
		return false
	}
	return au.Scheme == iu.Scheme && au.Port() == iu.Port() && isLoopback(au.Hostname()) && isLoopback(iu.Hostname())
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
