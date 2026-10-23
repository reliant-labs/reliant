// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"
	querypb "go.temporal.io/api/query/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"

	"github.com/reliant-labs/reliant/internal/chatselection"
	"github.com/reliant-labs/reliant/internal/db"
	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
)

// fakeQueryResponse answers QueryWorkflowWithOptions the way Temporal does:
// a query that asks to be refused on a closed run (NOT_OPEN) is refused when
// closed is true, and otherwise gets answer/err. A fake that answered a closed
// run anyway would hide exactly the replay storm QueryOpenRun exists to stop.
func fakeQueryResponse(req *client.QueryWorkflowWithOptionsRequest, closed bool, answer converter.EncodedValue, err error) (*client.QueryWorkflowWithOptionsResponse, error) {
	if closed && req.QueryRejectCondition == enums.QUERY_REJECT_CONDITION_NOT_OPEN {
		return &client.QueryWorkflowWithOptionsResponse{
			QueryRejected: &querypb.QueryRejected{Status: enums.WORKFLOW_EXECUTION_STATUS_COMPLETED},
		}, nil
	}
	if err != nil {
		return nil, err
	}
	return &client.QueryWorkflowWithOptionsResponse{QueryResult: answer}, nil
}

// seedSelection stores params as the chat's selection, the record every send
// and param edit leaves of what the chat's run was given.
func seedSelection(t *testing.T, ctx context.Context, repo *db.Repo, chatID, userID, workflowName string, params map[string]any) {
	t.Helper()
	_, err := chatselection.NewInstrumented(repo).Merge(ctx, chatID, userID, workflowName, params, 0)
	require.NoError(t, err)
}

// queryRecorder records every query a handler makes, answering from inputs.
type queryRecorder struct {
	wakeTestTemporalClient
	closed  bool
	inputs  map[string]interface{}
	queries []*client.QueryWorkflowWithOptionsRequest
}

func (c *queryRecorder) QueryWorkflowWithOptions(_ context.Context, req *client.QueryWorkflowWithOptionsRequest) (*client.QueryWorkflowWithOptionsResponse, error) {
	c.queries = append(c.queries, req)
	return fakeQueryResponse(req, c.closed, jsonEncodedValue{v: c.inputs}, nil)
}

// Every query a chat handler makes names no run id — the workflow's CURRENT
// run, never the one the chat row recorded, which a continue-as-new leaves
// closed — and asks Temporal to refuse it if that run is not open, rather than
// replay the run's whole history on a worker to answer it. On 2026-10-23 a
// stale chats.run_id aimed these queries at continued-as-new runs of ~37k
// events, and each answer cost a worker 13-17 minutes of two cores.
func TestRunQueriesTargetTheCurrentOpenRunOnly(t *testing.T) {
	t.Parallel()
	stale := "run-closed-by-continue-as-new"
	chat := &db.Chat{ID: "chat-1", WorkflowID: new(string), RunID: &stale}
	*chat.WorkflowID = "chat-1"

	open := &queryRecorder{inputs: map[string]interface{}{"mode": "auto"}}
	svc := &Service{deps: Deps{Temporal: open}}

	assert.False(t, svc.checkParamsActuallyChanged(context.Background(), chat.MainThreadID(), map[string]interface{}{"mode": "auto"}),
		"an open run answers, and an unchanged param is reported unchanged")
	assert.True(t, svc.checkParamsActuallyChanged(context.Background(), chat.MainThreadID(), map[string]interface{}{"mode": "plan"}))
	require.NotNil(t, svc.runInputsForSend(context.Background(), chat.MainThreadID()))

	require.Len(t, open.queries, 3)
	for _, q := range open.queries {
		assert.Equal(t, "chat-1", q.WorkflowID)
		assert.Empty(t, q.RunID, "a query must target the current run, never the chat row's (stale) run id")
		assert.Equal(t, enums.QUERY_REJECT_CONDITION_NOT_OPEN, q.QueryRejectCondition,
			"a closed run must be refused, not replayed")
	}

	closed := &queryRecorder{closed: true, inputs: map[string]interface{}{"mode": "auto"}}
	svc = &Service{deps: Deps{Temporal: closed}}
	assert.True(t, svc.checkParamsActuallyChanged(context.Background(), chat.MainThreadID(), map[string]interface{}{"mode": "auto"}),
		"a refused query reads as cannot-tell, which assumes changed")
	assert.Nil(t, svc.runInputsForSend(context.Background(), chat.MainThreadID()),
		"a closed run has no inputs to give: the caller rebuilds them from the chat")
	_, err := svc.queryRunInputs(context.Background(), chat.MainThreadID())
	require.ErrorIs(t, err, rtemporal.ErrRunNotOpen)
}
