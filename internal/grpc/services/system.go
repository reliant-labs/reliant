// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/version"
)

// SystemService implements the SystemService RPC handlers
type SystemService struct {
	reliantv1connect.UnimplementedSystemServiceHandler
	db             db.Repository
	temporalClient client.Client
	natsChecker    func() bool // returns true if NATS is connected; nil means NATS not in use
	streamingHub   streaming.StreamingHub
}

// NewSystemService creates a new SystemService
func NewSystemService(database db.Repository, temporalClient client.Client, natsChecker func() bool, streamingHub streaming.StreamingHub) *SystemService {
	return &SystemService{
		db:             database,
		temporalClient: temporalClient,
		natsChecker:    natsChecker,
		streamingHub:   streamingHub,
	}
}

// Health returns the health status of the service
func (s *SystemService) Health(
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
func (s *SystemService) Ready(
	ctx context.Context,
	req *connect.Request[reliantv1.ReadyRequest],
) (*connect.Response[reliantv1.ReadyResponse], error) {
	var failures []string

	// Check database connectivity
	if s.db != nil {
		if err := s.db.Ping(ctx); err != nil {
			failures = append(failures, "db: "+err.Error())
		}
	}

	// Check Temporal connectivity
	if s.temporalClient != nil {
		_, err := s.temporalClient.CheckHealth(ctx, &client.CheckHealthRequest{})
		if err != nil {
			failures = append(failures, "temporal: "+err.Error())
		}
	}

	// Check NATS connectivity
	if s.natsChecker != nil && !s.natsChecker() {
		failures = append(failures, "nats: disconnected")
	}

	// Check streaming hub connectivity
	if s.streamingHub != nil && !s.streamingHub.IsConnected() {
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
func (s *SystemService) Info(
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
func (s *SystemService) Version(
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
func (s *SystemService) StartOAuthSignIn(
	ctx context.Context,
	req *connect.Request[reliantv1.StartOAuthSignInRequest],
) (*connect.Response[reliantv1.StartOAuthSignInResponse], error) {
	provider := req.Msg.Provider
	if provider == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("provider is required"))
	}

	result, err := auth.LoginWithOAuthProvider(ctx, provider, auth.LoginOptions{})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to complete %s OAuth sign-in: %w", provider, err))
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
func (s *SystemService) DevAuthLoad(
	ctx context.Context,
	req *connect.Request[reliantv1.DevAuthLoadRequest],
) (*connect.Response[reliantv1.DevAuthLoadResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("DevAuthLoad is not supported"))
}

// DevAuthSave saves the global auth session (development only)
func (s *SystemService) DevAuthSave(
	ctx context.Context,
	req *connect.Request[reliantv1.DevAuthSaveRequest],
) (*connect.Response[reliantv1.DevAuthSaveResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("DevAuthSave is not supported"))
}

// DevAuthClear clears the global auth session (development only)
func (s *SystemService) DevAuthClear(
	ctx context.Context,
	req *connect.Request[reliantv1.DevAuthClearRequest],
) (*connect.Response[reliantv1.DevAuthClearResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("DevAuthClear is not supported"))
}
