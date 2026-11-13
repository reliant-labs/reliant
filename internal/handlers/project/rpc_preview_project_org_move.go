package project

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Reasons (x-forge-error-reason) a project move, or its preview, is refused
// for, beyond NotFound (no such project, or not the caller's) and the
// person-only refusal every person-only action shares (auth.RequirePerson).
const (
	// ReasonOrgsUnavailable: the deployment has no organizations, or the
	// owner belongs to none.
	ReasonOrgsUnavailable = "orgs_unavailable"
	// ReasonSameOrg: the project is already in the organization named.
	ReasonSameOrg = "same_org"
	// ReasonSourceAdminRequired: the owner is not an admin or owner of the
	// project's organization, whose consent the move needs.
	ReasonSourceAdminRequired = "org_admin_required"
	// ReasonNotOrgMember: the owner is not a live member of an organization
	// the move names (the target, or the project's own).
	ReasonNotOrgMember = "not_org_member"
)

// PreviewProjectOrgMove says what moving the project to req.ToOrgId would do
// and what running work refuses it now (control-plane
// docs/design/org-project-move.md §2, §5). It writes nothing. The caller must
// be allowed to make the move itself (authz.ProjectOrgMove), so the preview
// never tells anyone about a project they could not move.
//
// The effects are reliant's half. What control-plane holds (secret access to
// revoke, hosted projects, the target's funding, which machines are in the
// target) is left unset with control_plane_effects_known false until the
// move's control-plane read exists.
func (s *Service) PreviewProjectOrgMove(
	ctx context.Context,
	req *connect.Request[pb.PreviewProjectOrgMoveRequest],
) (*connect.Response[pb.PreviewProjectOrgMoveResponse], error) {
	userID := auth.MustGetUserID(ctx)
	projectID, to := req.Msg.GetProjectId(), req.Msg.GetToOrgId()
	project, err := s.deps.Repo.GetProject(ctx, projectID)
	if err != nil && !errors.Is(err, svcerr.ErrNotFound) {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to load the project"))
	}
	from, err := authz.ProjectOrgMove(ctx, userID, project, to)
	if err != nil {
		return nil, projectMoveRefusal(ctx, err)
	}
	blockers, err := s.deps.Repo.ProjectOrgMoveBlockers(ctx, projectID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read the project's running work"))
	}
	effects, err := s.deps.Repo.ProjectOrgMoveEffects(ctx, projectID, to)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read what the move would change"))
	}
	return connect.NewResponse(&pb.PreviewProjectOrgMoveResponse{
		FromOrgId: from,
		ToOrgId:   to,
		Blockers:  projectMoveBlockersProto(blockers),
		Effects:   projectMoveEffectsProto(effects),
	}), nil
}

// projectMoveRefusal is the answer to a move authz refused.
func projectMoveRefusal(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, authz.ErrMembershipUnavailable):
		return svcerr.WithCause(svcerr.Unavailable("could not verify organization membership; try again"), err)
	case errors.Is(err, authz.ErrPersonRequired):
		// The same refusal, and reason, as every person-only action. The
		// edge installs both classifications together, so RequirePerson
		// refuses too; if it ever did not, the refusal must still stand.
		if refusal := auth.RequirePerson(ctx, "move a project to another organization"); refusal != nil {
			return refusal
		}
		return svcerr.WithReason(svcerr.PermissionDenied(
			"only a person signed in to the Reliant app can move a project to another organization"), auth.ReasonPersonRequired)
	case errors.Is(err, authz.ErrMoveNeedsOrgs):
		return svcerr.WithReason(svcerr.FailedPrecondition(
			"there are no organizations to move this project between"), ReasonOrgsUnavailable)
	case errors.Is(err, authz.ErrMoveSameOrg):
		return svcerr.WithReason(svcerr.InvalidArgument(
			"the project is already in that organization"), ReasonSameOrg)
	case errors.Is(err, authz.ErrNotOrgAdmin):
		return svcerr.WithReason(svcerr.PermissionDenied(
			"only an admin or owner of the project's organization can move a project out of it; ask one of them to make you an admin"), ReasonSourceAdminRequired)
	case errors.Is(err, authz.ErrNotTargetMember):
		return svcerr.WithReason(svcerr.PermissionDenied(
			"you are not a member of the organization you asked to move the project to"), ReasonNotOrgMember)
	case errors.Is(err, authz.ErrNotOrgMember):
		return svcerr.WithReason(svcerr.PermissionDenied(
			"you are no longer a member of the project's organization"), ReasonNotOrgMember)
	default:
		return svcerr.NotFound("project")
	}
}

func projectMoveBlockersProto(b *core.ProjectOrgMoveBlockers) *pb.ProjectOrgMoveBlockers {
	out := &pb.ProjectOrgMoveBlockers{
		ChatsTotal:          int32(b.ChatsTotal),
		LiveRuns:            int32(b.LiveRuns),
		BackgroundProcesses: int32(b.BackgroundProcesses),
	}
	for _, c := range b.Chats {
		out.Chats = append(out.Chats, &pb.ProjectOrgMoveBlockedChat{
			ChatId: c.ChatID, Title: c.Title, Activity: pb.ChatActivity(c.Activity),
		})
	}
	return out
}

func projectMoveEffectsProto(e *core.ProjectOrgMoveEffects) *pb.ProjectOrgMoveEffects {
	out := &pb.ProjectOrgMoveEffects{
		Chats:              int32(e.Chats),
		Runs:               int32(e.Runs),
		Triggers:           int32(e.Triggers),
		TriggersToPause:    int32(e.TriggersToPause),
		SharesToClear:      int32(e.SharesToClear),
		OrgBindingsToClear: int32(e.OrgBindingsToClear),
		ForgeProjectName:   e.ForgeProjectName,
	}
	for _, t := range e.TriggersNeedingSetup {
		out.TriggersNeedingSetup = append(out.TriggersNeedingSetup, &pb.ProjectOrgMoveTriggerSetup{
			TriggerId: t.TriggerID, Name: t.Name, Reason: t.Reason,
		})
	}
	for _, m := range e.Machines {
		out.Machines = append(out.Machines, &pb.ProjectOrgMoveMachine{
			DaemonId: m.DaemonID, Name: m.Name, Attached: m.Attached, PinnedChats: int32(m.PinnedChats),
		})
	}
	return out
}
