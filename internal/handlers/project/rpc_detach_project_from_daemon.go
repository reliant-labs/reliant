package project

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// DetachProjectFromDaemon forgets the project's checkout on one machine and
// sends the machine nothing: its folder stays exactly as it is. The
// project_daemons row and the machine's synced config of the project go, and
// a detachment is recorded so the machine's next connect does not record the
// folder again (db.Repo.DetachProjectFromDaemon). Contrast
// RemoveProjectFromDaemon, which deletes the folder.
//
// What it refuses, each with FailedPrecondition and the way out:
//   - leaving the project on no machine. With no checkout recorded anywhere,
//     every machine is accepted for the project (daemonchoice.Validate) and the
//     plain-folder heal would make an empty twin of the folder on the next
//     machine to connect. Attach another machine first, or delete the project.
//   - workspaces (branch worktrees) of the project on the machine. They are git
//     worktrees of this very checkout, and their creation and cleanup run
//     against it there. Archive them first.
//   - a clone still landing on the machine, whose outcome would land unrecorded.
//
// Chats pinned to the machine are left pinned. Their next tool call in the
// main checkout is refused with projectcheckout's answer — the machines that
// do have the project, or attach it here — and nothing ran; switching the
// chat's machine, or attaching the folder again, continues the conversation.
//
// Idempotent: a machine the project is not on succeeds.
func (s *Service) DetachProjectFromDaemon(
	ctx context.Context,
	req *connect.Request[pb.DetachProjectFromDaemonRequest],
) (*connect.Response[pb.DetachProjectFromDaemonResponse], error) {
	userID := auth.MustGetUserID(ctx)
	projectID, daemonID := req.Msg.ProjectId, req.Msg.DaemonId
	if projectID == "" || daemonID == "" {
		return nil, svcerr.InvalidArgument("project_id and daemon_id are required")
	}
	project, err := s.deps.Repo.GetProjectWithUserCheck(ctx, projectID, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}
	if _, err := s.ownedMachine(ctx, userID, daemonID); err != nil {
		return nil, err
	}
	row, err := s.checkoutOn(ctx, project.ID, daemonID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return connect.NewResponse(&pb.DetachProjectFromDaemonResponse{}), nil
	}
	machine := s.machineName(ctx, daemonID)
	if row.InstallState == core.ProjectInstallInstalling {
		return nil, svcerr.FailedPrecondition(fmt.Sprintf("%q is still being cloned onto %s; wait for the clone to finish, then detach it", project.Name, machine))
	}
	if err := s.requireCheckoutElsewhere(ctx, userID, project, daemonID, machine); err != nil {
		return nil, err
	}
	if err := s.requireNoWorkspacesOn(ctx, project, daemonID, machine); err != nil {
		return nil, err
	}
	if err := s.deps.Repo.DetachProjectFromDaemon(ctx, project.ID, daemonID, row.Path); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to detach the project from the machine"))
	}
	return connect.NewResponse(&pb.DetachProjectFromDaemonResponse{}), nil
}

// requireCheckoutElsewhere refuses a detach that would leave the project with
// no installed checkout on another of the user's machines.
func (s *Service) requireCheckoutElsewhere(ctx context.Context, userID string, project *db.Project, daemonID, machine string) error {
	rows, err := daemonplacement.UserCheckouts(ctx, s.deps.Repo, userID, project.ID)
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to read the project's machines"))
	}
	for _, row := range rows {
		if row.DaemonID != daemonID {
			return nil
		}
	}
	return svcerr.FailedPrecondition(fmt.Sprintf(
		"%s is the only machine with %q; attach the project to another machine first, or delete the project", machine, project.Name))
}

// requireNoWorkspacesOn refuses a detach while live branch workspaces of the
// project are owned by the machine.
func (s *Service) requireNoWorkspacesOn(ctx context.Context, project *db.Project, daemonID, machine string) error {
	worktrees, err := s.deps.Repo.ListWorktrees(ctx, db.WorktreeFilters{ProjectID: &project.ID})
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to read the project's workspaces"))
	}
	n := 0
	for _, wt := range worktrees {
		if wt != nil && !wt.IsMain && wt.DeletedAt == nil && wt.DaemonID != nil && *wt.DaemonID == daemonID {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	noun := "workspaces"
	if n == 1 {
		noun = "workspace"
	}
	return svcerr.FailedPrecondition(fmt.Sprintf(
		"%d %s of %q live on %s; archive them before detaching the project from that machine", n, noun, project.Name, machine))
}
