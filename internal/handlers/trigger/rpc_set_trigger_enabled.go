package trigger

import (
	"context"
	"errors"

	"github.com/reliant-labs/reliant/internal/authz"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// SetTriggerEnabled pauses or resumes a trigger.
func (s *Service) SetTriggerEnabled(
	ctx context.Context,
	req *connect.Request[reliantv1.SetTriggerEnabledRequest],
) (*connect.Response[reliantv1.SetTriggerEnabledResponse], error) {
	trigger, err := s.ownedTrigger(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	if trigger.Kind == core.TriggerKindSchedule && s.deps.Syncer == nil {
		return nil, svcerr.Unavailable("the schedule backend is unavailable")
	}
	if req.Msg.Enabled {
		if err := s.requireProjectWritable(ctx, trigger); err != nil {
			return nil, err
		}
		// Turning an activation back on is not a way around setup: its params
		// are checked against the workflow as it is now, because the workflow
		// may have gained a required input while it was paused.
		if err := s.requireSetupComplete(ctx, trigger); err != nil {
			return nil, err
		}
		enabling := *trigger
		enabling.Enabled = true
		if err := s.refreshTriggerAccess(ctx, &enabling); err != nil {
			return nil, err
		}
	}

	if err := s.deps.TriggerWrites.EnableTrigger(ctx, trigger.ID, req.Msg.Enabled); err != nil {
		return nil, triggerDBError("set trigger enabled", err)
	}
	trigger.Enabled = req.Msg.Enabled
	// A person pausing or resuming it is the reason now (the write cleared
	// paused_reason too).
	trigger.PausedReason = ""

	// A pushed trigger needs nothing converged — the receivers read
	// `enabled` off the row for every event — but a polled one pauses its
	// poll schedule.
	if err := s.sync(ctx, trigger.Kind, trigger.ID); err != nil {
		return nil, triggerSyncError(err)
	}
	// A managed webhook is connected while enabled and only then. Resuming
	// one that cannot be connected is refused, and the row put back: an
	// enabled trigger nothing delivers to looks working and is not.
	if s.managed(trigger) {
		if !trigger.Enabled {
			s.retireAlerting(ctx, trigger.ID)
		} else if err := s.provisionAlerting(ctx, trigger); err != nil {
			if revertErr := s.deps.TriggerWrites.EnableTrigger(ctx, trigger.ID, false); revertErr != nil {
				logging.ErrorContext(ctx, "could not pause a trigger whose alerts could not be connected",
					"trigger_id", trigger.ID, "error", err, "revert_error", revertErr)
			}
			return nil, err
		}
	}
	if trigger.Enabled {
		s.ensureGrants(ctx, trigger)
	} else {
		s.grants.releaseIfUnused(ctx, trigger.UserID, trigger.DaemonID)
	}
	return connect.NewResponse(&reliantv1.SetTriggerEnabledResponse{
		Trigger:        s.render(ctx, trigger),
		FundingWarning: s.fundingWarning(ctx, trigger),
	}), nil
}

// requireProjectWritable refuses to turn trigger on while the caller may not
// run work in its project: the check every launch of it meets. ownedTrigger
// decides on the trigger's own org, and a trigger written before orgs has
// none, so without this its creator could resume it after leaving the
// project's org, and each resume would buy one more refused, re-paused
// firing (triggers.AutoPause). An outage is Unavailable, never an answer.
func (s *Service) requireProjectWritable(ctx context.Context, trigger *core.Trigger) error {
	_, err := s.deps.Projects.GetProjectWithUserCheck(ctx, trigger.ProjectID, auth.MustGetUserID(ctx))
	switch {
	case err == nil:
		return nil
	case errors.Is(err, authz.ErrMembershipUnavailable):
		return orgs.LookupFailed(ctx, "could not verify the trigger's organization; try again", err)
	case errors.Is(err, core.ErrProjectNotFound):
		return svcerr.NotFound("trigger")
	default:
		return triggerDBError("get the trigger's project", err)
	}
}
