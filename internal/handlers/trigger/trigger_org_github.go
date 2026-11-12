// Copyright (c) 2025 Reliant Labs

package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/ghdelegated"
	"github.com/reliant-labs/reliant/internal/triggers"
)

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
	if err != nil || out.ConnectionId != nil || t.Kind != core.TriggerKindIntegration {
		return out, err
	}
	var cfg core.IntegrationConfig
	if json.Unmarshal(t.Config, &cfg) != nil || cfg.Installation == "" {
		return out, nil
	}
	if id, err := strconv.ParseInt(cfg.Installation, 10, 64); err == nil && id > 0 {
		conn := ghdelegated.OrgAppConnectionID(id)
		out.ConnectionId = &conn
	}
	return out, nil
}
