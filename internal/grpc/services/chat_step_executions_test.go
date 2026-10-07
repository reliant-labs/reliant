// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

func recordedStep(stepID, activity, nodePath string, at time.Time) *db.StepExecution {
	step := &db.StepExecution{
		ID:           uuid.NewString(),
		StepID:       stepID,
		ActivityName: activity,
		CreatedAt:    at,
	}
	if nodePath != "" {
		step.NodePath = sql.NullString{String: nodePath, Valid: true}
	}
	return step
}

// ListStepExecutions is what one step's inspector reads: the step's full
// record — inputs, output (even for CallLLM, whose output the lean tree
// withholds), error and attempt — for that node and what ran inside it.
func TestListStepExecutions_ReturnsTheNodesFullRecord(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	const userID = "test-user"
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	chatID := seedChatForThreadTree(t, repo, ctx, userID)
	base := time.Now().UTC()

	failed := recordedStep("summarize", "CallLLM", "summarize", base)
	failed.InputJSON = sql.NullString{String: `{"system_prompt":"Summarize #42"}`, Valid: true}
	failed.ErrorMessage = sql.NullString{String: "provider returned 400", Valid: true}
	failed.Attempt = sql.NullInt32{Int32: 1, Valid: true}
	failed.Success = sql.NullBool{Bool: false, Valid: true}

	retried := recordedStep("summarize", "CallLLM", "summarize", base.Add(time.Millisecond))
	retried.OutputJSON = stepOutput(`{"response_text":"done","tool_calls":[{"name":"view"}]}`)
	retried.Attempt = sql.NullInt32{Int32: 2, Valid: true}

	seedWorkflowWithSteps(t, repo, ctx, chatID, chatID, nil, []*db.StepExecution{
		failed,
		retried,
		// An Agent step's own turn, found by its node path.
		recordedStep("call_llm", "CallLLM", "agent.agent_loop.call_llm", base.Add(2*time.Millisecond)),
		// A different node whose id shares the prefix: not inside "summarize".
		recordedStep("summarize_more", "CallLLM", "summarize_more", base.Add(3*time.Millisecond)),
		// A row from before node_path was recorded, found by its step id.
		recordedStep("agent", "Legacy", "", base.Add(4*time.Millisecond)),
	})

	service := &ChatService{database: repo}
	list := func(nodePath string) *reliantv1.ListStepExecutionsResponse {
		resp, err := service.ListStepExecutions(ctx, connect.NewRequest(&reliantv1.ListStepExecutionsRequest{
			ChatId:   chatID,
			NodePath: nodePath,
		}))
		require.NoError(t, err)
		return resp.Msg
	}

	summarize := list("summarize").GetStepExecutions()
	require.Len(t, summarize, 2)
	require.Equal(t, int32(2), summarize[0].GetAttempt(), "newest first")
	require.Equal(t, `{"response_text":"done","tool_calls":[{"name":"view"}]}`, summarize[0].GetOutputJson(),
		"CallLLM's output is the step's answer; the inspector gets it even though the tree withholds it")
	require.Equal(t, int32(1), summarize[1].GetAttempt())
	require.Equal(t, `{"system_prompt":"Summarize #42"}`, summarize[1].GetInputJson())
	require.Equal(t, "provider returned 400", summarize[1].GetErrorMessage())
	require.False(t, summarize[1].GetSuccess())

	agent := list("agent").GetStepExecutions()
	require.Len(t, agent, 2, "the agent's own turns, by path, and its pre-path row, by step id")
	require.Equal(t, "Legacy", agent[0].GetActivityName())
	require.Equal(t, "agent.agent_loop.call_llm", agent[1].GetNodePath())

	require.Len(t, list("").GetStepExecutions(), 5, "no node path: the whole chat")
}

func TestListStepExecutions_OtherUsersChatIsNotFound(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ownerCtx := context.WithValue(context.Background(), auth.UserIDContextKey, "owner")
	chatID := seedChatForThreadTree(t, repo, ownerCtx, "owner")

	otherCtx := context.WithValue(context.Background(), auth.UserIDContextKey, "someone-else")
	_, err := (&ChatService{database: repo}).ListStepExecutions(otherCtx, connect.NewRequest(&reliantv1.ListStepExecutionsRequest{ChatId: chatID}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}
