// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"os"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/auth"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/pat"
)

// DaemonTokenService is the JWT-authed surface for managing PATs.
// PATs themselves authenticate against DaemonAuthService / ToolsDaemonService,
// not this service — you bootstrap a PAT from a session, not from another PAT.
type DaemonTokenService struct {
	reliantv1connect.UnimplementedDaemonTokenServiceHandler
	patService *pat.Service
}

func NewDaemonTokenService(patService *pat.Service) *DaemonTokenService {
	return &DaemonTokenService{patService: patService}
}

func (s *DaemonTokenService) CreateDaemonToken(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateDaemonTokenRequest],
) (*connect.Response[reliantv1.CreateDaemonTokenResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	name := req.Msg.GetName()
	if name == "" {
		hostname, _ := os.Hostname()
		if hostname != "" {
			name = hostname
		} else {
			name = "daemon"
		}
	}

	rawToken, record, err := s.patService.CreatePAT(ctx, userID, name, false, nil)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create daemon token: %w", err))
	}

	return connect.NewResponse(&reliantv1.CreateDaemonTokenResponse{
		Token:   rawToken,
		TokenId: record.ID,
	}), nil
}

func (s *DaemonTokenService) ListDaemonTokens(
	ctx context.Context,
	req *connect.Request[reliantv1.ListDaemonTokensRequest],
) (*connect.Response[reliantv1.ListDaemonTokensResponse], error) {
	_ = req
	userID := auth.MustGetUserID(ctx)

	pats, err := s.patService.ListPATs(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing tokens: %w", err))
	}

	tokens := make([]*reliantv1.DaemonTokenInfo, 0, len(pats))
	for _, p := range pats {
		info := &reliantv1.DaemonTokenInfo{
			Id:          p.ID,
			Name:        p.Name,
			TokenPrefix: p.TokenPrefix,
			Ephemeral:   p.Ephemeral,
			CreatedAt:   p.CreatedAt.Format(time.RFC3339),
			Revoked:     p.RevokedAt != nil,
		}
		if p.LastUsedAt != nil {
			info.LastUsedAt = p.LastUsedAt.Format(time.RFC3339)
		}
		if p.ExpiresAt != nil {
			info.ExpiresAt = p.ExpiresAt.Format(time.RFC3339)
		}
		tokens = append(tokens, info)
	}

	return connect.NewResponse(&reliantv1.ListDaemonTokensResponse{
		Tokens: tokens,
	}), nil
}

func (s *DaemonTokenService) RevokeDaemonToken(
	ctx context.Context,
	req *connect.Request[reliantv1.RevokeDaemonTokenRequest],
) (*connect.Response[reliantv1.RevokeDaemonTokenResponse], error) {
	if req.Msg.TokenId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("token_id is required"))
	}

	// auth.MustGetUserID ensures the caller is authenticated
	_ = auth.MustGetUserID(ctx)

	if err := s.patService.RevokePAT(ctx, req.Msg.TokenId); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("revoking token: %w", err))
	}

	return connect.NewResponse(&reliantv1.RevokeDaemonTokenResponse{}), nil
}

// MintManagedDaemonToken provisions a PAT bound to an authoritative daemon_id
// on behalf of the control-plane operator. This RPC is internal-service-authed
// (NOT user-JWT-authed) — see interceptors.InternalServiceInterceptor, which
// gates this procedure on a token signed with the shared INTERNAL_SERVICE_SECRET.
// The handler therefore does not read a user ID from context; the operator
// supplies the owning user_id in the request.
func (s *DaemonTokenService) MintManagedDaemonToken(
	ctx context.Context,
	req *connect.Request[reliantv1.MintManagedDaemonTokenRequest],
) (*connect.Response[reliantv1.MintManagedDaemonTokenResponse], error) {
	userID := req.Msg.GetUserId()
	daemonID := req.Msg.GetDaemonId()
	if userID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("user_id is required"))
	}
	if daemonID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("daemon_id is required"))
	}

	name := req.Msg.GetName()
	if name == "" {
		name = "managed-daemon:" + daemonID
	}

	rawToken, patID, err := s.patService.CreatePATForDaemon(ctx, userID, daemonID, name, nil)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to mint managed daemon token: %w", err))
	}

	return connect.NewResponse(&reliantv1.MintManagedDaemonTokenResponse{
		Token:   rawToken,
		TokenId: patID,
	}), nil
}

// RevokeManagedDaemonToken revokes every live PAT bound to the given daemon_id.
// Internal-service-authed (see MintManagedDaemonToken).
func (s *DaemonTokenService) RevokeManagedDaemonToken(
	ctx context.Context,
	req *connect.Request[reliantv1.RevokeManagedDaemonTokenRequest],
) (*connect.Response[reliantv1.RevokeManagedDaemonTokenResponse], error) {
	daemonID := req.Msg.GetDaemonId()
	if daemonID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("daemon_id is required"))
	}

	count, err := s.patService.RevokeManagedDaemonPATs(ctx, daemonID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to revoke managed daemon tokens: %w", err))
	}

	return connect.NewResponse(&reliantv1.RevokeManagedDaemonTokenResponse{
		RevokedCount: int32(count),
	}), nil
}
