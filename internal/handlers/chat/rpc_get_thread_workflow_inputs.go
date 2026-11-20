package chat

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/workflow"
)

// GetThreadWorkflowInputs returns the workflow inputs for a specific thread.
// It looks up the workflow record for the thread, queries Temporal for current inputs,
// and falls back to empty inputs for completed workflows.
func (s *Service) GetThreadWorkflowInputs(
	ctx context.Context,
	req *connect.Request[reliantv1.GetThreadWorkflowInputsRequest],
) (*connect.Response[reliantv1.GetThreadWorkflowInputsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" || req.Msg.ThreadId == "" {
		return nil, svcerr.InvalidArgument("chat_id and thread_id are required")
	}

	// Verify chat ownership
	chat, err := s.deps.Repo.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if !authz.CanReadChat(ctx, userID, chat) {
		return nil, svcerr.NotFound("chat")
	}

	// Look up the workflow record for this thread
	wf, err := s.deps.Repo.GetWorkflowByThread(ctx, req.Msg.ChatId, req.Msg.ThreadId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get the thread's workflow"))
	}
	if wf == nil {
		return nil, svcerr.WithMessage(svcerr.ErrNotFound, "no workflow found for thread")
	}

	isRunning := wf.Status == db.Active()
	// A sub-agent that stopped on an error is not running, but its params
	// still matter: they are what it resumes with, and the run that will
	// resume it holds them.
	resumable := !isRunning && s.canResumeStoppedAgent(ctx, chat, req.Msg.ThreadId)

	// Resolve to root workflow ID for Temporal queries.
	// Child workflows are inline goroutines with synthetic DB IDs that Temporal doesn't know.
	// All queries must target the root workflow (the only real Temporal workflow).
	rootWorkflowID := chat.MainThreadID()

	// Query Temporal for thread-specific inputs. Only the root's current run
	// can answer, and only while it is open (rtemporal.QueryOpenRun).
	inputsMap := make(map[string]*structpb.Value)
	if (isRunning || resumable) && rootWorkflowID != "" {
		// Use get_thread_inputs query which returns the thread's subInputs map
		queryResp, err := rtemporal.QueryOpenRun(ctx, s.deps.Temporal, rootWorkflowID, "get_thread_inputs", req.Msg.ThreadId)
		if err == nil {
			var currentInputs map[string]interface{}
			if err := queryResp.Get(&currentInputs); err == nil {
				// Convert to protobuf Values, filtering out runtime-injected inputs
				for key, value := range currentInputs {
					if workflow.RuntimeInjectedInputs[key] {
						continue
					}
					pbVal, err := structpb.NewValue(value)
					if err == nil {
						inputsMap[key] = pbVal
					}
				}
			}
		} else {
			logging.DebugContext(ctx, "Failed to query thread inputs", "error", err, "rootWorkflowID", rootWorkflowID, "thread", req.Msg.ThreadId)
		}
	}

	return connect.NewResponse(&reliantv1.GetThreadWorkflowInputsResponse{
		WorkflowName: wf.WorkflowName,
		Inputs:       inputsMap,
		IsRunning:    isRunning,
		Resumable:    resumable,
	}), nil
}
