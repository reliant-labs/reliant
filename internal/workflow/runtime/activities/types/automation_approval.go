package types

import (
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// AutomationApprovalInput is authored only by the workflow runtime.
type AutomationApprovalInput struct {
	ChatID       string
	Thread       string
	ToolName     string
	ToolInput    string
	ToolCallID   string
	Unattended   bool
	Capabilities *pb.ToolCapabilities
}
type AutomationApprovalPreview struct {
	Title   string
	Hash    string
	Refusal string
	// NoApproval: the tool allows this call without a card
	// (AutomationApprovalTool.PrepareAutomation reported none needed).
	NoApproval bool `json:",omitempty"`
	// Kind says what sort of card this is: "" for an activation or machine
	// card, PreviewKindConnectionProposal for propose_connection's. The
	// runtime settles the card by its kind, never by which payload is set.
	Kind string `json:",omitempty"`
	// Connection is set for a propose_connection card: what it asks the
	// person to connect. The card is approved by making the connection.
	Connection *core.ConnectionProposal `json:",omitempty"`
}

// PreviewKindConnectionProposal is the Kind of a propose_connection card: it
// is approved by making a connection, whose id the run records for the call.
const PreviewKindConnectionProposal = "connection_proposal"
