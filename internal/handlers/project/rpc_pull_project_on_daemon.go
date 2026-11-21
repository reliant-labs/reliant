package project

import (
	"context"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/logging"
)

// PullProjectOnDaemon runs `git pull` for the project's clone on a daemon.
// The command is pinned to that daemon: the row's path is ITS path, and sent
// by default resolution it named a directory on whichever machine answered.
// Control-plane callers are expected to invoke this with the daemon's owner
// user identity.
func (s *Service) PullProjectOnDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.PullProjectOnDaemonRequest],
) (*connect.Response[reliantv1.PullProjectOnDaemonResponse], error) {
	userID := auth.MustGetUserID(ctx)
	_, pd, err := s.resolveProjectDaemonRow(ctx, req.Msg.ProjectId, req.Msg.DaemonId, userID)
	if err != nil {
		return nil, err
	}
	branch := ""
	if pd.DefaultBranch != nil {
		branch = *pd.DefaultBranch
	}
	var pullResp struct {
		Success bool   `json:"success"`
		Output  string `json:"output"`
		Error   string `json:"error"`
	}
	payload := map[string]string{"path": pd.Path, "branch": branch}
	if err := s.sendToDaemon(ctx, userID, req.Msg.DaemonId, "git.pull", payload, &pullResp); err != nil {
		return nil, daemonerr.Error("git pull", err)
	}
	// Refresh cloned_at so the admin UI shows "just now" after a successful
	// pull. Errors here are non-fatal — the pull already landed.
	if err := s.deps.Repo.UpsertProjectDaemon(ctx, req.Msg.ProjectId, req.Msg.DaemonId, pd.Path, pd.DefaultBranch); err != nil {
		logging.WarnContext(ctx, "PullProjectOnDaemon: failed to refresh cloned_at", "error", err, "project_id", req.Msg.ProjectId, "daemon_id", req.Msg.DaemonId)
	}
	return connect.NewResponse(&reliantv1.PullProjectOnDaemonResponse{
		Output: pullResp.Output,
	}), nil
}
