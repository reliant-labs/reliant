package project

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Reasons a move is refused for, beyond PreviewProjectOrgMove's.
const (
	// ReasonProjectMovesDisabled: the deployment has not turned moves on
	// (orgs.project_moves_enabled).
	ReasonProjectMovesDisabled = "project_moves_disabled"
	// ReasonOrgChanged: the project is no longer in the org the caller saw
	// it in (expected_from_org_id).
	ReasonOrgChanged = "org_changed"
	// ReasonProjectBusy: work is running in the project.
	ReasonProjectBusy = "project_busy"
)

// MovesAllowed reports whether the deployment's org rollout gates let a
// project be moved to another org: only when orgs.project_moves_enabled is
// set. nil is every gate off.
func MovesAllowed(gates *configv1.OrgsConfig) bool {
	return gates != nil && gates.GetProjectMovesEnabled()
}

// moveWait is how long MoveProjectToOrg waits for its saga to commit or stop
// before answering with the move still under way; movePoll how often it
// looks.
var (
	moveWait = 15 * time.Second
	movePoll = 250 * time.Millisecond
)

// MoveProjectToOrg moves the project to req.ToOrgId (control-plane
// docs/design/org-project-move.md §3, §4). It decides as PreviewProjectOrgMove
// does (authz.ProjectOrgMove), refuses a stale source org and running work,
// records the move, and starts its saga (orgmove.ProjectOrgMoveWorkflow),
// which re-checks everything as it commits. A repeat of the caller's request
// key answers the move it made, wherever that move is now.
func (s *Service) MoveProjectToOrg(
	ctx context.Context,
	req *connect.Request[pb.MoveProjectToOrgRequest],
) (*connect.Response[pb.MoveProjectToOrgResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if !MovesAllowed(s.deps.OrgRollout) || s.deps.Moves == nil {
		return nil, svcerr.WithReason(svcerr.FailedPrecondition(
			"moving a project to another organization is not available on this deployment yet"), ReasonProjectMovesDisabled)
	}
	// A retried request is the move it made, shown as GetProjectOrgMove
	// would show it.
	if prior, err := s.deps.Repo.FindProjectOrgMoveByRequest(ctx, userID, req.Msg.GetRequestKey()); err == nil {
		if !canReadMove(ctx, userID, prior) {
			return nil, svcerr.NotFound("project move")
		}
		return s.followMove(ctx, prior)
	} else if !errors.Is(err, core.ErrProjectOrgMoveNotFound) {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read the move"))
	}

	projectID, to := req.Msg.GetProjectId(), req.Msg.GetToOrgId()
	project, err := s.deps.Repo.GetProject(ctx, projectID)
	if err != nil && !errors.Is(err, svcerr.ErrNotFound) {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to load the project"))
	}
	from, err := authz.ProjectOrgMove(ctx, userID, project, to)
	if err != nil {
		return nil, projectMoveRefusal(ctx, err)
	}
	if from != req.Msg.GetExpectedFromOrgId() {
		return nil, svcerr.WithReason(svcerr.FailedPrecondition(
			"the project is no longer in the organization you moved it from; reload and try again"), ReasonOrgChanged)
	}
	blockers, err := s.deps.Repo.ProjectOrgMoveBlockers(ctx, projectID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read the project's running work"))
	}
	if !blockers.Empty() {
		return nil, svcerr.WithReason(svcerr.FailedPrecondition(fmt.Sprintf(
			"work is running in this project (%d chats, %d runs, %d background processes); stop it, then move the project",
			blockers.ChatsTotal, blockers.LiveRuns, blockers.BackgroundProcesses)), ReasonProjectBusy)
	}
	// A project the backfill has not reached is in the owner's default org,
	// which authz resolved as from: stamp it there first, so the commit
	// decides on a real org rather than on a default that may change.
	if project.OrgID == nil || *project.OrgID == "" {
		if err := s.deps.Repo.AssignUnassignedOrg(ctx, project.UserID, from); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to assign the project its organization"))
		}
	}
	move, _, err := s.deps.Repo.InsertProjectOrgMove(ctx, &core.ProjectOrgMove{
		ID:          uuid.NewString(),
		ProjectID:   projectID,
		OwnerUserID: project.UserID,
		ActorUserID: userID,
		FromOrgID:   from,
		ToOrgID:     to,
		RequestKey:  req.Msg.GetRequestKey(),
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to record the move"))
	}
	return s.followMove(ctx, move)
}

// followMove starts the move's saga (a no-op once started) and waits up to
// moveWait for it to commit or stop, answering the move as it is then.
func (s *Service) followMove(ctx context.Context, move *core.ProjectOrgMove) (*connect.Response[pb.MoveProjectToOrgResponse], error) {
	if move.State.Open() {
		if err := s.deps.Moves.StartProjectOrgMove(ctx, move.ID); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Unavailable("could not start the move; try again"))
		}
	}
	deadline := time.Now().Add(moveWait)
	for !settled(move.State) && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, svcerr.Wrap(ctx.Err())
		case <-time.After(movePoll):
		}
		next, err := s.deps.Repo.GetProjectOrgMove(ctx, move.ID)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read the move"))
		}
		move = next
	}
	return connect.NewResponse(&pb.MoveProjectToOrgResponse{Move: projectOrgMoveProto(move)}), nil
}

// settled: the move committed (moved, completed) or stopped (aborted).
func settled(s core.ProjectOrgMoveState) bool {
	return s == core.ProjectOrgMoveStateMoved || s == core.ProjectOrgMoveStateCompleted || s == core.ProjectOrgMoveStateAborted
}

var moveStates = map[core.ProjectOrgMoveState]pb.ProjectOrgMoveState{
	core.ProjectOrgMoveStatePreparing: pb.ProjectOrgMoveState_PROJECT_ORG_MOVE_STATE_PREPARING,
	core.ProjectOrgMoveStatePrepared:  pb.ProjectOrgMoveState_PROJECT_ORG_MOVE_STATE_PREPARED,
	core.ProjectOrgMoveStateMoved:     pb.ProjectOrgMoveState_PROJECT_ORG_MOVE_STATE_MOVED,
	core.ProjectOrgMoveStateCompleted: pb.ProjectOrgMoveState_PROJECT_ORG_MOVE_STATE_COMPLETED,
	core.ProjectOrgMoveStateAborting:  pb.ProjectOrgMoveState_PROJECT_ORG_MOVE_STATE_ABORTING,
	core.ProjectOrgMoveStateAborted:   pb.ProjectOrgMoveState_PROJECT_ORG_MOVE_STATE_ABORTED,
}

func projectOrgMoveProto(m *core.ProjectOrgMove) *pb.ProjectOrgMove {
	c := m.Counts
	return &pb.ProjectOrgMove{
		Id:          m.ID,
		ProjectId:   m.ProjectID,
		OwnerUserId: m.OwnerUserID,
		ActorUserId: m.ActorUserID,
		FromOrgId:   m.FromOrgID,
		ToOrgId:     m.ToOrgID,
		State:       moveStates[m.State],
		Detail:      m.Detail,
		Counts: &pb.ProjectOrgMoveCounts{
			Chats: int32(c.Chats), Runs: int32(c.Runs), Triggers: int32(c.Triggers),
			TriggersPaused: int32(c.TriggersPaused), TriggersReenabled: int32(c.TriggersReenabled),
			SharesCleared: int32(c.SharesCleared), BindingsCleared: int32(c.BindingsCleared),
			SecretEntriesRevoked: int32(c.SecretEntriesRevoked),
		},
		CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: m.UpdatedAt.UTC().Format(time.RFC3339),
	}
}
