// Copyright (c) 2025 Reliant Labs
package db

import (
	"encoding/json"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// ApprovalFeedStatus is the status string a chat feed approval update carries
// for status. Clients map it back; an unknown one must never read as pending.
func ApprovalFeedStatus(status int32) string {
	switch reliantv1.ApprovalStatus(status) {
	case reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING:
		return "pending"
	case reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED:
		return "approved"
	case reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED:
		return "denied"
	case reliantv1.ApprovalStatus_APPROVAL_STATUS_EXPIRED:
		return "expired"
	default:
		return "cancelled"
	}
}

// AttributeApprovalUpdate adds to an approval's chat feed update the workflow
// that raised it (the workflow_id Approval.workflow_id reports) and its thread,
// when known. Every approval update carries them, its resolution included, so
// a client replaying the feed attributes an approval to its run on its own:
// once resolved it is gone from the pending-only ListApprovalsByChat.
func AttributeApprovalUpdate(update map[string]any, a *core.Approval) {
	if a.Metadata != nil {
		var metadata struct {
			WorkflowID string `json:"workflow_id"`
		}
		if err := json.Unmarshal([]byte(*a.Metadata), &metadata); err == nil && metadata.WorkflowID != "" {
			update["workflow_id"] = metadata.WorkflowID
		}
	}
	if a.ThreadID != nil && *a.ThreadID != "" {
		update["thread_id"] = *a.ThreadID
	}
}
