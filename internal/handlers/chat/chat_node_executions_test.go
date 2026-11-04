// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// A run-status view starts from where its run's nodes stand: the newest event
// of each node of that run — the chat snapshot no longer carries node state.
// Another run's nodes and the run's status row are not part of it, and another
// user's chat is NotFound.
func TestGetWorkflowNodeExecutions_TheRunsNewestEventPerNode(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	const userID = "test-user"
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	chatID := seedChatForThreadTree(t, repo, ctx, userID)

	node := func(workflowID, nodeID, event string) {
		t.Helper()
		require.NoError(t, repo.CreateChatUpdate(ctx, chatID, db.UpdateTypeNodeExecution, workflowID+":"+nodeID,
			`{"workflow_id":"`+workflowID+`","node_id":"`+nodeID+`","event_type":"`+event+`"}`))
	}
	node("run-1", "call_llm", "started")
	node("run-1", "call_llm", "completed")
	node("run-2", "call_llm", "started")
	node("run-1", "tools", "failed")
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, db.UpdateTypeWorkflowStatus, "run-1", `{"status":"completed"}`))

	service := &Service{deps: Deps{Repo: repo}}
	resp, err := service.GetWorkflowNodeExecutions(ctx, connect.NewRequest(&reliantv1.GetWorkflowNodeExecutionsRequest{
		ChatId: chatID, WorkflowId: "run-1",
	}))
	require.NoError(t, err)

	var got []string
	for _, u := range resp.Msg.GetUpdates() {
		require.Equal(t, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_NODE_EXECUTION.String(), u.GetUpdateType())
		got = append(got, u.GetEntityId()+" "+u.GetData())
	}
	require.Equal(t, []string{
		`run-1:call_llm {"workflow_id":"run-1","node_id":"call_llm","event_type":"completed"}`,
		`run-1:tools {"workflow_id":"run-1","node_id":"tools","event_type":"failed"}`,
	}, got)

	otherCtx := context.WithValue(context.Background(), auth.UserIDContextKey, "someone-else")
	_, err = service.GetWorkflowNodeExecutions(otherCtx, connect.NewRequest(&reliantv1.GetWorkflowNodeExecutionsRequest{
		ChatId: chatID, WorkflowId: "run-1",
	}))
	require.Equal(t, connect.CodeNotFound, svcerr.Code(err))
}
