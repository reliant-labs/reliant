package types

import pb "github.com/reliant-labs/reliant/gen/reliant/v1"

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
}
