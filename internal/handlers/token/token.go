// Copyright (c) 2025 Reliant Labs
package token

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

// maxTokenNameLen bounds a token label.
const maxTokenNameLen = 128

// scopeForKind maps the wire kind onto its one scope.
func scopeForKind(kind reliantv1.TokenKind) (fat.Scope, error) {
	switch kind {
	case reliantv1.TokenKind_TOKEN_KIND_DAEMON:
		return fat.ScopeDaemonConnect, nil
	case reliantv1.TokenKind_TOKEN_KIND_API:
		return fat.ScopeReliantAPI, nil
	default:
		return "", svcerr.InvalidArgument("kind is required (DAEMON or API)")
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

// callerUser returns the authenticated user and whether the caller is a
// machine credential rather than an interactive session.
func callerUser(ctx context.Context) (string, bool, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return "", false, svcerr.Unauthenticated("unauthenticated")
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

// authorityError is a failed token-authority call as the svcerr kind the rpc
// returns. The authority may be control-plane's (a remote client), so its error
// is never the verdict itself: it is the logged cause, and the client reads
// what this rpc says — except a grant the caller asked for and may not have,
// which is theirs to correct and is reported in the authority's own words.
func authorityError(op string, err error) error {
	switch {
	case tokenauthority.IsNotFound(err):
		return svcerr.WithCause(svcerr.NotFound("token"), err)
	case tokenauthority.IsInvalidGrant(err):
		msg := err.Error()
		var refused *accesstokenclient.RPCError
		if errors.As(err, &refused) {
			msg = refused.Message
		}
		return svcerr.WithCause(svcerr.InvalidArgument(msg), err)
	}
	return svcerr.WithCause(svcerr.Unavailable("token service unavailable"), fmt.Errorf("token authority %s: %w", op, err))
}

// CreateToken mints a token acting as the caller. Interactive session only: a
// machine credential never mints a credential.
func (s *Service) CreateToken(
	ctx context.Context, req *connect.Request[reliantv1.CreateTokenRequest],
) (*connect.Response[reliantv1.CreateTokenResponse], error) {
	userID, isMachine, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}
	if isMachine {
		return nil, svcerr.PermissionDenied("token management requires an interactive session; a token cannot mint a token")
	}
	scope, err := scopeForKind(req.Msg.GetKind())
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Msg.GetName())
	if name == "" || len(name) > maxTokenNameLen {
		return nil, svcerr.InvalidArgument(fmt.Sprintf("name is required (at most %d characters)", maxTokenNameLen))
	}
	if req.Msg.GetTtlSeconds() < 0 {
		return nil, svcerr.InvalidArgument("ttl_seconds must not be negative")
	}
	var expiresAt *time.Time
	if ttl := req.Msg.GetTtlSeconds(); ttl > 0 {
		at := time.Now().Add(time.Duration(ttl) * time.Second)
		expiresAt = &at
	}

	// The kind's one scope, and nothing else. A DAEMON credential minted by
	// the control plane holds EVERY scope its person holds from that alone,
	// resolved at each use (control-plane accesstoken/wildcard.go), so no
	// list named here can go stale when forge adds a scope; a self-hosted
	// store mints exactly daemon:connect.
	minted, err := s.deps.Authority.MintForUser(ctx, tokenauthority.MintRequest{
		UserID: userID, Name: name, Scopes: []fat.Scope{scope}, ExpiresAt: expiresAt,
	})
	if err != nil {
		return nil, authorityError("create", err)
	}
	scopes := minted.Scopes
	logging.InfoContext(ctx, "access token created", "user_id", userID, "token_id", minted.TokenID, "scopes", strings.Join(scopes, " "))
	return connect.NewResponse(&reliantv1.CreateTokenResponse{
		Info: &reliantv1.TokenInfo{
			Id:          minted.TokenID,
			Name:        name,
			TokenPrefix: minted.DisplayPrefix,
			CreatedAt:   time.Now().UTC().Format(time.RFC3339),
			ExpiresAt:   formatTime(minted.ExpiresAt),
			Kind:        req.Msg.GetKind(),
			// What the token carries, as the authority reports it — for a
			// daemon credential, its resolved wildcard. Built by hand rather than through
			// tokenInfoProto because a mint returns Minted, not TokenInfo —
			// which is exactly why this field was missing while the list
			// reported it.
			Scopes: scopes,
		},
		Token: minted.Plaintext,
	}), nil
}

// ListTokens lists the caller's live tokens, optionally of one kind.
func (s *Service) ListTokens(
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
	tokens, err := s.deps.Authority.ListForUser(ctx, userID, scope)
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
func (s *Service) RevokeToken(
	ctx context.Context, req *connect.Request[reliantv1.RevokeTokenRequest],
) (*connect.Response[reliantv1.RevokeTokenResponse], error) {
	userID, _, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(req.Msg.GetId())
	if id == "" {
		return nil, svcerr.InvalidArgument("token id is required")
	}
	if err := s.deps.Authority.RevokeForUser(ctx, userID, id); err != nil {
		return nil, authorityError("revoke", err)
	}
	logging.InfoContext(ctx, "access token revoked", "user_id", userID, "token_id", id)
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
func (s *Service) UpdateToken(
	ctx context.Context, req *connect.Request[reliantv1.UpdateTokenRequest],
) (*connect.Response[reliantv1.UpdateTokenResponse], error) {
	userID, isMachine, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}
	if isMachine {
		return nil, svcerr.PermissionDenied("a machine credential cannot edit a credential; sign in to change a token's permissions")
	}
	id := strings.TrimSpace(req.Msg.GetId())
	if id == "" {
		return nil, svcerr.InvalidArgument("token id is required")
	}

	var name *string
	if req.Msg.Name != nil {
		trimmed := strings.TrimSpace(req.Msg.GetName())
		if trimmed == "" {
			return nil, svcerr.InvalidArgument("name cannot be blank")
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
			// fat's refusal names the scope it does not know: the caller's to fix.
			return nil, svcerr.InvalidArgument(err.Error())
		}
	}
	if name == nil && scopes == nil {
		return nil, svcerr.InvalidArgument("nothing to update: set name, or set scopes with the permissions to grant")
	}

	info, err := s.deps.Authority.UpdateForUser(ctx, userID, id, name, scopes)
	if err != nil {
		return nil, authorityError("update", err)
	}
	logging.InfoContext(ctx, "access token updated", "user_id", userID, "token_id", id,
		"renamed", name != nil, "rescoped", scopes != nil)
	return connect.NewResponse(&reliantv1.UpdateTokenResponse{Info: tokenInfoProto(info)}), nil
}
