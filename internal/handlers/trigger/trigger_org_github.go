// Copyright (c) 2025 Reliant Labs

package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/ghdelegated"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// ReasonOrgInstallationTriggersDisabled is the reason a binding is refused
// while the deployment's rollout gate is off.
const ReasonOrgInstallationTriggersDisabled = "org_installation_triggers_disabled"

// InstallationBindingAllowed reports whether the deployment's org rollout
// gates (Deps.OrgRollout) let a trigger be bound to an org's GitHub App
// installation: only when orgs.github_installation_triggers_enabled is set.
// nil is every gate off.
func InstallationBindingAllowed(gates *configv1.OrgsConfig) bool {
	return gates != nil && gates.GetGithubInstallationTriggersEnabled()
}

// bindOrgGitHubApp records, in a GitHub trigger's config, the org GitHub App
// installation its definition names as its connection
// (ghdelegated.OrgAppConnectionID; org-sharing.md §7.1), so routing sends it
// only events from that installation. It runs once the trigger's org is
// decided, because the installation must be that org's: linked to it at
// control-plane and not suspended, with the owner a live member.
//
// Binding narrows and never widens: the event must still be about a
// repository the trigger's OWNER can see (access-gated routing), so linking an
// installation to an org tells nobody about anything they could not see.
// A definition that names no org installation leaves the trigger as it was
// before org installations: every installation the owner's access covers.
func (s *Service) bindOrgGitHubApp(ctx context.Context, ownerID string, def *reliantv1.TriggerDefinition, trigger *core.Trigger) error {
	if def == nil || def.ConnectionId == nil {
		return nil
	}
	installationID, ok := ghdelegated.ParseOrgAppConnectionID(*def.ConnectionId)
	if !ok {
		return nil
	}
	// Rollout gate: a release before installation binding routes a bound
	// trigger on its owner's access alone and drops the binding when it
	// re-projects it, so nothing is bound until every replica honours it.
	if !InstallationBindingAllowed(s.deps.OrgRollout) {
		return svcerr.WithReason(svcerr.FailedPrecondition("listening through an organization's GitHub App installation is not enabled on this deployment yet"), ReasonOrgInstallationTriggersDisabled)
	}
	if trigger.Kind != core.TriggerKindIntegration {
		return svcerr.InvalidArgument("an organization's GitHub App can only be the source of a GitHub trigger")
	}
	var cfg map[string]any
	if err := json.Unmarshal(trigger.Config, &cfg); err != nil || cfg == nil {
		return svcerr.InvalidArgument("the trigger's integration config is unreadable")
	}
	if cfg["integration"] != ghdelegated.IntegrationID {
		return svcerr.InvalidArgument("an organization's GitHub App can only be the source of a GitHub trigger")
	}
	if trigger.OrgID == nil || *trigger.OrgID == "" {
		return svcerr.FailedPrecondition("this trigger belongs to no organization, so it cannot listen through an organization's GitHub App")
	}
	org := *trigger.OrgID
	if err := authz.OrgMemberDenial(ctx, ownerID, org); err != nil {
		if errors.Is(err, authz.ErrMembershipUnavailable) {
			return svcerr.Unavailable("could not check organization membership; try again")
		}
		return svcerr.PermissionDenied("you are not a member of the organization whose GitHub App this trigger names")
	}
	if s.deps.OrgGitHubApps == nil {
		return svcerr.FailedPrecondition("this deployment has no organization GitHub Apps")
	}
	insts, err := s.deps.OrgGitHubApps.ListOrgGitHubInstallations(ctx, org)
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Unavailable("could not read the organization's GitHub App installations; try again"))
	}
	for _, in := range insts {
		if in.InstallationID != installationID {
			continue
		}
		if in.Suspended {
			return svcerr.FailedPrecondition("that GitHub App installation is suspended")
		}
		cfg[core.ActivationInstallationKey] = strconv.FormatInt(installationID, 10)
		raw, err := json.Marshal(cfg)
		if err != nil {
			return svcerr.Internal("encoding the trigger config")
		}
		trigger.Config = raw
		return nil
	}
	return svcerr.FailedPrecondition("that GitHub App installation is not linked to this trigger's organization")
}

// renderTrigger is triggers.ToProto plus the one field it cannot know: a
// GitHub trigger bound to its org's GitHub App installation names that
// installation as its connection, the id its definition was written with.
func renderTrigger(t *core.Trigger, nextFireAt *time.Time, firings []*core.TriggerEventWithRun, reg *core.TriggerRegistration) (*reliantv1.Trigger, error) {
	out, err := triggers.ToProto(t, nextFireAt, firings, reg)
	if err != nil || out.ConnectionId != nil {
		return out, err
	}
	out.ConnectionId = effectiveConnectionID(t)
	return out, nil
}

// effectiveConnectionID is the connection a trigger row listens through, as
// a definition names it: its saved connection, else the org GitHub App
// installation it is bound to (ghdelegated.OrgAppConnectionID), else nil. A
// path that carries a row's connection into a new definition must carry
// this, never ConnectionID alone: an installation binding has no
// ConnectionID, so dropping it would silently widen the trigger to every
// installation its owner can see.
func effectiveConnectionID(t *core.Trigger) *string {
	if t == nil {
		return nil
	}
	if t.ConnectionID != nil {
		id := *t.ConnectionID
		return &id
	}
	if t.Kind != core.TriggerKindIntegration {
		return nil
	}
	var cfg core.IntegrationConfig
	if json.Unmarshal(t.Config, &cfg) != nil || cfg.Installation == "" {
		return nil
	}
	if id, err := strconv.ParseInt(cfg.Installation, 10, 64); err == nil && id > 0 {
		conn := ghdelegated.OrgAppConnectionID(id)
		return &conn
	}
	return nil
}

// activationRow is the trigger row a definition makes, validated as its write
// will be, for a path that previews or checks one before writing it through
// CreateTrigger or UpdateTrigger (EnableWorkflow's first phase, an agent's
// activation). Like every path from a definition to a row, it binds the
// definition's org GitHub App installation (bindOrgGitHubApp), so a refusal
// is reported before anything is written and the row carries the binding
// the write will record. The org is the one that write decides: existing's
// for an existing trigger, else its project's (triggerOrg).
func (s *Service) activationRow(ctx context.Context, userID string, def *reliantv1.TriggerDefinition, existing *core.Trigger) (*core.Trigger, error) {
	row, err := s.triggerFromDefinition(ctx, userID, def, existing)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		row.OrgID = existing.OrgID
	} else if def.ConnectionId != nil && ghdelegated.IsOrgAppConnectionID(*def.ConnectionId) {
		// Only a binding needs the org this early; the write decides it for
		// everything else.
		project, err := s.deps.Repo.GetProjectWithUserCheck(ctx, row.ProjectID, userID)
		if err != nil {
			return nil, triggerDBError("get project", err)
		}
		if row.OrgID, err = s.triggerOrg(ctx, userID, "", project); err != nil {
			return nil, err
		}
	}
	if err := s.bindOrgGitHubApp(ctx, userID, def, row); err != nil {
		return nil, err
	}
	return row, nil
}
