// Copyright (c) 2025 Reliant Labs
package app

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/credentials/connectionvault"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// connectionProposer is tools.ConnectionProposer for the worker, where
// propose_connection executes: a chat's earlier proposals from its approvals,
// the connection the person made from the connections table (metadata only;
// the credential is never read), and an org role asked of control-plane now.
type connectionProposer struct {
	approvals   chatApprovals
	connections ownedConnections
	roles       orgRoles
}

type chatApprovals interface {
	ListApprovalsByChat(ctx context.Context, chatID string) ([]*db.Approval, error)
}

type ownedConnections interface {
	GetOwnedConnection(ctx context.Context, owner core.ConnectionOwner, id string) (*core.Connection, error)
}

type orgRoles interface {
	RoleFresh(ctx context.Context, userID, orgID string) (role string, ok bool, err error)
}

func newConnectionProposer(repo *db.Repo, conns connectionvault.Service, roles orgRoles) tools.ConnectionProposer {
	return &connectionProposer{approvals: repo, connections: conns, roles: roles}
}

// IsOrgAdmin is true only for a role control-plane names owner or admin; an
// unknown role is never guessed upward (authz.RequireOrgAdmin's rule).
func (p *connectionProposer) IsOrgAdmin(ctx context.Context, userID, orgID string) (bool, error) {
	if p.roles == nil {
		return false, nil
	}
	role, known, err := p.roles.RoleFresh(ctx, userID, orgID)
	if err != nil {
		return false, err
	}
	return known && (role == "owner" || role == "admin"), nil
}

func (p *connectionProposer) ChatProposals(ctx context.Context, chatID string) ([]tools.ConnectionProposalState, error) {
	approvals, err := p.approvals.ListApprovalsByChat(ctx, chatID)
	if err != nil {
		return nil, err
	}
	var out []tools.ConnectionProposalState
	for _, a := range approvals {
		proposal := a.ConnectionProposal()
		if proposal == nil {
			continue
		}
		state := tools.ConnectionProposalState{
			Proposal: *proposal,
			Pending:  a.Status == int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING),
			Declined: a.Status == int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED),
		}
		if a.ResolvedAt != nil {
			state.ResolvedAt = *a.ResolvedAt
		} else if state.Declined {
			state.ResolvedAt = time.Now()
		}
		out = append(out, state)
	}
	return out, nil
}

func (p *connectionProposer) Connection(ctx context.Context, owner core.ConnectionOwner, id string) (*core.Connection, error) {
	return p.connections.GetOwnedConnection(ctx, owner, id)
}
