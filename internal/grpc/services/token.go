// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/pat"
)

// TokenService is the Connect surface for managing user API tokens (api-kind
// rlnt_pat_ PATs). It is a thin wrapper over the single mint/list/revoke
// implementation in internal/pat.Service — it never hashes, validates, or
// stores tokens itself.
//
// It replaces the former POST/GET/DELETE /api/v1/tokens JSON handler. The auth
// interceptor accepts both Supabase JWTs and api-kind PATs on these
// procedures, so the "a PAT cannot mint a PAT" rule is enforced in
// CreateToken (see requireInteractiveSession); ListTokens and RevokeToken
// accept either credential and are strictly owner- and kind-scoped by the
// underlying service.
type TokenService struct {
	reliantv1connect.UnimplementedTokenServiceHandler
	patService *pat.Service
}

// NewTokenService constructs the api-token management service.
func NewTokenService(patService *pat.Service) *TokenService {
	return &TokenService{patService: patService}
}

// requireInteractiveSession enforces the JWT-only rule for token issuance: a
// PAT bearer can never mint another PAT. The interceptor has already validated
// whatever bearer is present (JWT or api-kind PAT) and populated the identity;
// here we re-inspect the raw Authorization header and reject PAT-format
// bearers. This mirrors the DaemonTokenService rule (which the gRPC
// interceptor enforces for that whole service) and the JWT-only middleware the
// deleted HTTP surface used.
func requireInteractiveSession(authHeader string) error {
	bearer := strings.TrimPrefix(authHeader, "Bearer ")
	if auth.IsPATFormat(bearer) {
		return connect.NewError(connect.CodeUnauthenticated,
			fmt.Errorf("token management requires an interactive session"))
	}
	return nil
}

// tokenInfoProto is the metadata view of an api-kind token. It never carries
// the raw secret or the stored hash.
func tokenInfoProto(p *db.DaemonPAT) *reliantv1.TokenInfo {
	info := &reliantv1.TokenInfo{
		Id:          p.ID,
		Name:        p.Name,
		TokenPrefix: p.TokenPrefix,
		CreatedAt:   p.CreatedAt.UTC().Format(time.RFC3339),
	}
	if p.LastUsedAt != nil {
		info.LastUsedAt = p.LastUsedAt.UTC().Format(time.RFC3339)
	}
	if p.ExpiresAt != nil {
		info.ExpiresAt = p.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if p.RevokedAt != nil {
		info.RevokedAt = p.RevokedAt.UTC().Format(time.RFC3339)
	}
	return info
}

// CreateToken mints a new api-kind PAT for the authenticated user. Session
// (JWT) authed only — a PAT cannot mint a PAT.
func (s *TokenService) CreateToken(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateTokenRequest],
) (*connect.Response[reliantv1.CreateTokenResponse], error) {
	if err := requireInteractiveSession(req.Header().Get("Authorization")); err != nil {
		return nil, err
	}

	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("unauthenticated"))
	}
	email, _ := auth.GetUserEmailFromContext(ctx)

	if req.Msg.GetTtlSeconds() < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("ttl_seconds must not be negative"))
	}

	raw, tok, err := s.patService.CreateAPIToken(ctx, userID, email, req.Msg.GetName(),
		time.Duration(req.Msg.GetTtlSeconds())*time.Second)
	if err != nil {
		// CreateAPIToken failures are user-facing validation errors (empty or
		// too-long name, duplicate active name, negative ttl) — the same cases
		// the deleted HTTP handler returned 400 for.
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	logging.Info("API token created", "user_id", userID, "token_id", tok.ID, "name", tok.Name)
	return connect.NewResponse(&reliantv1.CreateTokenResponse{
		Info:  tokenInfoProto(tok),
		Token: raw,
	}), nil
}

// ListTokens returns metadata for every api-kind token owned by the caller.
// Accepts either a JWT or an api-kind PAT (owner-scoped by the service).
func (s *TokenService) ListTokens(
	ctx context.Context,
	req *connect.Request[reliantv1.ListTokensRequest],
) (*connect.Response[reliantv1.ListTokensResponse], error) {
	_ = req
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("unauthenticated"))
	}

	toks, err := s.patService.ListAPITokens(ctx, userID)
	if err != nil {
		logging.Error("Failed to list api tokens", "user_id", userID, "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list tokens"))
	}

	out := make([]*reliantv1.TokenInfo, 0, len(toks))
	for _, tok := range toks {
		out = append(out, tokenInfoProto(tok))
	}
	return connect.NewResponse(&reliantv1.ListTokensResponse{Tokens: out}), nil
}

// RevokeToken marks one of the caller's api-kind tokens revoked. Accepts
// either a JWT or an api-kind PAT (owner- and kind-scoped by the service).
func (s *TokenService) RevokeToken(
	ctx context.Context,
	req *connect.Request[reliantv1.RevokeTokenRequest],
) (*connect.Response[reliantv1.RevokeTokenResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("unauthenticated"))
	}

	id := req.Msg.GetId()
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("token id is required"))
	}

	if err := s.patService.RevokeAPIToken(ctx, userID, id); err != nil {
		if errors.Is(err, pat.ErrTokenNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		logging.Error("Failed to revoke api token", "user_id", userID, "token_id", id, "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to revoke token"))
	}

	logging.Info("API token revoked", "user_id", userID, "token_id", id)
	return connect.NewResponse(&reliantv1.RevokeTokenResponse{}), nil
}
