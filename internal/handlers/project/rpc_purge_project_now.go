package project

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// PurgeProjectNow deletes a project at once and for good, scheduled for
// deletion or not: what DeleteProject did before the restore window
// (project_delete.go). Only its owner may, naming it.
//
// The request decides and claims: the claim re-checks the owner's authority
// and makes the project hidden and unrestorable before this returns, and the
// machine fan-out runs after it, so both are answered here. The batches that
// delete the project's rows run after the response, detached from the
// request (startPurge): a project with large chats takes longer than a
// request may run, and a batch cut off by the request's deadline rolls back.
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
	var checkouts []*core.ProjectDaemon
	if req.Msg.GetRemoveFromMachines() {
		// Read before the purge, which takes the rows that name the
		// checkouts; removed once the claim re-checked this authority under
		// the owner's org lock.
		if checkouts, err = s.deps.Repo.ListProjectDaemonsForProject(ctx, project.ID); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list the project's machines"))
		}
	}
	decided := project.Decision()
	if err := s.deps.Repo.ClaimProjectPurgeNow(ctx, project.ID, decided); err != nil {
		if svcerr.IsNotFound(err) {
			return nil, core.ErrProjectNotFound
		}
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to delete project"))
	}
	var removals []*pb.ProjectMachineRemoval
	if req.Msg.GetRemoveFromMachines() {
		removals = s.removeFromMachines(ctx, userID, project, checkouts)
	}
	s.startPurge(ctx, project.ID, decided.OwnerUserID)
	return connect.NewResponse(&pb.PurgeProjectNowResponse{MachineRemovals: removals}), nil
}

// purgeNowBudget bounds an immediate purge's detached batches. It is shorter
// than the claim's lease (db.ProjectPurgeLease), so the purge worker, which
// retakes a claim only once the lease has passed, never runs beside them.
const purgeNowBudget = db.ProjectPurgeLease - 10*time.Minute

// startPurge runs the batches of the purge PurgeProjectNow claimed, detached
// from the request: auth values and tracing carry over, cancellation does
// not, and purgeNowBudget bounds it. The triggers it deleted have their
// schedules converged once it finishes. A purge that fails, or that a
// shutdown stops, leaves the project hidden and claimed, and the purge
// worker resumes it once the claim's lease has passed; so a failure is only
// logged. s.purges tracks the run, for the tests to wait on.
func (s *Service) startPurge(ctx context.Context, projectID, owner string) {
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), purgeNowBudget)
	s.purges.Add(1)
	go func() {
		defer s.purges.Done()
		defer cancel()
		triggers, err := s.deps.Repo.PurgeClaimedProject(bg, projectID, owner)
		if err != nil {
			logging.WarnContext(bg, "[project] an immediate purge stopped part way; the project stays hidden, and the purge worker finishes it once its claim's lease has passed",
				"project_id", projectID, "lease", db.ProjectPurgeLease, "error", err)
			return
		}
		s.convergeTriggerSchedules(bg, triggers)
		logging.InfoContext(bg, "[project] purged a project at its owner's request", "project_id", projectID, "triggers", len(triggers))
	}()
}
