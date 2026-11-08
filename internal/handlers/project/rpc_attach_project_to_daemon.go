package project

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitref"
	"github.com/reliant-labs/reliant/internal/ospath"
	"github.com/reliant-labs/reliant/internal/projectfacts"
)

// AttachProjectToDaemon records a folder already on one of the caller's
// machines as the project's checkout there. Unlike MarkProjectInstalled —
// bookkeeping for a clone another route made, which released clients call
// right after cloning and which checks nothing — it asks the machine first:
// a path that is missing or is a file is refused, so a typo never becomes a
// checkout that looks installed and points at nothing. That is why it is a
// new rpc rather than a check added to MarkProjectInstalled, whose callers
// expect it to succeed with no round trip to the machine.
//
// Refused as well:
//   - a folder that is already ANOTHER project's checkout on the machine
//     (AlreadyExists): one folder is one project per machine;
//   - a clone of the project still landing on the machine (FailedPrecondition):
//     its outcome would overwrite the attach.
//
// Re-attaching the machine at another path moves its checkout there; it also
// ends a detachment of the pair (UpsertProjectDaemon clears it).
func (s *Service) AttachProjectToDaemon(
	ctx context.Context,
	req *connect.Request[pb.AttachProjectToDaemonRequest],
) (*connect.Response[pb.AttachProjectToDaemonResponse], error) {
	userID := auth.MustGetUserID(ctx)
	projectID, daemonID, path := req.Msg.ProjectId, req.Msg.DaemonId, strings.TrimSpace(req.Msg.Path)
	if projectID == "" || daemonID == "" || path == "" {
		return nil, svcerr.InvalidArgument("project_id, daemon_id and path are required")
	}
	// ospath: the path is on the machine, which may be Windows while this
	// server is Linux.
	if !ospath.IsAbs(path) {
		return nil, svcerr.InvalidArgument(fmt.Sprintf("path must be absolute, got: %q", path))
	}
	var defaultBranch *string
	if req.Msg.DefaultBranch != nil && strings.TrimSpace(*req.Msg.DefaultBranch) != "" {
		branch := gitref.NormalizeBranchName(*req.Msg.DefaultBranch)
		if err := gitref.ValidateBranchName(branch); err != nil {
			return nil, svcerr.InvalidArgument(err.Error())
		}
		defaultBranch = &branch
	}

	project, err := s.deps.Repo.GetProjectWithUserCheck(ctx, projectID, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}
	if _, err := s.ownedMachine(ctx, userID, daemonID); err != nil {
		return nil, err
	}
	if other, err := s.projectAtPathOn(ctx, userID, daemonID, path); err != nil {
		return nil, err
	} else if other != nil && other.ID != project.ID {
		return nil, svcerr.AlreadyExists(fmt.Sprintf("%s is already the project %q on %s", path, other.Name, s.machineName(ctx, daemonID)))
	}
	prior, err := s.checkoutOn(ctx, project.ID, daemonID)
	if err != nil {
		return nil, err
	}
	if prior != nil && prior.InstallState == core.ProjectInstallInstalling {
		return nil, svcerr.FailedPrecondition(fmt.Sprintf("%q is still being cloned onto %s; wait for the clone to finish, or remove it from the machine first",
			project.Name, s.machineName(ctx, daemonID)))
	}
	if defaultBranch == nil && prior != nil {
		defaultBranch = prior.DefaultBranch
	}

	if err := s.requireFolder(ctx, userID, daemonID, path); err != nil {
		return nil, err
	}
	warnings := s.checkoutWarnings(ctx, userID, daemonID, project, path)

	if err := s.recordCheckout(ctx, userID, daemonID, project, path, defaultBranch); err != nil {
		return nil, err
	}
	// Learned here as after a clone (MarkProjectInstalled): the folder's
	// forge.yaml is on this machine's disk now.
	projectfacts.LearnForgeName(ctx, s.deps.Router, s.deps.Repo, userID, daemonID, project.ID, path)

	return connect.NewResponse(&pb.AttachProjectToDaemonResponse{
		ProjectDaemon: s.checkoutProto(ctx, justRecorded(project.ID, daemonID, path, defaultBranch)),
		Warnings:      warnings,
	}), nil
}
