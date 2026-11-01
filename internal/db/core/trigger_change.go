package core

import "time"

// TriggerChange is an owner-scoped configuration audit entry, not a firing.
type TriggerChange struct {
	ID          string
	UserID      string
	ChatID      string
	RunID       string
	ToolCallID  string
	Operation   string
	BeforeState string
	AfterState  string
	// Approval is how an agent's change was approved: "card",
	// "auto:<automation-approvals mode>" or "auto:pause". Empty for a change
	// the owner made in the app.
	Approval string
	// CapabilitiesAdded and CapabilitiesRemoved are the capability items an
	// agent's change granted and dropped (automationpolicy.Capabilities).
	CapabilitiesAdded   []string
	CapabilitiesRemoved []string
	CreatedAt           time.Time
}

// CapabilityChange is the stored form of a change's capability movement.
type CapabilityChange struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
}
