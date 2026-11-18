// Copyright (c) 2025 Reliant Labs

package project

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/projectproto"
)

// Deleting a project with a restore window (control-plane
// docs/design/unified-shell-and-projects.md §11.3 "Delete a project", D18,
// D19, D20).
//
//   - DeleteProject schedules it: the project is hidden at once
//     (internal/authz reads a scheduled project as absent, and every list
//     leaves it and its chats out), its enabled triggers are paused with
//     core.TriggerPausedProjectDeleted, and the purge worker deletes it once
//     core.ProjectDeleteWindow has passed (internal/app ProjectPurge, over
//     the same batched store.DeleteUserProject the rpc used to run inline).
//   - RestoreProject cancels that until the purge begins, and turns back on
//     exactly the triggers the delete paused.
//   - PurgeProjectNow is the immediate, irreversible delete DeleteProject
//     used to be, for the owner who means it: what an old caller that
//     relied on the synchronous delete calls now.
//
// Old clients and the SDK keep calling DeleteProject and get the scheduled
// delete; its response still says success.
//
// Who (authz.ProjectDelete): the project's owner; or an admin of its org
// once the owner has left it. Removing the code from machines is the
// owner's alone, since the machines are theirs, and it is opt-in: a
// checkout can hold work nobody committed.

// ReasonProjectOwnerIsMember is the x-forge-error-reason of an org admin's
// delete of a project whose owner is still a member of its org.
const ReasonProjectOwnerIsMember = "project_owner_is_member"

// ReasonProjectScheduledForDeletion is the x-forge-error-reason of a create
// refused because a project of the caller's that is scheduled for deletion
// still holds the path or repository.
const ReasonProjectScheduledForDeletion = "project_scheduled_for_deletion"

// DeleteProject schedules the project's deletion (see the top of this file).
func (s *Service) DeleteProject(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteProjectRequest],
) (*connect.Response[reliantv1.DeleteProjectResponse], error) {
	userID := auth.MustGetUserID(ctx)
	project, authority, err := s.deletableProject(ctx, userID, req.Msg.GetProjectId(), "delete")
	if err != nil {
		return nil, err
	}
	var checkouts []*core.ProjectDaemon
	if req.Msg.GetRemoveFromMachines() {
		if authority != authz.ProjectDeleteOwner {
			return nil, errMachinesAreTheOwners
		}
		// Read before the delete: the fan-out after it acts on these rows.
		if checkouts, err = s.deps.Repo.ListProjectDaemonsForProject(ctx, project.ID); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list the project's machines"))
		}
	}

	// The write re-checks, under the owner's org lock, the owner and org this
	// authority was decided on: a project that moved org meanwhile is refused.
	// The machine fan-out below runs only after it succeeded.
	deletion, err := s.deps.Repo.ScheduleProjectDeletion(ctx, project.ID, userID, s.now().Add(core.ProjectDeleteWindow), project.Decision())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to delete project"))
	}
	s.convergeTriggerSchedules(ctx, deletion.PausedTriggerIDs)
	if !deletion.AlreadyScheduled {
		logging.InfoContext(ctx, "[project] scheduled a project's deletion", "project_id", project.ID, "owner", project.UserID,
			"deleted_by", userID, "as_org_admin", authority == authz.ProjectDeleteOrgAdmin,
			"delete_after", deletion.DeleteAfter, "triggers_paused", len(deletion.PausedTriggerIDs))
	}

	when := deletion.DeleteAfter.UTC().Format(time.RFC3339)
	resp := &reliantv1.DeleteProjectResponse{
		Success:     true,
		Message:     fmt.Sprintf("Project scheduled for deletion on %s; restore it until then", deletion.DeleteAfter.UTC().Format("2006-01-02 15:04 MST")),
		DeleteAfter: &when,
	}
	if req.Msg.GetRemoveFromMachines() {
		resp.MachineRemovals = s.removeFromMachines(ctx, userID, project, checkouts)
	}
	return connect.NewResponse(resp), nil
}

// errMachinesAreTheOwners refuses an org admin's request to remove a
// departed member's checkouts: their machines are not the admin's to act on.
var errMachinesAreTheOwners = svcerr.PermissionDenied(
	"only the project's owner can remove its code from their machines; delete it without removing the code")

// deletableProject reads the project, scheduled for deletion or not, and
// decides userID may delete or restore it (authz.ProjectDelete). action
// names the act in a person-only refusal.
func (s *Service) deletableProject(ctx context.Context, userID, projectID, action string) (*db.Project, authz.ProjectDeleteAuthority, error) {
	if projectID == "" {
		return nil, authz.ProjectDeleteNone, svcerr.InvalidArgument("project_id is required")
	}
	project, err := s.deps.Repo.GetProject(ctx, projectID)
	if err != nil {
		if svcerr.IsNotFound(err) {
			return nil, authz.ProjectDeleteNone, core.ErrProjectNotFound
		}
		return nil, authz.ProjectDeleteNone, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}
	authority, err := authz.ProjectDelete(ctx, userID, project)
	if err != nil {
		return nil, authz.ProjectDeleteNone, projectDeleteRefusal(ctx, err, action)
	}
	return project, authority, nil
}

// projectDeleteRefusal is the answer to a delete or restore authz refused.
func projectDeleteRefusal(ctx context.Context, err error, action string) error {
	switch {
	case errors.Is(err, authz.ErrMembershipUnavailable):
		return svcerr.WithCause(svcerr.Unavailable("could not verify organization membership; try again"), err)
	case errors.Is(err, authz.ErrPersonRequired):
		if refusal := auth.RequirePerson(ctx, action+" a project of a member who left the organization"); refusal != nil {
			return refusal
		}
		return svcerr.WithReason(svcerr.PermissionDenied(
			"only a person signed in to the Reliant app can "+action+" a project of a member who left the organization"), auth.ReasonPersonRequired)
	case errors.Is(err, authz.ErrOwnerIsMember):
		return svcerr.WithReason(svcerr.FailedPrecondition(
			"this project's owner is a member of its organization, so only they can "+action+" it"), ReasonProjectOwnerIsMember)
	default:
		// ErrDenied, ErrNotOrgMember: NotFound, as for a project that is
		// not there, with why as the withheld cause.
		return svcerr.WithCause(core.ErrProjectNotFound, err)
	}
}

// convergeTriggerSchedules converges the Temporal Schedules of the triggers
// a delete, restore or purge changed. A failure is drift the startup sync
// repairs (a paused trigger's fires skip on its row until then), never a
// reason to report the delete failed.
func (s *Service) convergeTriggerSchedules(ctx context.Context, triggerIDs []string) {
	if s.deps.Schedules == nil {
		return
	}
	for _, id := range triggerIDs {
		if err := s.deps.Schedules.Sync(ctx, id); err != nil {
			logging.WarnContext(ctx, "[project] could not converge a trigger's schedule after a project delete or restore; the startup sync repairs it",
				"trigger_id", id, "error", err)
		}
	}
}

// machineRemovalConcurrency bounds how many machines a fan-out asks at once.
const machineRemovalConcurrency = 4

// removeFromMachines removes project's checkout from each machine in
// checkouts (RemoveProjectFromDaemon's work, removeCheckout), a few at a
// time, and reports how each ended. A machine that is offline or refuses
// keeps its copy; nothing here fails the delete.
func (s *Service) removeFromMachines(ctx context.Context, userID string, project *db.Project, checkouts []*core.ProjectDaemon) []*reliantv1.ProjectMachineRemoval {
	out := make([]*reliantv1.ProjectMachineRemoval, len(checkouts))
	sem := make(chan struct{}, machineRemovalConcurrency)
	var wg sync.WaitGroup
	for i, pd := range checkouts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := &reliantv1.ProjectMachineRemoval{
				DaemonId:    pd.DaemonID,
				MachineName: s.machineName(ctx, pd.DaemonID),
				Path:        pd.Path,
				Status:      reliantv1.ProjectMachineRemovalStatus_PROJECT_MACHINE_REMOVAL_STATUS_REMOVED,
			}
			if err := s.removeCheckout(ctx, userID, project, pd); err != nil {
				r.Status = reliantv1.ProjectMachineRemovalStatus_PROJECT_MACHINE_REMOVAL_STATUS_FAILED
				if svcerr.IsUnavailable(err) {
					r.Status = reliantv1.ProjectMachineRemovalStatus_PROJECT_MACHINE_REMOVAL_STATUS_OFFLINE
				}
				r.Message = svcerr.ToConnect(err).Message()
				logging.InfoContext(ctx, "[project] a deleted project's checkout stays on a machine", "project_id", project.ID,
					"daemon_id", pd.DaemonID, "status", r.Status.String(), "error", err)
			}
			out[i] = r
		}()
	}
	wg.Wait()
	return out
}

// ownsDeleted reports whether userID owns the scheduled project p and may
// still act on it (restore or purge it).
func ownsDeleted(ctx context.Context, userID string, p *db.Project) bool {
	authority, err := authz.ProjectDelete(ctx, userID, p)
	return err == nil && authority == authz.ProjectDeleteOwner
}

// deletedProjectToProto is a scheduled project as the caller who may restore
// it reads it: the owner's whole project, or, for an org admin, the viewer's
// shape (without the departed owner's path).
func deletedProjectToProto(p *db.Project, authority authz.ProjectDeleteAuthority) *reliantv1.Project {
	out := projectproto.Project(p)
	if authority != authz.ProjectDeleteOwner {
		out = projectproto.ForViewer(out)
		out.ViewerAccess = false
	}
	return out
}
