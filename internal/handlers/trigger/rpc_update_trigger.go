package trigger

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// UpdateTrigger replaces the definition and reconverges the schedule. The id,
// owner and kind are not updatable.
func (s *Service) UpdateTrigger(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateTriggerRequest],
) (*connect.Response[reliantv1.UpdateTriggerResponse], error) {
	existing, err := s.ownedTrigger(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}

	def := req.Msg.Trigger
	if def == nil {
		return nil, svcerr.InvalidArgument("trigger is required")
	}

	updated, err := s.triggerFromDefinition(ctx, existing.UserID, def, existing)
	if err != nil {
		return nil, err
	}
	if updated.Kind == core.TriggerKindSchedule && s.deps.Syncer == nil {
		return nil, svcerr.Unavailable("the schedule backend is unavailable")
	}
	updated.ID = existing.ID
	updated.CreatedAt = existing.CreatedAt
	// A trigger's org is fixed (no moving between orgs): it may move only to
	// a project of the same org, and only a caller acting in that org may
	// change it at all (updatedTriggerOrg).
	var source, target *core.Project
	if existing.OrgID == nil {
		if source, err = s.deps.Repo.GetProjectWithUserCheck(ctx, existing.ProjectID, existing.UserID); err != nil && !svcerr.IsNotFound(err) {
			return nil, triggerDBError("get project", err)
		}
	}
	if updated.ProjectID != existing.ProjectID {
		if target, err = s.deps.Repo.GetProjectWithUserCheck(ctx, updated.ProjectID, existing.UserID); err != nil {
			return nil, triggerDBError("get project", err)
		}
	}
	if updated.OrgID, err = s.updatedTriggerOrg(ctx, existing.UserID, existing, source, target); err != nil {
		return nil, err
	}
	if updated.UnattendedFunding, err = s.unattendedFundingFor(ctx, existing.UserID, updated.OrgID, def.UnattendedFunding, existing); err != nil {
		return nil, err
	}
	if err := s.bindOrgGitHubApp(ctx, existing.UserID, def, updated); err != nil {
		return nil, err
	}
	// The write re-reads, under the owner's org lock, every org this
	// decision read: a backfill between here and there refuses it.
	reads := []db.OrgRead{{Table: "triggers", ID: existing.ID, Org: existing.OrgID}}
	for _, p := range []*core.Project{source, target} {
		if p != nil {
			reads = append(reads, db.OrgRead{Table: "projects", ID: p.ID, Org: p.OrgID})
		}
	}
	// nil enabled on update means "leave it as it is": an update of the
	// schedule should not silently resume a trigger the owner paused.
	updated.Enabled = existing.Enabled
	if def.Enabled != nil {
		updated.Enabled = *def.Enabled
	}
	if err := s.refreshTriggerAccess(ctx, updated); err != nil {
		return nil, err
	}

	if err := s.deps.Repo.UpdateTriggerInOrg(ctx, updated, reads...); err != nil {
		return nil, triggerDBError("update trigger", err)
	}

	if updated.Kind == core.TriggerKindWebhook {
		// The token is untouched by an edit; only RotateWebhookToken
		// replaces it. The HMAC secret follows the definition.
		if _, err := s.writeWebhookCredentials(ctx, updated, def, false); err != nil {
			return nil, err
		}
		// A managed webhook follows the edit: re-provisioned while enabled
		// (its project, and so its scope, may have changed), removed while
		// paused. A failed provision leaves the receiver it already had.
		if s.managed(updated) {
			if updated.Enabled {
				if err := s.provisionAlerting(ctx, updated); err != nil {
					return nil, err
				}
			} else {
				s.retireAlerting(ctx, updated.ID)
			}
		}
	}
	// Unlike create, a failed converge here is not rolled back: the row is
	// the truth and is now correct, and SyncAll repairs Temporal at the next
	// startup. Reverting would throw away the user's edit to protect a
	// projection.
	if err := s.sync(ctx, updated.Kind, updated.ID); err != nil {
		return nil, triggerSyncError(err)
	}

	if updated.Enabled {
		s.ensureGrants(ctx, updated)
	}
	if existing.DaemonID != updated.DaemonID || !updated.Enabled {
		s.grants.releaseIfUnused(ctx, existing.UserID, existing.DaemonID)
	}
	return connect.NewResponse(&reliantv1.UpdateTriggerResponse{
		Trigger:        s.render(ctx, updated),
		FundingWarning: s.fundingWarning(ctx, updated),
	}), nil
}
