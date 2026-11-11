// Copyright (c) 2025 Reliant Labs

package workflow

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// listScope is the org filter of a list (orgs.ListScope): nil when the
// request names no org or the deployment has none.
func (s *Service) listScope(ctx context.Context, userID, requested string) (*core.OrgScope, error) {
	if s.deps.Orgs == nil {
		return nil, nil
	}
	return s.deps.Orgs.ListScope(ctx, userID, requested, s.deps.Repo.AssignUnassignedOrg)
}

// actingOrg is the org a create acts in (orgs.ActingOrg): nil when the
// deployment has none, or none could be resolved for a request that named
// none.
func (s *Service) actingOrg(ctx context.Context, userID, requested string) (*string, error) {
	if s.deps.Orgs == nil {
		return nil, nil
	}
	orgID, err := s.deps.Orgs.ActingOrg(ctx, userID, requested)
	if err != nil || orgID == "" {
		return nil, err
	}
	return &orgID, nil
}
