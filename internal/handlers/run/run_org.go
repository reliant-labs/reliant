// Copyright (c) 2025 Reliant Labs

package run

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
