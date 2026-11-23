package run

import (
	"context"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// ResumeRun continues a paused run from its recorded position.
func (s *Service) ResumeRun(
	ctx context.Context,
	req *connect.Request[reliantv1.ResumeRunRequest],
) (*connect.Response[reliantv1.ResumeRunResponse], error) {
	userID := auth.MustGetUserID(ctx)

	_, chat, err := s.runOwnedBy(ctx, userID, req.Msg.RunId)
	if err != nil {
		return nil, err
	}

	resp, err := s.deps.Chats.ResumeChat(ctx, connect.NewRequest(&reliantv1.ResumeChatRequest{
		ChatId: chat.ID,
	}))
	if err != nil {
		return nil, err
	}

	// Re-read rather than echoing the request's run id: resuming an interrupted
	// run can start a NEW execution that continues from the checkpoint, so the
	// live run is whatever the chat now points at.
	out := &reliantv1.ResumeRunResponse{
		Success: resp.Msg.Success,
		Message: resp.Msg.Message,
	}
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		if current, err := s.deps.Workflows.LoadWorkflow(ctx, *chat.WorkflowID); err == nil {
			out.Run = runToProto(current)
		}
	}
	return connect.NewResponse(out), nil
}
