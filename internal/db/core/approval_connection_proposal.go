package core

import "encoding/json"

// The approval metadata keys of a connection proposal: an agent's
// propose_connection call, which asks the person to connect an integration
// in the connection dialog. The proposal is written when the card is raised;
// the connection id when the person approves it by making the connection.
const (
	ApprovalMetadataConnectionProposal = "connection_proposal"
	ApprovalMetadataConnectionID       = "connection_id"
)

// The scopes a connection proposal names. An empty scope lets the approver
// choose: their own connection, or (an owner or admin of the chat's
// organization) the organization's.
const (
	ConnectionProposalPersonal = "personal"
	ConnectionProposalOrg      = "org"
)

// ConnectionProposal is what a propose_connection card asks for. It holds no
// credential and never will: the person enters one in the connection dialog.
type ConnectionProposal struct {
	IntegrationID   string `json:"integration_id"`
	IntegrationName string `json:"integration_name,omitempty"`
	Scope           string `json:"scope,omitempty"`
	OrgID           string `json:"org_id,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

// Key identifies proposals that ask for the same thing (integration, scope
// and organization, whatever the reason), so an identical one is not raised
// twice in a chat.
func (p ConnectionProposal) Key() string {
	return p.IntegrationID + "|" + p.Scope + "|" + p.OrgID
}

// ConnectionProposal is the proposal a's card asks for, or nil when a is not
// a connection proposal.
func (a *Approval) ConnectionProposal() *ConnectionProposal {
	var meta struct {
		Proposal *ConnectionProposal `json:"connection_proposal"`
	}
	if a == nil || a.Metadata == nil || json.Unmarshal([]byte(*a.Metadata), &meta) != nil {
		return nil
	}
	if meta.Proposal == nil || meta.Proposal.IntegrationID == "" {
		return nil
	}
	return meta.Proposal
}

// ApprovedConnectionID is the connection the person made to approve a's
// proposal, or "" when none is recorded.
func (a *Approval) ApprovedConnectionID() string {
	var meta struct {
		ConnectionID string `json:"connection_id"`
	}
	if a == nil || a.Metadata == nil || json.Unmarshal([]byte(*a.Metadata), &meta) != nil {
		return ""
	}
	return meta.ConnectionID
}
