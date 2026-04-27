// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/logging"
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
	localMode      bool // true when running in monolith/desktop mode with local FS access
}

// NewSystemService creates a new SystemService
func NewSystemService(database db.Repository, temporalClient client.Client, natsChecker func() bool, streamingHub streaming.StreamingHub, localMode bool) *SystemService {
	return &SystemService{
		db:             database,
		temporalClient: temporalClient,
		natsChecker:    natsChecker,
		streamingHub:   streamingHub,
		localMode:      localMode,
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
	if !s.localMode {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("OAuth sign-in requires local desktop mode"))
	}

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

// getAuthFilePath returns the path to the global auth file for the current OS.
func getAuthFilePath() (string, error) {
	return auth.CurrentAuthFilePath()
}

// DevAuthLoad loads the global auth session (development only)
func (s *SystemService) DevAuthLoad(
	ctx context.Context,
	req *connect.Request[reliantv1.DevAuthLoadRequest],
) (*connect.Response[reliantv1.DevAuthLoadResponse], error) {
	// Only allow in development mode
	if config.IsProductionEnvironment() {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("dev endpoints not available in production"))
	}
	if !s.localMode {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("DevAuth endpoints require local filesystem access (not available in cloud/distributed mode)"))
	}

	authFile, err := getAuthFilePath()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Check if file exists
	if _, err := os.Stat(authFile); os.IsNotExist(err) {
		return connect.NewResponse(&reliantv1.DevAuthLoadResponse{
			Success:     true,
			SessionJson: nil,
		}), nil
	}

	// Read the auth file
	data, err := os.ReadFile(authFile)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read auth file: %w", err))
	}

	// Validate JSON
	var session map[string]interface{}
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("invalid auth file: %w", err))
	}

	sessionJSON := string(data)
	return connect.NewResponse(&reliantv1.DevAuthLoadResponse{
		Success:     true,
		SessionJson: &sessionJSON,
	}), nil
}

// DevAuthSave saves the global auth session (development only)
func (s *SystemService) DevAuthSave(
	ctx context.Context,
	req *connect.Request[reliantv1.DevAuthSaveRequest],
) (*connect.Response[reliantv1.DevAuthSaveResponse], error) {
	// Only allow in development mode
	if config.IsProductionEnvironment() {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("dev endpoints not available in production"))
	}
	if !s.localMode {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("DevAuth endpoints require local filesystem access (not available in cloud/distributed mode)"))
	}

	authFile, err := getAuthFilePath()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Validate JSON
	var session map[string]interface{}
	if err := json.Unmarshal([]byte(req.Msg.SessionJson), &session); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid JSON: %w", err))
	}

	// Ensure directory exists
	authDir := filepath.Dir(authFile)
	if err := os.MkdirAll(authDir, 0755); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create auth directory: %w", err))
	}

	// Pretty print and write
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to marshal session: %w", err))
	}

	if err := os.WriteFile(authFile, data, 0644); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to write auth file: %w", err))
	}

	return connect.NewResponse(&reliantv1.DevAuthSaveResponse{
		Success: true,
	}), nil
}

// DevAuthClear clears the global auth session (development only)
func (s *SystemService) DevAuthClear(
	ctx context.Context,
	req *connect.Request[reliantv1.DevAuthClearRequest],
) (*connect.Response[reliantv1.DevAuthClearResponse], error) {
	// Only allow in development mode
	if config.IsProductionEnvironment() {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("dev endpoints not available in production"))
	}
	if !s.localMode {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("DevAuth endpoints require local filesystem access (not available in cloud/distributed mode)"))
	}

	authFile, err := getAuthFilePath()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Check if file exists
	if _, err := os.Stat(authFile); os.IsNotExist(err) {
		return connect.NewResponse(&reliantv1.DevAuthClearResponse{
			Success: true,
		}), nil
	}

	// Delete the file
	if err := os.Remove(authFile); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete auth file: %w", err))
	}

	logging.Info("[Dev Auth] Auth session cleared")

	return connect.NewResponse(&reliantv1.DevAuthClearResponse{
		Success: true,
	}), nil
}
