package core

import (
	"context"
	"time"
)

// Approval represents a unified approval record for both tool calls and workflow steps.
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
}

// ApprovalStore is the shared contract for approval persistence across drivers.
type ApprovalStore interface {
	CreateApproval(ctx context.Context, approval *Approval) error
	GetApproval(ctx context.Context, id string) (*Approval, error)
	GetApprovalByEntityID(ctx context.Context, entityID string) (*Approval, error)
	ListPendingApprovalsByChat(ctx context.Context, chatID string) ([]*Approval, error)
	ListApprovalsByChat(ctx context.Context, chatID string) ([]*Approval, error)
	UpdateApprovalStatus(ctx context.Context, id string, status int32, denialReason *string, actionTaken *string, metadata *string, resolvedAt *time.Time) error
}
