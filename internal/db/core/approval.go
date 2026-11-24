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
	// DecidedBy is the person who approved or denied; nil on a decided row
	// means the chat's owner (every row decided before the column), and it
	// is nil while pending or when the system resolved it.
	DecidedBy *string `json:"decided_by,omitempty"`
	// MinRole is the least chat level that may decide this approval
	// ("reply", "manage" or "owner"), stamped once at creation by
	// internal/approvalkind. Nil, or any value approvalkind does not know,
	// means owner-only (approvalkind.Stored): a row an older worker wrote
	// fails closed. docs/design/chat-collaboration.md §2.4 (control-plane).
	MinRole *string `json:"min_role,omitempty"`
}
