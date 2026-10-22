// Copyright (c) 2025 Reliant Labs
package system

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/version"
)

// Health returns the health status of the service
func (s *Service) Health(
	ctx context.Context,
	req *connect.Request[reliantv1.HealthRequest],
) (*connect.Response[reliantv1.HealthResponse], error) {
	resp := &reliantv1.HealthResponse{
		Status:  "ok",
		Version: "v2",
	}
	return connect.NewResponse(resp), nil
}

// Ready returns the readiness status of the service
func (s *Service) Ready(
	ctx context.Context,
	req *connect.Request[reliantv1.ReadyRequest],
) (*connect.Response[reliantv1.ReadyResponse], error) {
	var failures []string

	// Check database connectivity
	if s.deps.Repo != nil {
		if err := s.deps.Repo.Ping(ctx); err != nil {
			failures = append(failures, "db: "+err.Error())
		}
	}

	// Check Temporal connectivity
	if s.deps.Temporal != nil {
		_, err := s.deps.Temporal.CheckHealth(ctx, &client.CheckHealthRequest{})
		if err != nil {
			failures = append(failures, "temporal: "+err.Error())
		}
	}

	// Check NATS connectivity
	if s.deps.NATSConnected != nil && !s.deps.NATSConnected() {
		failures = append(failures, "nats: disconnected")
	}

	// Check streaming hub connectivity
	if s.deps.StreamingHub != nil && !s.deps.StreamingHub.IsConnected() {
		failures = append(failures, "nats-streaming: disconnected")
	}

	status := "ready"
	if len(failures) > 0 {
		status = "not_ready: " + strings.Join(failures, "; ")
	}

	resp := &reliantv1.ReadyResponse{
		Status:    status,
		Timestamp: time.Now().UTC().Unix(),
	}
	return connect.NewResponse(resp), nil
}

// Info returns general API information including version, build info, and worktree
func (s *Service) Info(
	ctx context.Context,
	req *connect.Request[reliantv1.InfoRequest],
) (*connect.Response[reliantv1.InfoResponse], error) {
	buildInfo := version.Get()

	// Extract worktree name from working directory
	// Pattern: ~/.reliant/worktrees/<repo_id>/<worktree-name>
	worktreeName := "dev"
	if wd, err := os.Getwd(); err == nil {
		worktreeName = filepath.Base(wd)
	}

	resp := &reliantv1.InfoResponse{
		Version:      buildInfo.Version,
		Commit:       buildInfo.Commit,
		Date:         buildInfo.Date,
		Branch:       buildInfo.Branch,
		Api:          "v2",
		Timestamp:    time.Now().UTC().Unix(),
		WorktreeName: worktreeName,
	}
	return connect.NewResponse(resp), nil
}

// Version returns detailed version information
func (s *Service) Version(
	ctx context.Context,
	req *connect.Request[reliantv1.VersionRequest],
) (*connect.Response[reliantv1.VersionResponse], error) {
	buildInfo := version.Get()

	resp := &reliantv1.VersionResponse{
		Version: buildInfo.Version,
		Commit:  buildInfo.Commit,
		Date:    buildInfo.Date,
		Branch:  buildInfo.Branch,
	}
	return connect.NewResponse(resp), nil
}

// StartOAuthSignIn completes the Electron-local OAuth sign-in flow for any provider.
func (s *Service) StartOAuthSignIn(
	ctx context.Context,
	req *connect.Request[reliantv1.StartOAuthSignInRequest],
) (*connect.Response[reliantv1.StartOAuthSignInResponse], error) {
	provider := req.Msg.Provider
	if provider == "" {
		return nil, svcerr.InvalidArgument("provider is required")
	}

	result, err := auth.LoginWithOAuthProvider(ctx, s.deps.OAuthProvider, provider)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal(fmt.Sprintf("failed to complete %s OAuth sign-in", provider)))
	}

	return connect.NewResponse(&reliantv1.StartOAuthSignInResponse{
		AccessToken:   result.AccessToken,
		RefreshToken:  result.RefreshToken,
		UserId:        result.UserID,
		Email:         result.Email,
		ProviderToken: result.ProviderToken,
	}), nil
}

// ============================================
// Dev Auth endpoints
// ============================================

// DevAuthLoad loads the global auth session (development only)
func (s *Service) DevAuthLoad(
	ctx context.Context,
	req *connect.Request[reliantv1.DevAuthLoadRequest],
) (*connect.Response[reliantv1.DevAuthLoadResponse], error) {
	return nil, svcerr.Unimplemented("DevAuthLoad is not supported")
}

// DevAuthSave saves the global auth session (development only)
func (s *Service) DevAuthSave(
	ctx context.Context,
	req *connect.Request[reliantv1.DevAuthSaveRequest],
) (*connect.Response[reliantv1.DevAuthSaveResponse], error) {
	return nil, svcerr.Unimplemented("DevAuthSave is not supported")
}

// DevAuthClear clears the global auth session (development only)
func (s *Service) DevAuthClear(
	ctx context.Context,
	req *connect.Request[reliantv1.DevAuthClearRequest],
) (*connect.Response[reliantv1.DevAuthClearResponse], error) {
	return nil, svcerr.Unimplemented("DevAuthClear is not supported")
}
