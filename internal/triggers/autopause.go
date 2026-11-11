// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
	"fmt"

	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// AutoPause pauses a trigger whose creator is no longer a live member of its
// org. Every run a trigger launches executes as its creator, so once they
// have left (control-plane docs/design/orgs-and-members.md §5) no firing can
// launch: without the pause each one would be recorded as failed, forever.
// The first refused firing is recorded failed, the trigger is paused with
// core.TriggerPausedCreatorLeftOrg, and nothing fires again until the
// creator, a member again, resumes it (the trigger handler re-checks their
// membership).
//
// Only an answer pauses. A membership check that failed (control-plane
// unavailable) fails that one firing closed and retryably; it never pauses.
type AutoPause struct {
	store     TriggerPauser
	schedules ScheduleClient
}

// NewAutoPause pauses through store, and pauses the trigger's Temporal
// schedule through schedules (nil: the row only, and the next startup's
// SyncAll pauses the schedule; until then its fires skip on the row).
func NewAutoPause(store TriggerPauser, schedules ScheduleClient) *AutoPause {
	return &AutoPause{store: store, schedules: schedules}
}

// The outcome details of a firing refused because its creator left its org:
// the one that paused the trigger, one refused while no AutoPause is wired,
// and a fire of the trigger once it is paused (skipped).
const (
	creatorLeftOrgDetail       = "the trigger's creator is no longer a member of its organization, so it was paused; it fires again once they rejoin and resume it"
	creatorLeftOrgNoPause      = "the trigger's creator is no longer a member of its organization"
	creatorLeftOrgPausedDetail = "trigger is paused: its creator is no longer a member of its organization"
)

// Refuse pauses t, whose firing was refused because its creator left its
// org, and says what that firing records: failed, saying the trigger was
// paused, when this firing paused it (or, with no AutoPause wired, failed
// without pausing anything); skipped when a concurrent firing paused it
// first and recorded the failure, so a burst records one.
//
// The error is the pause's write: the caller fails the firing retryably and
// records nothing, its retry reaches the same refusal and pauses again.
// Pausing the schedule is best effort: a fire it lets through skips on the
// row's enabled flag, and SyncAll converges it.
func (p *AutoPause) Refuse(ctx context.Context, t *core.Trigger) (core.TriggerEventOutcome, string, error) {
	if p == nil {
		return core.TriggerEventFailed, creatorLeftOrgNoPause, nil
	}
	paused, err := p.store.PauseTrigger(ctx, t.ID, core.TriggerPausedCreatorLeftOrg)
	if err != nil {
		return "", "", fmt.Errorf("pause trigger %s whose creator left its organization: %w", t.ID, err)
	}
	if !paused {
		return core.TriggerEventSkipped, creatorLeftOrgPausedDetail, nil
	}
	logging.InfoContext(ctx, "[triggers] paused a trigger whose creator is no longer a member of its organization",
		"trigger_id", t.ID, "user_id", t.UserID, "org_id", ptr.From(t.OrgID))
	if p.schedules != nil {
		err = p.schedules.GetHandle(ctx, ScheduleID(t.ID)).Pause(ctx, client.SchedulePauseOptions{Note: creatorLeftOrgPausedDetail})
		if err != nil && !isNotFound(err) {
			logging.WarnContext(ctx, "[triggers] could not pause the schedule of a trigger paused for its creator leaving its organization; its fires skip until it converges",
				"trigger_id", t.ID, "error", err)
		}
	}
	return core.TriggerEventFailed, creatorLeftOrgDetail, nil
}

// CreatorLeftOrg reports whether t's creator is no longer a live member of
// t's org: authz's own answer for the trigger, asked for its creator. A
// trigger with no org, or a deployment with none, is never left. An outage
// is an error, so the firing fails closed and retries without deciding
// anything.
func CreatorLeftOrg(ctx context.Context, t *core.Trigger) (bool, error) {
	switch denial := authz.WriteTriggerDenial(ctx, t.UserID, t); {
	case denial == nil:
		return false, nil
	case errors.Is(denial, authz.ErrNotOrgMember):
		return true, nil
	case errors.Is(denial, authz.ErrMembershipUnavailable):
		return false, fmt.Errorf("check that trigger %s's creator is still a member of its organization: %w", t.ID, denial)
	default:
		// Its own creator is never otherwise denied; the launch decides.
		return false, nil
	}
}

// pausedReason is Trigger.paused_reason: the reason only while t is paused.
func pausedReason(t *core.Trigger) string {
	if t.Enabled {
		return ""
	}
	return t.PausedReason
}

// pausedDetail is why a fire of a paused trigger is skipped.
func pausedDetail(t *core.Trigger) string {
	if t.PausedReason == core.TriggerPausedCreatorLeftOrg {
		return creatorLeftOrgPausedDetail
	}
	return "trigger is disabled"
}
