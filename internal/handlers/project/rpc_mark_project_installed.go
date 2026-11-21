package project

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/projectfacts"
)

// MarkProjectInstalled records that a project has a clone on a daemon. The
// project must belong to the calling user; the daemon_id is trusted because
// the user-facing flow that calls this just successfully cloned through the
// gateway, which already authenticated the daemon binding. It checks nothing
// on the machine and is kept that way for the released clients that call it
// right after a clone; attaching a folder the user names is
// AttachProjectToDaemon, which does check.
func (s *Service) MarkProjectInstalled(
	ctx context.Context,
	req *connect.Request[reliantv1.MarkProjectInstalledRequest],
) (*connect.Response[reliantv1.MarkProjectInstalledResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ProjectId == "" || req.Msg.DaemonId == "" || req.Msg.Path == "" {
		return nil, svcerr.InvalidArgument("project_id, daemon_id, and path are required")
	}

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	var defaultBranch *string
	if req.Msg.DefaultBranch != nil && *req.Msg.DefaultBranch != "" {
		db := *req.Msg.DefaultBranch
		defaultBranch = &db
	}

	if err := s.deps.Repo.UpsertProjectDaemon(ctx, req.Msg.ProjectId, req.Msg.DaemonId, req.Msg.Path, defaultBranch); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to record project installation"))
	}

	// The caller just cloned onto this daemon, so forge.yaml is on its disk
	// now. Learn the project's forge name here rather than leaving the row
	// saying "not read yet" until someone opens the Forge tab with a daemon
	// online — see forge_name_learn.go. Non-forge repos are left unmarked,
	// and a failed read is never the RPC's failure: the install is recorded.
	projectfacts.LearnForgeName(ctx, s.deps.Router, s.deps.Repo,
		userID, req.Msg.DaemonId, req.Msg.ProjectId, req.Msg.Path)
	projectfacts.RefreshForgeApps(ctx, s.deps.Router, s.deps.Repo,
		userID, req.Msg.DaemonId, req.Msg.ProjectId, req.Msg.Path, 0)

	// Return the resolved row so the client can populate UI state without
	// a follow-up list call.
	pd := &db.ProjectDaemon{
		ProjectID:     req.Msg.ProjectId,
		DaemonID:      req.Msg.DaemonId,
		Path:          req.Msg.Path,
		DefaultBranch: defaultBranch,
		ClonedAt:      time.Now().UTC(),
	}
	return connect.NewResponse(&reliantv1.MarkProjectInstalledResponse{
		ProjectDaemon: projectDaemonToProto(pd),
	}), nil
}
