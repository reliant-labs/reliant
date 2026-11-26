package approval

import (
	"context"
	"encoding/json"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// approvalToProto converts a db.Approval to proto Approval
func approvalToProto(a *db.Approval) *reliantv1.Approval {
	proto := &reliantv1.Approval{
		Id:           a.ID,
		ChatId:       a.ChatID,
		ApprovalType: reliantv1.ApprovalType(a.ApprovalType),
		EntityId:     a.EntityID,
		Status:       reliantv1.ApprovalStatus(a.Status),
		Title:        a.Title,
		CreatedAt:    a.CreatedAt.Format(time.RFC3339),
	}
	if a.DenialReason != nil {
		proto.DenialReason = a.DenialReason
	}
	if a.BatchID != nil && *a.BatchID != "" {
		proto.BatchId = a.BatchID
	}

	if a.ResolvedAt != nil {
		resolvedAt := a.ResolvedAt.Format(time.RFC3339)
		proto.ResolvedAt = &resolvedAt
	}

	// Parse and populate structured metadata fields
	if a.Metadata != nil {
		var metadata map[string]interface{}
		if err := json.Unmarshal([]byte(*a.Metadata), &metadata); err == nil {
			if messageID, ok := metadata["message_id"].(string); ok {
				proto.MessageId = &messageID
			}
			if workflowID, ok := metadata["workflow_id"].(string); ok {
				proto.WorkflowId = &workflowID
			}
			if runID, ok := metadata["run_id"].(string); ok {
				proto.RunId = &runID
			}
		}
	}
	if p := a.ConnectionProposal(); p != nil {
		proto.ConnectionProposal = proposalToProto(p)
	}

	return proto
}

// ListApprovalsByChat lists all pending approvals for a chat
func (s *Service) ListApprovalsByChat(
	ctx context.Context,
	req *connect.Request[reliantv1.ListApprovalsByChatRequest],
) (*connect.Response[reliantv1.ListApprovalsByChatResponse], error) {
	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	if err := s.chatBelongsToUser(ctx, req.Msg.ChatId); err != nil {
		return nil, err
	}

	approvals, err := s.deps.Approvals.ListPendingApprovalsByChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list approvals"))
	}

	protoApprovals := make([]*reliantv1.Approval, len(approvals))
	for i, a := range approvals {
		protoApprovals[i] = approvalToProto(a)
	}

	return connect.NewResponse(&reliantv1.ListApprovalsByChatResponse{
		Approvals: protoApprovals,
		Total:     int32(len(protoApprovals)),
	}), nil
}
