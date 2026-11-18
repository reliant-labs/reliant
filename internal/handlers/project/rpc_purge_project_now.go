package project

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// PurgeProjectNow deletes a project at once and for good, scheduled for
// deletion or not: what DeleteProject did before the restore window
// (project_delete.go). Only its owner may, naming it.
func (s *Service) PurgeProjectNow(
	ctx context.Context,
	req *connect.Request[pb.PurgeProjectNowRequest],
) (*connect.Response[pb.PurgeProjectNowResponse], error) {
	userID := auth.MustGetUserID(ctx)
	project, authority, err := s.deletableProject(ctx, userID, req.Msg.GetProjectId(), "delete")
	if err != nil {
		return nil, err
	}
	if authority != authz.ProjectDeleteOwner {
		return nil, svcerr.PermissionDenied("only the project's owner can delete it immediately; deleting it with DeleteProject purges it once its restore window ends")
	}
	if strings.TrimSpace(req.Msg.GetConfirmName()) != project.Name {
		return nil, svcerr.InvalidArgument(fmt.Sprintf("confirm_name must be the project's name, %q, to delete it immediately", project.Name))
	}
	var removals []*pb.ProjectMachineRemoval
	if req.Msg.GetRemoveFromMachines() {
		// Before the purge: it takes the rows that name the checkouts.
		checkouts, err := s.deps.Repo.ListProjectDaemonsForProject(ctx, project.ID)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list the project's machines"))
		}
		removals = s.removeFromMachines(ctx, userID, project, checkouts)
	}
	triggers, err := s.deps.Repo.PurgeProjectNow(ctx, project.ID, userID)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, svcerr.WithCause(svcerr.DeadlineExceeded(
				"the delete stopped part way: what is left of the project stays hidden, and is deleted when you run it again or by the next purge"), err)
		}
		if svcerr.IsNotFound(err) {
			return nil, core.ErrProjectNotFound
		}
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to delete project"))
	}
	s.convergeTriggerSchedules(ctx, triggers)
	logging.InfoContext(ctx, "[project] purged a project at its owner's request", "project_id", project.ID, "triggers", len(triggers))
	return connect.NewResponse(&pb.PurgeProjectNowResponse{MachineRemovals: removals}), nil
}
