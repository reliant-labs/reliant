// Copyright (c) 2025 Reliant Labs

package chat

import (
	"context"
	"strings"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// newChatOrgs is what a new chat's org is decided from (launch.Spec.OrgID and
// DefaultOrgID): the org the request names, verified, and, when it names
// none, the caller's default. A default that cannot be resolved is "": the
// chat takes its project's org, or none, and is stamped later (orgs.ListScope),
// so starting a chat never fails over an org the request did not name.
//
// A chat in a project that belongs to an org belongs to that org, so a
// request that names none is treated as naming the project's org: the caller
// must be a live member of it (Named), whatever client sent the request.
// Owning the project row is not authority in its org.
func (s *Service) newChatOrgs(ctx context.Context, userID, projectID, requested string) (named, def string, err error) {
	if s.deps.Orgs == nil {
		return "", "", nil
	}
	// Named trims what it is given, so " " names no org: normalize first, or
	// a blank org_id would skip the project's org and its membership check.
	requested = strings.TrimSpace(requested)
	if requested == "" && projectID != "" {
		project, err := s.deps.Repo.GetProjectWithUserCheck(ctx, projectID, userID)
		if err == nil && project.OrgID != nil {
			requested = *project.OrgID
		}
		// A missing project is the launcher's to report.
	}
	if named, err = s.deps.Orgs.Named(ctx, userID, requested); err != nil || named != "" {
		return named, "", err
	}
	def, err = s.deps.Orgs.DefaultOrg(ctx, userID)
	if err != nil {
		logging.WarnContext(ctx, "[chat] could not resolve the default org; the chat takes its project's or none", "user_id", userID, "error", err)
		return "", "", nil
	}
	return "", def, nil
}

// listScope is the org filter of a chat list (orgs.ListScope): nil when the
// request names no org.
func (s *Service) listScope(ctx context.Context, userID, requested string) (*core.OrgScope, error) {
	if s.deps.Orgs == nil {
		return nil, nil
	}
	return s.deps.Orgs.ListScope(ctx, userID, requested, s.deps.Repo.AssignUnassignedOrg)
}
