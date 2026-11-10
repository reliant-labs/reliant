// Copyright (c) 2025 Reliant Labs

package trigger

import (
	"context"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// listScope is the org filter of a list (orgs.ListScope): nil when the
// request names no org or the deployment has none.
func (s *Service) listScope(ctx context.Context, userID, requested string) (*core.OrgScope, error) {
	if s.deps.Orgs == nil {
		return nil, nil
	}
	return s.deps.Orgs.ListScope(ctx, userID, requested, s.deps.Repo.AssignUnassignedOrg)
}

// triggerOrg is the org a new trigger belongs to (orgs.InProject): its
// project's when the project has one, else the org the request named, else
// the caller's default, else none. A default that cannot be resolved leaves
// the trigger with none (stamped later), so creating it never fails over an
// org the request did not name.
//
// A project of an org makes the trigger that org's, so a request that names
// none is treated as naming the project's org: the caller must be a live
// member of it (Named). Owning the project row is not authority in its org.
func (s *Service) triggerOrg(ctx context.Context, userID, requested string, project *core.Project) (*string, error) {
	named, def := "", ""
	if s.deps.Orgs != nil {
		var err error
		requested = strings.TrimSpace(requested)
		if requested == "" && project != nil && project.OrgID != nil {
			requested = *project.OrgID
		}
		if named, err = s.deps.Orgs.Named(ctx, userID, requested); err != nil {
			return nil, err
		}
		if named == "" && (project == nil || project.OrgID == nil) {
			if def, err = s.deps.Orgs.DefaultOrg(ctx, userID); err != nil {
				logging.WarnContext(ctx, "[trigger] could not resolve the default org; creating with none", "user_id", userID, "error", err)
				def = ""
			}
		}
	}
	var projectOrg *string
	if project != nil {
		projectOrg = project.OrgID
	}
	return orgs.InProject(projectOrg, named, def)
}

// updatedTriggerOrg is the org a trigger has after an update, which moves it
// to project target when target is not nil. Every update of a trigger with an
// org acts in that org, so the caller must be a live member of it, with a
// credential bound to it if they use one (orgs.Named), whether or not the
// update moves the trigger. A trigger's org is fixed, so a move to a project
// of another org is refused. A trigger with no org yet is resolved as the
// backfill would stamp it (its project's org, else the caller's default), and
// a move stamps it with that org, so the backfill cannot later give it the
// org of the project it left. A default that cannot be resolved fails closed
// (Unavailable). The write re-reads every org this decided on under the
// owner's org lock (db.UpdateTriggerInOrg), so a backfill in between refuses
// it rather than turning it into an update in, or a move to, another org.
func (s *Service) updatedTriggerOrg(ctx context.Context, userID string, existing *core.Trigger, source, target *core.Project) (*string, error) {
	from := existing.OrgID
	if from == nil && source != nil {
		from = source.OrgID
	}
	var to *string
	if target != nil {
		to = target.OrgID
	}
	if s.deps.Orgs == nil {
		if from != nil && to != nil && *from != *to {
			return nil, errTriggerCrossesOrgs
		}
		return existing.OrgID, nil
	}
	def := ""
	if from == nil || (target != nil && to == nil) {
		var err error
		if def, err = s.deps.Orgs.DefaultOrg(ctx, userID); err != nil {
			return nil, svcerr.WithCause(svcerr.Unavailable("could not resolve your default organization; try again"), err)
		}
	}
	effective := func(org *string) string {
		if org == nil {
			return def
		}
		return *org
	}
	org := effective(from)
	if org != "" {
		if _, err := s.deps.Orgs.Named(ctx, userID, org); err != nil {
			return nil, err
		}
	}
	if target == nil {
		return existing.OrgID, nil
	}
	if org != effective(to) {
		return nil, errTriggerCrossesOrgs
	}
	if org == "" {
		return existing.OrgID, nil
	}
	return &org, nil
}

// errTriggerCrossesOrgs refuses an update that would move a trigger to a
// project of another org.
var errTriggerCrossesOrgs = svcerr.FailedPrecondition("that project belongs to another organization; a trigger stays in its organization")
