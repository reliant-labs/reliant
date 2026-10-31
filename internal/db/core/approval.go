package core

import "time"

// Approval is one approval request: a workflow step's, or one of a run's own tool calls'.
type Approval struct {
	ID                 string     `json:"id"`
	ChatID             string     `json:"chat_id"`
	ApprovalType       int32      // ApprovalType proto enum value
	EntityID           string     // content_block_id for tools, event_id for workflows
	Status             int32      `json:"status"` // ApprovalStatus proto enum value
	DenialReason       *string    `json:"denial_reason,omitempty"`
	ActionTaken        *string    `json:"action_taken,omitempty"` // Which action button was clicked (e.g., "Deploy Now")
	Title              string     `json:"title"`
	Metadata           *string    `json:"metadata,omitempty"`   // JSON object with type-specific data
	TemporalWorkflowID string     `json:"temporal_workflow_id"` // The actual Temporal execution ID for signaling (differs from WorkflowID for inline spawns)
	CreatedAt          time.Time  `json:"created_at"`
	ResolvedAt         *time.Time `json:"resolved_at,omitempty"`
	// ThreadID attributes the approval to the thread that raised it. Nil for
	// historical rows (not backfilled) and for approvals raised outside any
	// sub-agent thread.
	ThreadID *string `json:"thread_id,omitempty"`
	// BatchID groups the approvals one run step raised together (one turn's
	// tool calls), so they can be decided together. Nil for an approval
	// raised on its own.
	BatchID *string `json:"batch_id,omitempty"`
}
