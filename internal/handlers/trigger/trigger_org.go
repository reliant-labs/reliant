// Copyright (c) 2025 Reliant Labs

package trigger

import (
	"context"

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
func (s *Service) triggerOrg(ctx context.Context, userID, requested string, project *core.Project) (*string, error) {
	named, def := "", ""
	if s.deps.Orgs != nil {
		var err error
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
