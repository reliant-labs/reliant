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
	authority tokenauthority.Authority
}

// NewTokenService constructs the facade over authority.
func NewTokenService(authority tokenauthority.Authority) *TokenService {
	return &TokenService{authority: authority}
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

func kindForScopes(scopes []string) reliantv1.TokenKind {
	for _, s := range scopes {
		switch fat.Scope(s) {
		case fat.ScopeDaemonConnect:
			return reliantv1.TokenKind_TOKEN_KIND_DAEMON
		case fat.ScopeReliantAPI:
			return reliantv1.TokenKind_TOKEN_KIND_API
		}
	}
	return reliantv1.TokenKind_TOKEN_KIND_UNSPECIFIED
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
	logging.Info("access token created", "user_id", userID, "token_id", minted.TokenID, "scope", scope)
	return connect.NewResponse(&reliantv1.CreateTokenResponse{
		Info: &reliantv1.TokenInfo{
			Id:          minted.TokenID,
			Name:        name,
			TokenPrefix: minted.DisplayPrefix,
			CreatedAt:   time.Now().UTC().Format(time.RFC3339),
			ExpiresAt:   formatTime(minted.ExpiresAt),
			Kind:        req.Msg.GetKind(),
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
