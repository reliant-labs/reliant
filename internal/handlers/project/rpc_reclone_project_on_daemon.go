package project

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// RecloneProjectOnDaemon blows away the existing clone and re-clones from the
// project's remote_url at the recorded default branch. Refuses to run when
// the project has no remote_url (non-git project — nothing to clone from).
func (s *Service) RecloneProjectOnDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.RecloneProjectOnDaemonRequest],
) (*connect.Response[reliantv1.RecloneProjectOnDaemonResponse], error) {
	userID := auth.MustGetUserID(ctx)
	project, pd, err := s.resolveProjectDaemonRow(ctx, req.Msg.ProjectId, req.Msg.DaemonId, userID)
	if err != nil {
		return nil, err
	}
	if project.RemoteURL == nil || *project.RemoteURL == "" {
		return nil, svcerr.FailedPrecondition("project has no remote_url; cannot reclone")
	}
	branch := ""
	if pd.DefaultBranch != nil {
		branch = *pd.DefaultBranch
	} else if project.DefaultBranch != nil {
		branch = *project.DefaultBranch
	}
	machine := s.machineName(ctx, req.Msg.DaemonId)
	op, done, err := s.beginTeardown(ctx, userID, project, pd, req.Msg.DaemonId, db.CheckoutTeardownReclone, machine)
	if err != nil {
		return nil, err
	}
	if done {
		// A retry of a reclone the machine has since finished.
		return connect.NewResponse(&reliantv1.RecloneProjectOnDaemonResponse{Path: pd.Path}), nil
	}
	var reply teardownReply
	payload := map[string]string{
		"path":   pd.Path,
		"repo":   *project.RemoteURL,
		"branch": branch,
		"op_id":  op.ID,
	}
	sendErr := teardownMachine{s: s, userID: userID, daemonID: req.Msg.DaemonId}.Send(ctx, "git.reclone", payload, &reply, teardownTimeoutMs)
	if err := s.settleTeardown(ctx, userID, op, project, machine, reply, sendErr); err != nil {
		return nil, err
	}
	// Refresh cloned_at — the row's path/branch are unchanged but the
	// "Last cloned/pulled" column should now show "just now".
	if err := s.deps.Repo.UpsertProjectDaemon(ctx, req.Msg.ProjectId, req.Msg.DaemonId, pd.Path, pd.DefaultBranch); err != nil {
		logging.WarnContext(ctx, "RecloneProjectOnDaemon: failed to refresh cloned_at", "error", err, "project_id", req.Msg.ProjectId, "daemon_id", req.Msg.DaemonId)
	}
	return connect.NewResponse(&reliantv1.RecloneProjectOnDaemonResponse{
		Path: pd.Path,
	}), nil
}
