// Copyright (c) 2025 Reliant Labs
package services

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

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
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
// exchangeable, and what the CALLING token holds. A token may never grant
// authority it does not hold (forge/pkg/accesstoken), so a credential issued
// without deploy permission cannot become one that has it — and the scope list
// Settings shows for a token stays the truth about what it can do, directly or
// through here. The rest only ever narrows too: same acting user, same org,
// same daemon binding, ephemeral if the caller is, and an expiry no later than
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

// exchangeableScopes is the control-plane authority forge presents: deploy,
// managed secrets, custom domains. Deliberately NOT cluster:manage or token:*
// (org administration a deploy tool has no business holding), and never the
// session scopes themselves (reliant:api, daemon:connect) — an exchanged token
// that could connect a daemon would just be a second copy of the session.
var exchangeableScopes = []fat.Scope{
	fat.ScopeDeployRead, fat.ScopeDeployWrite,
	fat.ScopeSecretRead, fat.ScopeSecretWrite,
	fat.ScopeDomainRead, fat.ScopeDomainWrite,
}

// TokenControlPlane is what TokenService knows about the deployment's control
// plane.
type TokenControlPlane struct {
	// Issuer is the control plane's PUBLIC origin — the value of
	// RELIANT_AUTHORIZATION_SERVER, the issuer RFC 8414 metadata names. It is
	// the only audience ExchangeToken mints for. Empty on a self-hosted server,
	// which then issues no control-plane credentials at all.
	Issuer string
	// ClipsGrants reports that the authority clips an edited scope set to the
	// acting user's org permissions — control-plane's UpdateForUser does, the
	// self-hosted store does not. A DAEMON credential is given the user's
	// control-plane authority only where that clipping exists.
	ClipsGrants bool
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
func (s *TokenService) ExchangeToken(
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
	if strings.TrimSpace(s.controlPlane.Issuer) == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"this server has no control plane, so it issues no control-plane credentials"))
	}
	audience := strings.TrimSpace(req.Msg.GetAudience())
	if !sameControlPlane(audience, s.controlPlane.Issuer) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"tokens from this server are valid at %s, not %q; nothing was minted",
			strings.TrimRight(s.controlPlane.Issuer, "/"), audience))
	}
	requested, err := requestedExchangeScopes(req.Msg.GetScopes())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if subject.Resource != nil && subject.Resource.Kind != fat.ResourceDaemon {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf(
			"a token bound to a %s cannot be exchanged", subject.Resource.Kind))
	}

	granted := make([]fat.Scope, 0, len(requested))
	for _, scope := range requested {
		if subject.Scopes.Has(scope) {
			granted = append(granted, scope)
		}
	}
	if len(granted) == 0 {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf(
			"this Reliant credential cannot authorize %s: it holds %s. "+
				"Sign in again with `reliant auth login` (it asks for deploy, secret and domain permission), "+
				"or grant this credential those permissions in Reliant → Settings → Access Tokens",
			fat.JoinScopes(requested), describeScopes(subject.Scopes)))
	}

	expiresAt := time.Now().Add(exchangeTTL)
	if subject.ExpiresAt != nil && subject.ExpiresAt.Before(expiresAt) {
		expiresAt = *subject.ExpiresAt
	}
	minted, err := s.authority.MintForUser(ctx, tokenauthority.MintRequest{
		UserID: userID,
		// The parent's id makes the audit trail one hop: which session
		// produced this deploy credential.
		Name:      "forge (exchanged from " + subject.TokenID + ")",
		Scopes:    granted,
		Resource:  subject.Resource,
		Ephemeral: subject.Ephemeral,
		ExpiresAt: &expiresAt,
		// Never rotate: replacing the previous exchange would revoke a token
		// a long deploy in another process is still presenting.
	})
	if err != nil {
		return nil, authorityError("exchange", err)
	}

	// The org is the one dimension the mint does not take as input:
	// MintForUser acts in the user's PRIMARY org, the caller's token in the
	// org it was issued for. They agree unless the user's primary org changed
	// in between, and an exchange must never move authority across orgs.
	if err := s.confirmSameOrg(ctx, userID, minted, subject); err != nil {
		return nil, err
	}

	logging.Info("access token exchanged", "user_id", userID, "subject_token_id", subject.TokenID,
		"token_id", minted.TokenID, "scopes", fat.JoinScopes(granted), "expires_at", expiresAt.UTC().Format(time.RFC3339))
	out := &reliantv1.ExchangeTokenResponse{
		Token:       minted.Plaintext,
		TokenPrefix: minted.DisplayPrefix,
		ExpiresAt:   formatTime(&expiresAt),
		Scopes:      scopeStrings(granted),
	}
	if minted.ExpiresAt != nil {
		out.ExpiresAt = formatTime(minted.ExpiresAt)
	}
	return connect.NewResponse(out), nil
}

// sessionCredential authenticates the request's bearer against the authority,
// uncached, and requires it to be a Reliant session credential: live, acting
// as a person, carrying daemon:connect or reliant:api.
func (s *TokenService) sessionCredential(ctx context.Context, header http.Header) (*fat.Principal, error) {
	bearer, ok := strings.CutPrefix(header.Get("Authorization"), "Bearer ")
	bearer = strings.TrimSpace(bearer)
	if !ok || !auth.IsAccessTokenFormat(bearer) {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf(
			"ExchangeToken needs a Reliant access token (rlat_) as its bearer; sign in to Reliant (`reliant auth login` / the app)"))
	}
	subject, err := s.authority.Introspect(ctx, bearer)
	if errors.Is(err, tokenauthority.ErrInactive) {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf(
			"this Reliant credential is no longer valid; sign in to Reliant again (`reliant auth login` / the app)"))
	}
	if err != nil {
		logging.Error("token authority failed", "op", "exchange-introspect", "error", tokenauthority.Describe(err))
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("token service unavailable"))
	}
	if strings.TrimSpace(subject.ActingUserID) == "" {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf(
			"an organization token acts as no one, so there is no session to exchange; present it to the control plane directly"))
	}
	if !subject.Scopes.Has(fat.ScopeDaemonConnect) && !subject.Scopes.Has(fat.ScopeReliantAPI) {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf(
			"only a Reliant session credential (a daemon's or the CLI's) can be exchanged"))
	}
	return subject, nil
}

// confirmSameOrg introspects the freshly minted token and revokes it if it
// landed in a different org from its parent.
func (s *TokenService) confirmSameOrg(ctx context.Context, userID string, minted tokenauthority.Minted, subject *fat.Principal) error {
	child, err := s.authority.Introspect(ctx, minted.Plaintext)
	if err == nil && child.OrgID == subject.OrgID {
		return nil
	}
	if revokeErr := s.authority.RevokeForUser(ctx, userID, minted.TokenID); revokeErr != nil {
		logging.Error("could not revoke a mis-scoped exchanged token", "token_id", minted.TokenID,
			"error", tokenauthority.Describe(revokeErr))
	}
	if err != nil {
		logging.Error("token authority failed", "op", "exchange-confirm", "error", tokenauthority.Describe(err))
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("token service unavailable"))
	}
	logging.Warn("exchange refused: minted org differs from the calling token's", "user_id", userID,
		"subject_token_id", subject.TokenID)
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
		"your primary organization is not the one this credential was issued for; sign in to Reliant again"))
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
		return nil, err
	}
	out := make([]fat.Scope, 0, len(exchangeableScopes))
	for _, scope := range exchangeableScopes {
		if asked.Has(scope) {
			out = append(out, scope)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("none of the requested scopes is exchangeable (exchangeable: %s)", fat.JoinScopes(exchangeableScopes))
	}
	return out, nil
}

func describeScopes(set fat.Set) string {
	if len(set) == 0 {
		return "no permissions"
	}
	return set.String()
}

func scopeStrings(scopes []fat.Scope) []string {
	out := make([]string, len(scopes))
	for i, s := range scopes {
		out[i] = string(s)
	}
	return out
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
