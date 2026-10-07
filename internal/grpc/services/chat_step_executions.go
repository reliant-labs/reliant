// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// stepExecutionRecordLimit bounds one ListStepExecutions response. A node's
// own history — an Agent step's turns across a run — fits comfortably; a
// long chat read without a node_path is cut to its newest steps.
const stepExecutionRecordLimit = 500

// ListStepExecutions returns the full record of a chat's steps: per attempt,
// what the step was given (its resolved inputs), what it produced and why it
// failed. GetWorkflowExecutions is the lean tree every surface reads; this is
// the detail one step's inspector asks for, scoped by node_path.
func (s *ChatService) ListStepExecutions(
	ctx context.Context,
	req *connect.Request[reliantv1.ListStepExecutionsRequest],
) (*connect.Response[reliantv1.ListStepExecutionsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}
	nodePath := strings.TrimSpace(req.Msg.NodePath)

	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil || chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// One more than the limit says whether anything was left out.
	rows, err := s.database.ListStepExecutionRecordsForChat(ctx, req.Msg.ChatId, nodePath, stepExecutionRecordLimit+1)
	if err != nil {
		logging.Error("Failed to list step executions", "error", err, "chatID", req.Msg.ChatId, "nodePath", nodePath)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list step executions"))
	}
	truncated := len(rows) > stepExecutionRecordLimit
	if truncated {
		rows = rows[:stepExecutionRecordLimit]
	}

	executions := make([]*reliantv1.StepExecution, len(rows))
	for i, row := range rows {
		executions[i] = stepExecutionRecordToProto(row)
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
		OutputJson:   row.OutputJSON.String,
		CreatedAt:    row.CreatedAt.Format(time.RFC3339Nano),
		InputJson:    row.InputJSON.String,
		ErrorMessage: row.ErrorMessage.String,
		Attempt:      row.Attempt.Int32,
		NodePath:     row.NodePath.String,
	}
	if row.ExitCode.Valid {
		exitCode := int32(row.ExitCode.Int64)
		execution.ExitCode = &exitCode
	}
	if row.Success.Valid {
		success := row.Success.Bool
		execution.Success = &success
	}
	if row.DurationMs.Valid {
		durationMs := row.DurationMs.Int64
		execution.DurationMs = &durationMs
	}
	if row.LoopNodeID.Valid {
		loopNodeID := row.LoopNodeID.String
		execution.LoopNodeId = &loopNodeID
	}
	if row.LoopIteration.Valid {
		loopIteration := int32(row.LoopIteration.Int64)
		execution.LoopIteration = &loopIteration
	}
	return execution
}
