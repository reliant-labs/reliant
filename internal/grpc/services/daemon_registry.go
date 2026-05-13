// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// DaemonRegistryService handles daemon registry queries (list/get/resolve/resume).
// Token CRUD lives in DaemonTokenService; PAT introspection lives in DaemonAuthService.
type DaemonRegistryService struct {
	reliantv1connect.UnimplementedDaemonRegistryServiceHandler
	database db.Repository
	router   toolexec.DaemonRouter
}

func NewDaemonRegistryService(database db.Repository, router toolexec.DaemonRouter) *DaemonRegistryService {
	return &DaemonRegistryService{database: database, router: router}
}

func (s *DaemonRegistryService) ListDaemons(
	ctx context.Context,
	req *connect.Request[reliantv1.ListDaemonsRequest],
) (*connect.Response[reliantv1.ListDaemonsResponse], error) {
	_ = req

	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	// Trigger daemon connection check and wait briefly for it to register
	// before querying the DB. No-op in cloud mode where the daemon is external.
	if s.router != nil {
		_, _ = s.router.IsDaemonOnline(ctx, userID)
	}

	daemons, err := s.database.ListDaemonsByUserID(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list daemons: %w", err))
	}

	resp := &reliantv1.ListDaemonsResponse{Daemons: make([]*reliantv1.DaemonInfo, 0, len(daemons))}
	for _, d := range daemons {
		resp.Daemons = append(resp.Daemons, daemonToProto(d))
	}

	return connect.NewResponse(resp), nil
}

func (s *DaemonRegistryService) GetDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.GetDaemonRequest],
) (*connect.Response[reliantv1.GetDaemonResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}
	if req.Msg.GetDaemonId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("daemon_id is required"))
	}

	daemon, err := s.database.GetDaemon(ctx, req.Msg.GetDaemonId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("daemon not found: %w", err))
	}
	if daemon.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("daemon not found"))
	}

	return connect.NewResponse(&reliantv1.GetDaemonResponse{Daemon: daemonToProto(daemon)}), nil
}

func (s *DaemonRegistryService) ResolveDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.ResolveDaemonRequest],
) (*connect.Response[reliantv1.ResolveDaemonResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	daemons, err := s.database.ListDaemonsByUserID(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing daemons: %w", err))
	}

	var best *db.Daemon
	for _, d := range daemons {
		if req.Msg.DaemonId != "" && d.ID != req.Msg.DaemonId {
			continue
		}
		if req.Msg.DaemonName != "" {
			name := ""
			if d.Hostname != nil {
				name = *d.Hostname
			}
			if name != req.Msg.DaemonName {
				continue
			}
		}
		// Prefer active daemons, but accept any match.
		if best == nil || d.Status == db.DaemonStatusActive {
			best = d
		}
		if d.Status == db.DaemonStatusActive {
			break
		}
	}

	if best == nil {
		return connect.NewResponse(&reliantv1.ResolveDaemonResponse{Found: false}), nil
	}

	return connect.NewResponse(&reliantv1.ResolveDaemonResponse{
		Daemon: daemonToProto(best),
		Found:  true,
	}), nil
}

func (s *DaemonRegistryService) ResumeDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.ResumeDaemonRequest],
) (*connect.Response[reliantv1.ResumeDaemonResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	if req.Msg.DaemonId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("daemon_id is required"))
	}

	// Verify ownership.
	daemon, err := s.database.GetDaemon(ctx, req.Msg.DaemonId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("daemon not found: %w", err))
	}
	if daemon.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("daemon not found"))
	}

	// If the daemon is already active, nothing to do.
	if daemon.Status == db.DaemonStatusActive {
		return connect.NewResponse(&reliantv1.ResumeDaemonResponse{Resumed: true}), nil
	}

	// For OSS, we can only report that the daemon is not active.
	// The control plane (commercial) can override this to actually wake cloud daemons.
	return connect.NewResponse(&reliantv1.ResumeDaemonResponse{
		Resumed:      false,
		ErrorMessage: fmt.Sprintf("daemon %s is %s; automatic resume not available in OSS mode", req.Msg.DaemonId, daemon.Status.String()),
	}), nil
}

func daemonToProto(d *db.Daemon) *reliantv1.DaemonInfo {
	if d == nil {
		return &reliantv1.DaemonInfo{}
	}

	info := &reliantv1.DaemonInfo{
		DaemonId: d.ID,
		UserId:   d.UserID,
		Status:   mapDaemonStatus(d.Status),
		Projects: projectPathsToDiscoveredProjects(d.ProjectPaths),
	}
	if d.Hostname != nil {
		info.Hostname = *d.Hostname
	}
	if d.Platform != nil {
		info.Platform = *d.Platform
	}
	if d.DaemonType != nil {
		info.DaemonType = *d.DaemonType
	}
	if d.ConnectedAt != nil {
		info.ConnectedAt = timestamppb.New(*d.ConnectedAt)
	}
	if d.LastHeartbeat != nil {
		info.LastHeartbeat = timestamppb.New(*d.LastHeartbeat)
	}
	return info
}

func mapDaemonStatus(status db.DaemonStatus) reliantv1.DaemonStatus {
	return status
}

func projectPathsToDiscoveredProjects(projectPathsJSON *string) []*reliantv1.DiscoveredProject {
	if projectPathsJSON == nil || *projectPathsJSON == "" {
		return nil
	}
	var paths []string
	if err := json.Unmarshal([]byte(*projectPathsJSON), &paths); err != nil {
		return nil
	}
	projects := make([]*reliantv1.DiscoveredProject, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		projects = append(projects, &reliantv1.DiscoveredProject{Path: path})
	}
	return projects
}
