// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

// maxTokenNameLen bounds a token label.
const maxTokenNameLen = 128

// TokenService is reliant's ONE machine-credential surface — a thin facade
// over the deployment's token authority (control-plane when hosted, the local
// store when self-hosted). It never hashes, validates or stores a token.
type TokenService struct {
	reliantv1connect.UnimplementedTokenServiceHandler
	authority    tokenauthority.Authority
	controlPlane TokenControlPlane
}

// NewTokenService constructs the facade over authority. controlPlane is the
// deployment's control plane; its zero value is a self-hosted server.
func NewTokenService(authority tokenauthority.Authority, controlPlane TokenControlPlane) *TokenService {
	return &TokenService{authority: authority, controlPlane: controlPlane}
}

// scopeForKind maps the wire kind onto its one scope.
func scopeForKind(kind reliantv1.TokenKind) (fat.Scope, error) {
	switch kind {
	case reliantv1.TokenKind_TOKEN_KIND_DAEMON:
		return fat.ScopeDaemonConnect, nil
	case reliantv1.TokenKind_TOKEN_KIND_API:
		return fat.ScopeReliantAPI, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("kind is required (DAEMON or API)"))
	}
}

// kindForScopes names a token by the session authority it carries. A daemon
// credential also carries reliant:api, so daemon:connect decides wherever it
// sits in the list — the order a store returns scopes in is not a contract.
func kindForScopes(scopes []string) reliantv1.TokenKind {
	kind := reliantv1.TokenKind_TOKEN_KIND_UNSPECIFIED
	for _, s := range scopes {
		switch fat.Scope(s) {
		case fat.ScopeDaemonConnect:
			return reliantv1.TokenKind_TOKEN_KIND_DAEMON
		case fat.ScopeReliantAPI:
			kind = reliantv1.TokenKind_TOKEN_KIND_API
		}
	}
	return kind
}

// daemonCeiling is the authority a DAEMON credential is given where the
// control plane can clip it: daemon:connect, plus the control-plane authority
// ExchangeToken may derive for forge. It matches what `reliant daemon
// register` asks for and the control-plane half of what control-plane
// provisions for a managed daemon (requestedDaemonScopes), so the app's daemon
// is not the one machine where a deploy needs a second login. Not reliant:api:
// a daemon's credential stays a daemon's, not an API credential.
func daemonCeiling() fat.Set {
	set := fat.SetOf(fat.ScopeDaemonConnect)
	for _, s := range exchangeableScopes {
		set[s] = struct{}{}
	}
	return set
}

// widenDaemonCredential gives a just-minted daemon credential the user's own
// control-plane authority, clipped by the control plane to their org grants.
//
// TWO STEPS, NARROW FIRST, ON PURPOSE. MintForUser does not clip, so minting
// the ceiling directly would hand a member without a deploy grant a token that
// has one. UpdateForUser does clip (control-plane's access_token_internal), so
// mint narrow, then widen through the clip. A failed widen leaves a working,
// narrower daemon credential and is logged, never surfaced: the daemon still
// connects, and ExchangeToken will say what it lacks.
func (s *TokenService) widenDaemonCredential(ctx context.Context, userID, tokenID string) []string {
	if !s.controlPlane.ClipsGrants {
		return nil
	}
	info, err := s.authority.UpdateForUser(ctx, userID, tokenID, nil, daemonCeiling())
	if err != nil {
		logging.Warn("daemon credential minted without control-plane authority", "user_id", userID,
			"token_id", tokenID, "error", tokenauthority.Describe(err))
		return nil
	}
	return info.Scopes
}

// callerUser returns the authenticated user and whether the caller is a
// machine credential rather than an interactive session.
func callerUser(ctx context.Context) (string, bool, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return "", false, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("unauthenticated"))
	}
	_, isMachine := auth.MachineTokenFromContext(ctx)
	return userID, isMachine, nil
}

func formatTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func tokenInfoProto(t tokenauthority.TokenInfo) *reliantv1.TokenInfo {
	info := &reliantv1.TokenInfo{
		Id:          t.ID,
		Name:        t.Name,
		TokenPrefix: t.DisplayPrefix,
		CreatedAt:   t.CreatedAt.UTC().Format(time.RFC3339),
		LastUsedAt:  formatTime(t.LastUsedAt),
		ExpiresAt:   formatTime(t.ExpiresAt),
		Kind:        kindForScopes(t.Scopes),
		Ephemeral:   t.Ephemeral,
		// A permanent credential has no expiry to bound it, so its
		// PERMISSIONS are the only thing left to audit. Surfacing them is
		// what lets the Settings list answer "what can this token do".
		Scopes: t.Scopes,
	}
	if t.Resource != nil && t.Resource.Kind == fat.ResourceDaemon {
		info.DaemonId = t.Resource.ID
	}
	return info
}

func authorityError(op string, err error) error {
	switch {
	case tokenauthority.IsNotFound(err):
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("token not found"))
	case tokenauthority.IsInvalidGrant(err):
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	logging.Error("token authority failed", "op", op, "error", tokenauthority.Describe(err))
	return connect.NewError(connect.CodeUnavailable, fmt.Errorf("token service unavailable"))
}

// CreateToken mints a token acting as the caller. Interactive session only: a
// machine credential never mints a credential.
func (s *TokenService) CreateToken(
	ctx context.Context, req *connect.Request[reliantv1.CreateTokenRequest],
) (*connect.Response[reliantv1.CreateTokenResponse], error) {
	userID, isMachine, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}
	if isMachine {
		return nil, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("token management requires an interactive session; a token cannot mint a token"))
	}
	scope, err := scopeForKind(req.Msg.GetKind())
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Msg.GetName())
	if name == "" || len(name) > maxTokenNameLen {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("name is required (at most %d characters)", maxTokenNameLen))
	}
	if req.Msg.GetTtlSeconds() < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("ttl_seconds must not be negative"))
	}
	var expiresAt *time.Time
	if ttl := req.Msg.GetTtlSeconds(); ttl > 0 {
		at := time.Now().Add(time.Duration(ttl) * time.Second)
		expiresAt = &at
	}

	minted, err := s.authority.MintForUser(ctx, tokenauthority.MintRequest{
		UserID: userID, Name: name, Scopes: []fat.Scope{scope}, ExpiresAt: expiresAt,
	})
	if err != nil {
		return nil, authorityError("create", err)
	}
	scopes := []string{string(scope)}
	if scope == fat.ScopeDaemonConnect {
		if widened := s.widenDaemonCredential(ctx, userID, minted.TokenID); widened != nil {
			scopes = widened
		}
	}
	logging.Info("access token created", "user_id", userID, "token_id", minted.TokenID, "scopes", strings.Join(scopes, " "))
	return connect.NewResponse(&reliantv1.CreateTokenResponse{
		Info: &reliantv1.TokenInfo{
			Id:          minted.TokenID,
			Name:        name,
			TokenPrefix: minted.DisplayPrefix,
			CreatedAt:   time.Now().UTC().Format(time.RFC3339),
			ExpiresAt:   formatTime(minted.ExpiresAt),
			Kind:        req.Msg.GetKind(),
			// What the token carries: the kind's scope, or a daemon
			// credential's clipped ceiling. Built by hand rather than through
			// tokenInfoProto because a mint returns Minted, not TokenInfo —
			// which is exactly why this field was missing while the list
			// reported it.
			Scopes: scopes,
		},
		Token: minted.Plaintext,
	}), nil
}

// ListTokens lists the caller's live tokens, optionally of one kind.
func (s *TokenService) ListTokens(
	ctx context.Context, req *connect.Request[reliantv1.ListTokensRequest],
) (*connect.Response[reliantv1.ListTokensResponse], error) {
	userID, _, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}
	var scope fat.Scope
	if req.Msg.GetKind() != reliantv1.TokenKind_TOKEN_KIND_UNSPECIFIED {
		if scope, err = scopeForKind(req.Msg.GetKind()); err != nil {
			return nil, err
		}
	}
	tokens, err := s.authority.ListForUser(ctx, userID, scope)
	if err != nil {
		return nil, authorityError("list", err)
	}
	out := make([]*reliantv1.TokenInfo, 0, len(tokens))
	for _, t := range tokens {
		// This surface manages reliant's own kinds; LLM keys and connector
		// credentials have their own surfaces.
		if kindForScopes(t.Scopes) == reliantv1.TokenKind_TOKEN_KIND_UNSPECIFIED {
			continue
		}
		out = append(out, tokenInfoProto(t))
	}
	return connect.NewResponse(&reliantv1.ListTokensResponse{Tokens: out}), nil
}

// RevokeToken revokes one of the caller's tokens.
func (s *TokenService) RevokeToken(
	ctx context.Context, req *connect.Request[reliantv1.RevokeTokenRequest],
) (*connect.Response[reliantv1.RevokeTokenResponse], error) {
	userID, _, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(req.Msg.GetId())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("token id is required"))
	}
	if err := s.authority.RevokeForUser(ctx, userID, id); err != nil {
		return nil, authorityError("revoke", err)
	}
	logging.Info("access token revoked", "user_id", userID, "token_id", id)
	return connect.NewResponse(&reliantv1.RevokeTokenResponse{}), nil
}

// UpdateToken changes one of the caller's tokens' name and/or permissions.
//
// THE SECRET IS NEVER REISSUED. A live daemon's permissions can be widened or
// narrowed without re-registering it — which for a remote daemon would mean a
// browser login it cannot perform — and the change takes effect on the token's
// very next request, because authentication reads the row every time.
//
// SESSION ONLY, like minting. A machine credential editing scopes would be a
// token granting itself authority, which is the one thing the
// no-mint-beyond-your-scopes rule exists to prevent. Renaming is refused on the
// same path rather than carved out: one rule is easier to reason about than a
// field-by-field exception, and headless automation has no reason to rename.
func (s *TokenService) UpdateToken(
	ctx context.Context, req *connect.Request[reliantv1.UpdateTokenRequest],
) (*connect.Response[reliantv1.UpdateTokenResponse], error) {
	userID, isMachine, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}
	if isMachine {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf(
			"a machine credential cannot edit a credential; sign in to change a token's permissions"))
	}
	id := strings.TrimSpace(req.Msg.GetId())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("token id is required"))
	}

	var name *string
	if req.Msg.Name != nil {
		trimmed := strings.TrimSpace(req.Msg.GetName())
		if trimmed == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name cannot be blank"))
		}
		name = &trimmed
	}

	// A nil scope list leaves authority alone; an empty one is refused
	// downstream. The wrapper in the request is what keeps those apart — a
	// bare repeated field would make a rename strip every permission.
	var scopes fat.Set
	if req.Msg.Scopes != nil {
		scopes, err = fat.NewSet(req.Msg.GetScopes().GetScopes())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	if name == nil && scopes == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"nothing to update: set name, or set scopes with the permissions to grant"))
	}

	info, err := s.authority.UpdateForUser(ctx, userID, id, name, scopes)
	if err != nil {
		return nil, authorityError("update", err)
	}
	logging.Info("access token updated", "user_id", userID, "token_id", id,
		"renamed", name != nil, "rescoped", scopes != nil)
	return connect.NewResponse(&reliantv1.UpdateTokenResponse{Info: tokenInfoProto(info)}), nil
}
