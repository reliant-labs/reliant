// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
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
	// noOrgAccount is told of each pause RefuseNoOrgAccount makes, with the
	// model none of the org's accounts could run (WithNoOrgAccountPause).
	noOrgAccount func(ctx context.Context, triggerID, model string)
}

// WithNoOrgAccountPause has fn told of each pause for want of an org account,
// once, after it is written: the worker emails the org's owners and admins
// from it (control-plane docs/design/org-provider-accounts.md §11.4).
func (p *AutoPause) WithNoOrgAccountPause(fn func(ctx context.Context, triggerID, model string)) *AutoPause {
	p.noOrgAccount = fn
	return p
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
	// orgPolicyPausedDetail is a fire of a trigger paused because an org
	// policy refused its run (core.TriggerPausedOrgPolicy).
	orgPolicyPausedDetail = "trigger is paused: one of its organization's policies refused its run; resume it once the policy allows it"
	// noOrgAccountPrefix begins the detail of every firing refused because
	// none of its org's own accounts can run its model; it is how the next
	// such firing knows it is the second in a row.
	noOrgAccountPrefix = "This automation runs only on its organization's AI accounts, and none of them can run "
	// noOrgAccountPaused ends the detail of the firing that paused the
	// trigger for it.
	noOrgAccountPaused = " It failed this way twice in a row, so it was paused; resume it once an account can run it."
	// noOrgAccountPausedDetail is a fire of a trigger paused for it
	// (core.TriggerPausedNoOrgAccount).
	noOrgAccountPausedDetail = "trigger is paused: none of its organization's AI accounts could run its model; resume it once an admin adds one, or change its model"
)

// noOrgAccountDetail is the detail of a firing refused because none of its
// org's own accounts can run model (§11.4, I5): what is missing and who can
// fix it.
func noOrgAccountDetail(model string) string {
	return noOrgAccountPrefix + model + ". An admin can add an org API key or enable Reliant credits, or you can change the automation's model."
}

// FiringHistory reads a trigger's recorded firings. *db.Repo satisfies it.
type FiringHistory interface {
	// GetLatestTriggerEvent is the trigger's newest event with outcome,
	// nil (or core.ErrTriggerEventNotFound) when it has none.
	GetLatestTriggerEvent(ctx context.Context, triggerID string, outcome *core.TriggerEventOutcome) (*core.TriggerEvent, error)
}

// RefuseNoOrgAccount says what a firing of t records when its launch was
// refused because none of its org's own accounts can run its model
// (launch.ErrNoOrgAccount; control-plane docs/design/org-provider-accounts.md
// §11.4, I5). The firing is always recorded failed, and is never retried on
// a personal account. When the trigger's previous settled firing failed the
// same way, with no launch since, this second one pauses it, once, with
// core.TriggerPausedNoOrgAccount: a schedule would otherwise record the same
// failure every tick. The owner's inbox carries the failure, as for every
// failed firing; resuming the trigger resumes it.
//
// The error is a history read or the pause's write: the caller fails the
// firing retryably and records nothing. A nil AutoPause records the failure
// without pausing.
func (p *AutoPause) RefuseNoOrgAccount(ctx context.Context, history FiringHistory, t *core.Trigger, cause error) (string, error) {
	model := "this automation's model"
	var refused *launch.NoOrgAccountError
	if errors.As(cause, &refused) && refused.Model != "" {
		model = refused.Model
	}
	detail := noOrgAccountDetail(model)
	if p == nil {
		return detail, nil
	}
	again, err := previousFiringHadNoOrgAccount(ctx, history, t.ID)
	if err != nil {
		return "", fmt.Errorf("read the firings of trigger %s: %w", t.ID, err)
	}
	if !again {
		return detail, nil
	}
	paused, err := p.store.PauseTrigger(ctx, t.ID, core.TriggerPausedNoOrgAccount)
	if err != nil {
		return "", fmt.Errorf("pause trigger %s that no organization account can run: %w", t.ID, err)
	}
	if !paused {
		// Already paused: a concurrent firing paused it, or this is a
		// person's "run now" of the paused trigger.
		return detail, nil
	}
	logging.InfoContext(ctx, "[triggers] paused a trigger that none of its organization's accounts can run",
		"trigger_id", t.ID, "user_id", t.UserID, "org_id", ptr.From(t.OrgID), "model", model)
	if p.noOrgAccount != nil {
		notifyModel := ""
		if refused != nil {
			notifyModel = refused.Model
		}
		p.noOrgAccount(ctx, t.ID, notifyModel)
	}
	if p.schedules != nil {
		err = p.schedules.GetHandle(ctx, ScheduleID(t.ID)).Pause(ctx, client.SchedulePauseOptions{Note: noOrgAccountPausedDetail})
		if err != nil && !isNotFound(err) {
			logging.WarnContext(ctx, "[triggers] could not pause the schedule of a trigger paused for having no organization account; its fires skip until it converges",
				"trigger_id", t.ID, "error", err)
		}
	}
	return detail + noOrgAccountPaused, nil
}

// previousFiringHadNoOrgAccount reports whether trigger's newest failed
// firing was refused for having no org account and nothing has launched
// since: the firing being decided now is then the second in a row.
func previousFiringHadNoOrgAccount(ctx context.Context, history FiringHistory, triggerID string) (bool, error) {
	failed, launched := core.TriggerEventFailed, core.TriggerEventLaunched
	lastFailed, err := latestFiring(ctx, history, triggerID, &failed)
	if err != nil || lastFailed == nil || !strings.HasPrefix(lastFailed.OutcomeDetail, noOrgAccountPrefix) {
		return false, err
	}
	lastLaunched, err := latestFiring(ctx, history, triggerID, &launched)
	if err != nil {
		return false, err
	}
	return lastLaunched == nil || lastFailed.OccurredAt.After(lastLaunched.OccurredAt), nil
}

func latestFiring(ctx context.Context, history FiringHistory, triggerID string, outcome *core.TriggerEventOutcome) (*core.TriggerEvent, error) {
	ev, err := history.GetLatestTriggerEvent(ctx, triggerID, outcome)
	if errors.Is(err, core.ErrTriggerEventNotFound) {
		return nil, nil
	}
	return ev, err
}

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

// IntegrationRefused applies t's org's integrations policy to integration,
// the one t listens to (control-plane docs/design/org-policies.md §4.3, §5):
// true when the policy refuses it, and t is then paused, once, with
// core.TriggerPausedOrgPolicy, so routing and polling stop instead of
// recording a refusal on every event or tick. It resumes when its owner
// re-enables it. A nil AutoPause judges without pausing (the event is still
// not routed).
//
// The error is the policy that cannot be read where the point enforces (the
// caller retries; nothing is decided), or the pause's write (the caller still
// drops the event: the next one pauses again). A trigger of no org, an org
// with no integrations policy, and audit mode are never refused.
func (p *AutoPause) IntegrationRefused(ctx context.Context, t *core.Trigger, integration string) (bool, error) {
	d, err := orgpolicy.JudgeIntegration(ctx, ptr.From(t.OrgID), integration, false)
	if err != nil {
		return false, fmt.Errorf("read the integrations policy of trigger %s's organization: %w", t.ID, err)
	}
	if !d.Refused {
		return false, nil
	}
	if p == nil {
		return true, nil
	}
	paused, err := p.store.PauseTrigger(ctx, t.ID, core.TriggerPausedOrgPolicy)
	if err != nil {
		return true, fmt.Errorf("pause trigger %s whose integration its organization's policy does not allow: %w", t.ID, err)
	}
	if paused {
		logging.InfoContext(ctx, "[triggers] paused a trigger whose integration its organization's policy does not allow",
			"trigger_id", t.ID, "org_id", d.OrgID, "integration", integration, "policy_version", d.Version)
		if p.schedules != nil {
			err = p.schedules.GetHandle(ctx, ScheduleID(t.ID)).Pause(ctx, client.SchedulePauseOptions{Note: orgPolicyPausedDetail})
			if err != nil && !isNotFound(err) {
				logging.WarnContext(ctx, "[triggers] could not pause the schedule of a trigger paused for an org policy; its fires skip until it converges",
					"trigger_id", t.ID, "error", err)
			}
		}
	}
	return true, nil
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
	switch t.PausedReason {
	case core.TriggerPausedCreatorLeftOrg:
		return creatorLeftOrgPausedDetail
	case core.TriggerPausedOrgPolicy:
		return orgPolicyPausedDetail
	case core.TriggerPausedNoOrgAccount:
		return noOrgAccountPausedDetail
	}
	return "trigger is disabled"
}
