// Copyright (c) 2025 Reliant Labs
package approval

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// An agent's propose_connection call raises a tool-call approval that asks
// the person to connect an integration (reliant #2831). The person approves
// it by making the connection in the app's connection dialog, which sends the
// new connection's id with Approve. This file checks that connection against
// the proposal before the run learns its id: it is the integration the card
// named, and its owner is one the proposal allows — the approver, or the
// chat's organization, which only an owner or admin of it may approve. The
// connection service already refused an org connection from anyone else when
// it was made; the check here is what stops an approver from naming an
// existing org connection they could not have made.

// ReasonConnectionRequired is the error reason for approving a connection
// proposal without the connection: it is approved by making the connection
// from its card (Approve with connection_id), one at a time.
const ReasonConnectionRequired = "connection_required"

// proposalToProto is the proposal p as the client reads it.
func proposalToProto(p *core.ConnectionProposal) *reliantv1.ConnectionProposal {
	out := &reliantv1.ConnectionProposal{
		IntegrationId:   p.IntegrationID,
		IntegrationName: p.IntegrationName,
		Reason:          p.Reason,
	}
	switch p.Scope {
	case core.ConnectionProposalPersonal:
		out.Scope = reliantv1.ConnectionProposalScope_CONNECTION_PROPOSAL_SCOPE_PERSONAL
	case core.ConnectionProposalOrg:
		out.Scope = reliantv1.ConnectionProposalScope_CONNECTION_PROPOSAL_SCOPE_ORG
	}
	if p.OrgID != "" {
		orgID := p.OrgID
		out.OrgId = &orgID
	}
	return out
}

// connectionRequired refuses approving a proposal without its connection.
func connectionRequired(p *core.ConnectionProposal) error {
	return svcerr.WithReason(svcerr.FailedPrecondition(
		"This request asks you to connect "+proposalName(p)+": approve it by making the connection from its card, on its own."),
		ReasonConnectionRequired)
}

func proposalName(p *core.ConnectionProposal) string {
	if p.IntegrationName != "" {
		return p.IntegrationName
	}
	return p.IntegrationID
}

// checkProposalConnection verifies that connection id, made by userID, is one
// proposal p may be approved with.
func (s *Service) checkProposalConnection(ctx context.Context, userID string, p *core.ConnectionProposal, id string) error {
	if s.deps.Connections == nil {
		return svcerr.Unavailable("connection proposals cannot be approved on this server")
	}
	wrong := svcerr.InvalidArgument("that is not a " + proposalName(p) + " connection this request can be approved with")
	conn, err := s.deps.Connections.Get(ctx, userID, id)
	if (err != nil || conn == nil) && p.OrgID != "" && p.Scope != core.ConnectionProposalPersonal {
		conn, err = s.deps.Connections.GetOrg(ctx, userID, p.OrgID, id)
	}
	if err != nil || conn == nil {
		return wrong
	}
	if conn.IntegrationID != p.IntegrationID {
		return wrong
	}
	owner := conn.Owner()
	switch owner.Kind {
	case core.ConnectionOwnerUser:
		if p.Scope == core.ConnectionProposalOrg || owner.ID != userID {
			return wrong
		}
		return nil
	case core.ConnectionOwnerOrg:
		if p.Scope == core.ConnectionProposalPersonal || p.OrgID == "" || owner.ID != p.OrgID {
			return wrong
		}
		// Only an org's owners and admins may give it a connection. This
		// holds even for an org connection that already exists: approving
		// with it hands it to the run as the org's choice.
		switch err := authz.RequireOrgAdmin(ctx, userID, p.OrgID); {
		case err == nil:
			return nil
		case errors.Is(err, authz.ErrMembershipUnavailable):
			return svcerr.Unavailable("could not check your role in the organization; try again")
		default:
			return svcerr.PermissionDenied("only an owner or admin of the organization can approve an org-wide connection; make a personal connection instead")
		}
	default:
		return wrong
	}
}

// withConnectionID is a's metadata with the approving connection's id added.
func withConnectionID(a *db.Approval, id string) (*string, error) {
	meta := map[string]any{}
	if a.Metadata != nil && *a.Metadata != "" {
		if err := json.Unmarshal([]byte(*a.Metadata), &meta); err != nil {
			return nil, err
		}
	}
	meta[core.ApprovalMetadataConnectionID] = id
	b, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	out := string(b)
	return &out, nil
}
