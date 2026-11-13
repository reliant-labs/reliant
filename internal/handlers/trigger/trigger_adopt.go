// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
)

// The trigger half of the admin takeover of a departed member's automations
// (control-plane docs/design/org-sharing.md §8). AutomationTakeoverService
// (internal/handlers/automation_takeover) decides WHO may take a trigger and
// WHEN (an org admin, an accepted offer); this file is WHAT adoption does to
// the trigger, because it re-validates the trigger exactly as a create does,
// as its new owner.
//
// Adoption re-owns the trigger in place and never carries a credential. Each
// credential-bearing reference the old owner had (§8.4) is cleared or
// re-supplied:
//
//	connection_id            cleared; the new owner's named one or their default
//	daemon_id, worktree_id   cleared; the new owner's machine, pool or no_machine
//	project_id               the new owner's own project, in the trigger's org
//	webhook token            rotated: the old hash is cleared, a new token issued
//	webhook HMAC secret      cleared; the new owner supplies one (sealed as theirs)
//	presets                  re-resolved as the new owner's; one that is not refuses
//	params                   cleared; the new owner supplies their own values
//	provider account, scopes not stored on the trigger: runs mint for the owner
//
// The trigger is left disabled. SetTriggerEnabled, run by its new owner, is
// what turns it on, through every gate it always runs (project writable,
// setup complete, integration access) as them.

// errAdoptCrossesOrgs refuses a rebinding to a project outside the trigger's
// org: a trigger's org is fixed.
var errAdoptCrossesOrgs = svcerr.FailedPrecondition("choose one of your projects in this automation's organization; an automation stays in its organization")

// AdoptAs re-owns t, an orphan of its org, to adopterID with rb's
// references, as the acceptance of offerID when it is set. The caller has
// decided adopterID may (an org admin adopting to themselves, or the
// offeree of a live offer) and that t's creator left its org. Nothing is
// written unless every reference resolves as the adopter's; t must still be
// owned by the owner it was read with.
//
// The result is the trigger rendered for its new owner, a webhook's new
// token (shown once), and what they should know before enabling it.
func (s *Service) AdoptAs(ctx context.Context, t *core.Trigger, adopterID string, rb *reliantv1.TriggerRebinding, offerID string) (*reliantv1.AcceptTriggerOfferResponse, error) {
	if t == nil || t.OrgID == nil {
		return nil, svcerr.NotFound("trigger")
	}
	if rb == nil || rb.GetProjectId() == "" {
		return nil, svcerr.InvalidArgument("rebinding.project_id is required: the automation runs in one of your projects")
	}
	def, err := adoptionDefinition(t, rb)
	if err != nil {
		return nil, err
	}
	// Validated exactly as a create of the adopter's would be: their project,
	// their machine or pool, their worktree, their connection, the workflow
	// as it resolves for them.
	adopted, err := s.triggerFromDefinition(ctx, adopterID, def, t)
	if err != nil {
		return nil, err
	}
	project, err := s.deps.Repo.GetProjectWithUserCheck(ctx, adopted.ProjectID, adopterID)
	if err != nil {
		return nil, triggerDBError("get project", err)
	}
	if project.OrgID == nil || *project.OrgID != *t.OrgID {
		return nil, errAdoptCrossesOrgs
	}
	if err := s.resolvePresetsFor(ctx, adopterID, adopted); err != nil {
		return nil, err
	}
	if adopted.Kind == core.TriggerKindWebhook {
		cfg, err := triggers.WebhookConfigFor(adopted)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("could not read the webhook config"))
		}
		if cfg.HMAC != nil && rb.GetWebhookHmacSecret() == "" {
			return nil, svcerr.InvalidArgument("rebinding.webhook_hmac_secret is required: this webhook verifies signatures, and the old secret was sealed for its departed owner and is not carried. Choose a new one and give it to the sender")
		}
	}

	adopted.ID = t.ID
	adopted.CreatedAt = t.CreatedAt
	adopted.OrgID = t.OrgID
	adopted.Enabled = false
	adopted.PausedReason = ""
	// An org GitHub App installation the adopter names is validated and
	// recorded exactly as on create, now that the org is set: the rollout
	// gate, the installation linked to this org and live, the adopter a
	// member. The old owner's binding is not carried (the config was rebuilt
	// from the source), so without one the trigger listens on the adopter's
	// own access, as a new trigger of theirs would.
	if err := s.bindOrgGitHubApp(ctx, adopterID, def, adopted); err != nil {
		return nil, err
	}
	if err := s.deps.Repo.AdoptTrigger(ctx, adopted, t.UserID, offerID,
		db.OrgRead{Table: "projects", ID: project.ID, Org: project.OrgID}); err != nil {
		return nil, triggerDBError("adopt trigger", err)
	}
	logging.InfoContext(ctx, "[trigger] adopted an automation whose creator left its organization",
		"trigger_id", t.ID, "previous_owner", t.UserID, "new_owner", adopterID, "org_id", *t.OrgID, "offer_id", offerID)

	out := &reliantv1.AcceptTriggerOfferResponse{}
	if adopted.Kind == core.TriggerKindWebhook {
		out.Webhook, out.Notices = s.reissueWebhookCredentials(ctx, t, adopted, def)
	}
	if adopted.Kind == core.TriggerKindIntegration && t.ConnectionID != nil {
		out.Notices = append(out.Notices, "It now listens through your connection, not its previous owner's.")
	}
	// The schedule follows the row (paused); a failed converge is drift
	// SyncAll repairs, not a reason to report the adoption failed.
	if err := s.sync(ctx, adopted.Kind, adopted.ID); err != nil {
		logging.WarnContext(ctx, "[trigger] could not converge an adopted trigger's schedule; the startup sync repairs it",
			"trigger_id", adopted.ID, "error", err)
	}
	out.Notices = append(out.Notices, "It is paused. Turning it on checks it as yours: your project, your machine, and your access to everything it uses.")
	out.Trigger = s.render(ctx, adopted)
	return out, nil
}

// reissueWebhookCredentials gives an adopted webhook trigger credentials of
// its new owner's. The adoption already cleared the old token hash and
// secret; a plain webhook gets a new token (shown once), a managed one is
// disconnected from its alerts until its new owner enables it (which
// provisions a fresh token), and an HMAC secret is sealed under the new
// owner's vault tenant. A failure here does not undo the adoption: the
// trigger is paused and its new owner can rotate the token themselves.
func (s *Service) reissueWebhookCredentials(ctx context.Context, before, adopted *core.Trigger, def *reliantv1.TriggerDefinition) (*reliantv1.WebhookCredential, []string) {
	var notices []string
	var cred *reliantv1.WebhookCredential
	if s.managed(before) {
		s.retireAlerting(ctx, before.ID)
		notices = append(notices, "Its alerts are reconnected with a new token when you turn it on.")
	} else {
		var err error
		if cred, err = s.issueWebhookToken(ctx, adopted.ID); err != nil {
			logging.WarnContext(ctx, "[trigger] could not issue an adopted webhook trigger's token", "trigger_id", adopted.ID, "error", err)
			notices = append(notices, "Its old token no longer works and a new one could not be issued; rotate its token before turning it on.")
		} else {
			notices = append(notices, "Its old token no longer works. Give the sender the new URL or token shown now; it is not shown again.")
		}
	}
	if _, err := s.writeWebhookCredentials(ctx, adopted, def, false); err != nil {
		logging.WarnContext(ctx, "[trigger] could not store an adopted webhook trigger's signing secret", "trigger_id", adopted.ID, "error", err)
		notices = append(notices, "Its signing secret could not be stored; set it again by editing the automation before turning it on.")
	}
	return cred, notices
}

// adoptionDefinition is t's definition with rb in place of every reference
// that was its old owner's. What carries over is configuration with no
// credential in it: name, source, filter, workflow, prompt, notification and
// acting identity. Param values are the adopter's (rb.params), never the old
// owner's: a value is whatever its owner typed, an API token included.
func adoptionDefinition(t *core.Trigger, rb *reliantv1.TriggerRebinding) (*reliantv1.TriggerDefinition, error) {
	pt, err := triggers.ToProto(t, nil, nil, nil)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("could not read the trigger"))
	}
	disabled := false
	def := &reliantv1.TriggerDefinition{
		Name:       t.Name,
		ProjectId:  rb.GetProjectId(),
		WorktreeId: rb.WorktreeId,
		Enabled:    &disabled,
		Workflow:   t.Workflow,
		Presets:    map[string]string{},
		// Never the departed owner's values: they may be their tokens. The
		// adopter's own, validated as theirs by triggerFromDefinition.
		Params:            rb.GetParams(),
		Message:           t.Message,
		DaemonId:          rb.GetDaemonId(),
		NoMachine:         rb.GetNoMachine(),
		MachinePoolId:     rb.GetMachinePoolId(),
		NotifyOnComplete:  t.NotifyOnComplete,
		ConnectionId:      rb.ConnectionId,
		WebhookHmacSecret: rb.WebhookHmacSecret,
		ActingIdentity:    pt.GetActingIdentity(),
	}
	if rb.Workflow != nil {
		def.Workflow = rb.GetWorkflow()
	}
	presets := t.Presets
	if len(rb.GetPresets()) > 0 {
		presets = rb.GetPresets()
	}
	for group, name := range presets {
		def.Presets[group] = name
	}
	if t.WorkflowTrigger != nil {
		def.Source = &reliantv1.TriggerDefinition_WorkflowTrigger{WorkflowTrigger: *t.WorkflowTrigger}
		return def, nil
	}
	def.Filter = t.Filter
	switch src := pt.GetSource().(type) {
	case *reliantv1.Trigger_Schedule:
		def.Source = &reliantv1.TriggerDefinition_Schedule{Schedule: src.Schedule}
	case *reliantv1.Trigger_Webhook:
		def.Source = &reliantv1.TriggerDefinition_Webhook{Webhook: src.Webhook}
	case *reliantv1.Trigger_Integration:
		def.Source = &reliantv1.TriggerDefinition_Integration{Integration: src.Integration}
	case *reliantv1.Trigger_WorkflowEvent:
		def.Source = &reliantv1.TriggerDefinition_WorkflowEvent{WorkflowEvent: src.WorkflowEvent}
	default:
		return nil, svcerr.Internal(fmt.Sprintf("trigger %s has no source to adopt", t.ID))
	}
	return def, nil
}

// resolvePresetsFor refuses presets userID cannot resolve: presets are per
// user (presets.user_id), so the old owner's names may mean nothing, or
// something else, for the new owner. Resolved exactly as a launch resolves
// them (launch.ResolveRunPreset: theirs in the project, theirs, the
// project's, the builtins).
func (s *Service) resolvePresetsFor(ctx context.Context, userID string, t *core.Trigger) error {
	groups := make([]string, 0, len(t.Presets))
	for group := range t.Presets {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	for _, group := range groups {
		name := t.Presets[group]
		if name == "" {
			continue
		}
		if _, err := launch.ResolveRunPreset(ctx, s.deps.Repo, userID, t.ProjectID, name); err != nil {
			var storeErr *workflowsource.StoreError
			if errors.As(err, &storeErr) {
				return triggerDBError("resolve preset", err)
			}
			label := group
			if label == "" {
				label = "top level"
			}
			return svcerr.WithCause(svcerr.FailedPrecondition(fmt.Sprintf(
				"preset %q (%s) does not resolve for you: presets are per person, so choose one of yours in rebinding.presets", name, label)), err)
		}
	}
	return nil
}

// RenderDefinition renders t for someone who does not own it (an org
// admin listing orphans, a member offered it): the definition only. Param
// values are withheld (their names are returned), and so are the connection
// and the last firing's event payload, which can carry what the event
// carried. No secret is ever part of a rendered trigger.
func (s *Service) RenderDefinition(ctx context.Context, t *core.Trigger) (*reliantv1.Trigger, []string) {
	out := proto.Clone(s.render(ctx, t)).(*reliantv1.Trigger)
	names := make([]string, 0, len(t.Params))
	for name := range t.Params {
		names = append(names, name)
	}
	sort.Strings(names)
	out.Params = nil
	out.ConnectionId = nil
	out.Changes = nil
	if out.LastEvent != nil {
		out.LastEvent.Payload = nil
		out.LastEvent.Sender = nil
	}
	return out, names
}
