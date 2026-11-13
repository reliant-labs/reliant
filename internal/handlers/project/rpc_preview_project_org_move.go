package project

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
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
// The effects are reliant's half plus what control-plane holds (secret access
// to revoke, the hosted project staying behind, the target's funding, which
// machines are in the target), read through ProjectOrgMoveInternalService's
// PreviewProjectOrgMove with control_plane_effects_known true. Without that
// read (self-hosted, no internal-service identity, a control plane that
// predates it, or an outage) those fields stay unset and known is false: the
// preview shows them as unknown rather than failing.
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
	out := projectMoveEffectsProto(effects)
	if err := s.addControlPlaneEffects(ctx, userID, projectID, from, to, effects, out); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.PreviewProjectOrgMoveResponse{
		FromOrgId: from,
		ToOrgId:   to,
		Blockers:  projectMoveBlockersProto(blockers),
		Effects:   out,
	}), nil
}

// moveFactsTimeout bounds control-plane's preview read: past it the preview
// answers with those facts unknown rather than keep the dialog waiting.
var moveFactsTimeout = 5 * time.Second

// addControlPlaneEffects fills what control-plane holds into out, or leaves it
// unknown. A fresh refusal of the person's authority (no longer an admin of
// the source, or no longer a member of the target) is the preview's refusal
// too: reliant's own check read a cached membership.
func (s *Service) addControlPlaneEffects(ctx context.Context, userID, projectID, from, to string, e *core.ProjectOrgMoveEffects, out *pb.ProjectOrgMoveEffects) error {
	if s.deps.MoveFacts == nil {
		return nil
	}
	daemonIDs := make([]string, 0, len(e.Machines))
	for _, m := range e.Machines {
		daemonIDs = append(daemonIDs, m.DaemonID)
	}
	if len(daemonIDs) > maxMoveFactsDaemons {
		daemonIDs = daemonIDs[:maxMoveFactsDaemons]
	}
	readCtx, cancel := context.WithTimeout(ctx, moveFactsTimeout)
	defer cancel()
	facts, err := s.deps.MoveFacts.PreviewProjectOrgMove(readCtx, controlplane.ProjectOrgMovePreviewQuery{
		ProjectID: projectID, FromOrgID: from, ToOrgID: to, ActorUserID: userID,
		ForgeProjectName: e.ForgeProjectName, DaemonIDs: daemonIDs,
	})
	var refused *controlplane.ProjectOrgMoveError
	switch {
	case err == nil:
	case errors.As(err, &refused) && refused.Reason == controlplane.ReasonMoveSourceAdminRequired:
		return projectMoveRefusal(ctx, authz.ErrNotOrgAdmin)
	case errors.As(err, &refused) && refused.Reason == controlplane.ReasonMoveTargetMembershipMissing:
		return projectMoveRefusal(ctx, authz.ErrNotTargetMember)
	case errors.Is(err, controlplane.ErrProjectOrgMoveUnsupported):
		return nil
	default:
		logging.WarnContext(ctx, "could not read control-plane's half of a project move preview; showing it as unknown",
			"project_id", projectID, "to_org_id", to, "error", err)
		return nil
	}
	n := int32(facts.SecretAccessToRevoke)
	out.ControlPlaneEffectsKnown = true
	out.SecretAccessToRevoke = &n
	out.HostedProjectsStaying = facts.HostedProjectsStaying
	out.TargetFunded = facts.TargetFunded
	for _, m := range out.Machines {
		if in, ok := facts.InTargetOrg[m.DaemonId]; ok {
			m.InTargetOrg = &in
		}
	}
	return nil
}

// maxMoveFactsDaemons is control-plane's bound on the machines one preview
// asks about.
const maxMoveFactsDaemons = 200

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
