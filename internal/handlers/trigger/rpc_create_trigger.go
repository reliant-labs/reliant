package trigger

import (
	"context"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// CreateTrigger stores the trigger, then converges its schedule.
//
// The row is written first because it is the truth; Temporal is a projection
// of it. But a create whose Sync fails is rolled BACK, because the alternative
// is reporting success for a trigger that will never fire — and the owner has
// no way to tell that apart from one that is working. Update does not roll
// back: the previous definition is already live, and a failed converge there
// is drift that SyncAll repairs.
func (s *Service) CreateTrigger(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateTriggerRequest],
) (*connect.Response[reliantv1.CreateTriggerResponse], error) {
	userID := auth.MustGetUserID(ctx)

	def := req.Msg.Trigger
	if def == nil {
		return nil, svcerr.InvalidArgument("trigger is required")
	}

	trigger, err := s.triggerFromDefinition(ctx, userID, def, nil)
	if err != nil {
		return nil, err
	}
	if trigger.Kind == core.TriggerKindSchedule && s.deps.Syncer == nil {
		return nil, svcerr.Unavailable("the schedule backend is unavailable; cannot create a trigger that would never fire")
	}
	// Fixed at creation: every chat the trigger launches belongs to it.
	project, err := s.deps.Projects.GetProjectWithUserCheck(ctx, trigger.ProjectID, userID)
	if err != nil {
		return nil, triggerDBError("get project", err)
	}
	if trigger.OrgID, err = s.triggerOrg(ctx, userID, req.Msg.GetOrgId(), project); err != nil {
		return nil, err
	}
	if trigger.UnattendedFunding, err = s.unattendedFundingFor(ctx, userID, trigger.OrgID, def.UnattendedFunding, nil); err != nil {
		return nil, err
	}
	if err := s.bindOrgGitHubApp(ctx, userID, def, trigger); err != nil {
		return nil, err
	}
	trigger.ID = uuid.NewString()
	// nil enabled means true: a create that omits the field wants a working
	// trigger, not a disabled one.
	trigger.Enabled = def.Enabled == nil || *def.Enabled
	// Before the row: a trigger whose owner's access cannot be read is
	// refused, not stored unable to fire.
	if err := s.refreshTriggerAccess(ctx, trigger); err != nil {
		return nil, err
	}

	// Under the owner's org lock, only while the project's org this decision
	// read still holds.
	if err := s.deps.Repo.CreateTriggerInOrg(ctx, trigger, db.OrgRead{Table: "projects", ID: project.ID, Org: project.OrgID}); err != nil {
		return nil, triggerDBError("create trigger", err)
	}

	// A webhook trigger has no schedule: it fires when a delivery arrives.
	// What it has instead are credentials, written now that the row (whose
	// id they are bound to) exists. A failure removes the row: a webhook
	// trigger with no token could never be called.
	var cred *reliantv1.WebhookCredential
	if trigger.Kind == core.TriggerKindWebhook {
		// A managed webhook's token goes to its sender, never to the caller:
		// none is issued here, and Provision issues the one the control plane
		// gets. Until then (a trigger created paused) no token opens it.
		managed := s.managed(trigger)
		cred, err = s.writeWebhookCredentials(ctx, trigger, def, !managed)
		if err == nil && managed && trigger.Enabled {
			err = s.provisionAlerting(ctx, trigger)
		}
		if err != nil {
			if delErr := s.deps.Repo.DeleteTrigger(ctx, trigger.ID); delErr != nil {
				logging.ErrorContext(ctx, "failed to remove a webhook trigger whose credentials could not be written",
					"trigger_id", trigger.ID, "error", err, "delete_error", delErr)
			}
			return nil, err
		}
	}

	if err := s.sync(ctx, trigger.Kind, trigger.ID); err != nil {
		// Remove the schedule Sync may have half-created, then the row we
		// just wrote. Leaving either would mean an API that reported failure
		// for a trigger that still fires, or success for one with no schedule.
		// sync fails only through a Syncer, so there is one to ask.
		if s.deps.Syncer != nil {
			if schedErr := s.deps.Syncer.Delete(ctx, trigger.ID); schedErr != nil {
				logging.ErrorContext(ctx, "failed to remove the schedule of a trigger that could not be created",
					"trigger_id", trigger.ID, "sync_error", err, "delete_error", schedErr)
			}
		}
		if delErr := s.deps.Repo.DeleteTrigger(ctx, trigger.ID); delErr != nil {
			logging.ErrorContext(ctx, "failed to remove a trigger whose schedule could not be created",
				"trigger_id", trigger.ID, "sync_error", err, "delete_error", delErr)
		}
		return nil, triggerSyncError(err)
	}
	if trigger.Enabled {
		s.ensureGrants(ctx, trigger)
	}

	return connect.NewResponse(&reliantv1.CreateTriggerResponse{
		Trigger:        s.render(ctx, trigger),
		Webhook:        cred,
		FundingWarning: s.fundingWarning(ctx, trigger),
	}), nil
}
