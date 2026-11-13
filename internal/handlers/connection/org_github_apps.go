// Copyright (c) 2025 Reliant Labs
package connection

import (
	"context"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/ghdelegated"
)

// orgGitHubApps is the org's linked GitHub App installations as connections
// (org-sharing.md §7.1): synthesized, with no row and no secret, each named by
// ghdelegated.OrgAppConnectionID, the id a member's workflow step names to
// write as the org's bot. The caller has checked the requester is a live
// member of orgID. An unreachable control plane leaves them out rather than
// failing the list: they are an addition to the caller's own.
func (s *Service) orgGitHubApps(ctx context.Context, orgID, integrationID string) []*reliantv1.Connection {
	if s.deps.OrgGitHubApps == nil || (integrationID != "" && integrationID != ghdelegated.IntegrationID) {
		return nil
	}
	insts, err := s.deps.OrgGitHubApps.ListOrgGitHubInstallations(ctx, orgID)
	if err != nil {
		s.deps.Logger.WarnContext(ctx, "could not list the org's GitHub App installations; listing connections without them",
			"org_id", orgID, "error", err)
		return nil
	}
	out := make([]*reliantv1.Connection, 0, len(insts))
	for _, in := range insts {
		c := &reliantv1.Connection{
			Id: ghdelegated.OrgAppConnectionID(in.InstallationID), IntegrationId: ghdelegated.IntegrationID,
			AuthKind: reliantv1.ConnectionAuthKind_CONNECTION_AUTH_KIND_DELEGATED,
			Name:     "GitHub App: " + in.AccountLogin, AccountLabel: in.AccountLogin,
			Scopes: []string{}, Status: reliantv1.ConnectionStatus_CONNECTION_STATUS_ACTIVE,
			OwnerKind: reliantv1.ConnectionOwnerKind_CONNECTION_OWNER_KIND_ORG, OrgId: orgID,
		}
		if in.Suspended {
			c.Status, c.StatusReason = reliantv1.ConnectionStatus_CONNECTION_STATUS_NEEDS_REAUTH, "installation_suspended"
		}
		out = append(out, c)
	}
	return out
}
