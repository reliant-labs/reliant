// Copyright (c) 2025 Reliant Labs
// Package connectionproposer is tools.ConnectionProposer, for the worker where
// propose_connection executes.
//
//forge:exclude-contract: implements the consumer-declared tools.ConnectionProposer in internal/llm/tools; a second derived Service interface would duplicate it
package connectionproposer

import (
	"context"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// Proposer is tools.ConnectionProposer for the worker, where
// propose_connection executes: a chat's earlier proposals from its approvals,
// the connection the person made from the connections table (metadata only;
// the credential is never read), and an org role asked of control-plane now.
type Proposer struct {
	approvals   chatApprovals
	connections ownedConnections
	roles       orgRoles
}

type chatApprovals interface {
	ListApprovalsByChat(ctx context.Context, chatID string) ([]*core.Approval, error)
}

type ownedConnections interface {
	GetOwnedConnection(ctx context.Context, owner core.ConnectionOwner, id string) (*core.Connection, error)
}

type orgRoles interface {
	RoleFresh(ctx context.Context, userID, orgID string) (role string, ok bool, err error)
}

// Deps is what the proposer reads.
type Deps struct {
	// Approvals are the chat's approval cards, its earlier proposals among them.
	Approvals chatApprovals
	// Connections reads the connection the person made (metadata only).
	Connections ownedConnections
	// Roles names a person's org role; nil makes no one an org admin.
	Roles orgRoles
}

var _ tools.ConnectionProposer = (*Proposer)(nil)

// New is the proposer over d.
func New(d Deps) *Proposer {
	return &Proposer{approvals: d.Approvals, connections: d.Connections, roles: d.Roles}
}

// IsOrgAdmin is true only for a role control-plane names owner or admin; an
// unknown role is never guessed upward (authz.RequireOrgAdmin's rule).
func (p *Proposer) IsOrgAdmin(ctx context.Context, userID, orgID string) (bool, error) {
	if p.roles == nil {
		return false, nil
	}
	role, known, err := p.roles.RoleFresh(ctx, userID, orgID)
	if err != nil {
		return false, err
	}
	return known && (role == "owner" || role == "admin"), nil
}

func (p *Proposer) ChatProposals(ctx context.Context, chatID string) ([]tools.ConnectionProposalState, error) {
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

func (p *Proposer) Connection(ctx context.Context, owner core.ConnectionOwner, id string) (*core.Connection, error) {
	return p.connections.GetOwnedConnection(ctx, owner, id)
}
