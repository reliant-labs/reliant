// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/chatproto"
	"github.com/reliant-labs/reliant/internal/ptr"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// stepExecutionRecordLimit bounds one ListStepExecutions response. A node's
// own history — an Agent step's turns across a run — fits comfortably; a
// long chat read without a node_path is cut to its newest steps.
const stepExecutionRecordLimit = 500

// ListStepExecutions returns the full record of a chat's steps: per attempt,
// what the step was given (its resolved inputs), what it produced and why it
// failed. GetWorkflowExecutions is the lean tree every surface reads; this is
// the detail one step's inspector asks for, scoped by node_path.
func (s *Service) ListStepExecutions(
	ctx context.Context,
	req *connect.Request[reliantv1.ListStepExecutionsRequest],
) (*connect.Response[reliantv1.ListStepExecutionsResponse], error) {
	userID := auth.MustGetUserID(ctx)
	// Serves viewers of a shared chat, without what a step produced
	// (chatproto.ElideStepsForViewer).
	ctx = authz.WithViewerReads(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}
	nodePath := strings.TrimSpace(req.Msg.GetNodePath())

	chat, err := s.deps.Repo.GetChatRow(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	access, denied := authz.ReadChatAccess(ctx, userID, chat)
	if denied != nil {
		return nil, svcerr.NotFound("chat")
	}

	// One more than the limit says whether anything was left out.
	rows, err := s.deps.Repo.ListStepExecutionRecordsForChat(ctx, req.Msg.ChatId, nodePath, stepExecutionRecordLimit+1)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list step executions"))
	}
	truncated := len(rows) > stepExecutionRecordLimit
	if truncated {
		rows = rows[:stepExecutionRecordLimit]
	}

	executions := make([]*reliantv1.StepExecution, len(rows))
	for i, row := range rows {
		executions[i] = stepExecutionRecordToProto(row)
	}
	if access.Viewer() {
		chatproto.ElideStepsForViewer(executions)
	}
	return connect.NewResponse(&reliantv1.ListStepExecutionsResponse{
		StepExecutions: executions,
		Truncated:      truncated,
	}), nil
}

func stepExecutionRecordToProto(row *db.StepExecution) *reliantv1.StepExecution {
	execution := &reliantv1.StepExecution{
		Id:           row.ID,
		WorkflowId:   row.WorkflowID,
		StepId:       row.StepID,
		ActivityName: row.ActivityName,
		OutputJson:   ptr.From(row.OutputJSON),
		CreatedAt:    row.CreatedAt.Format(time.RFC3339Nano),
		InputJson:    ptr.From(row.InputJSON),
		ErrorMessage: ptr.From(row.ErrorMessage),
		Attempt:      ptr.From(row.Attempt),
		NodePath:     ptr.From(row.NodePath),
		Success:      row.Success,
		DurationMs:   row.DurationMs,
		LoopNodeId:   row.LoopNodeID,
	}
	if row.ExitCode != nil {
		exitCode := int32(*row.ExitCode)
		execution.ExitCode = &exitCode
	}
	if row.LoopIteration != nil {
		loopIteration := int32(*row.LoopIteration)
		execution.LoopIteration = &loopIteration
	}
	return execution
}
