// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/types/known/structpb"
)

type failingSource struct{ err error }

func (f failingSource) Credential(context.Context, httpaction.CredentialRequest) (httpaction.Credential, error) {
	return nil, f.err
}

func executeDirect(t *testing.T, a *ActionActivity, connection string) (*reliantv1.ActionOutput, error) {
	t.Helper()
	node := &reliantv1.Node{Id: "n", Type: "action", Args: &reliantv1.Node_Action{Action: &reliantv1.ActionArgs{
		Uses:       &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "http/request@1"}},
		With:       map[string]*structpb.Value{"url": structpb.NewStringValue("https://example.com/")},
		Connection: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: connection}},
	}}}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(a.Execute)
	val, err := env.ExecuteActivity(a.Execute, types.ActivityInput{Runtime: types.RuntimeContext{WorkflowID: "run-1", StepID: "n"}, Node: node})
	if err != nil {
		return nil, err
	}
	var out reliantv1.ActionOutput
	require.NoError(t, val.Get(&out))
	return &out, nil
}

// The activity must hand a connection failure to the graph as a typed,
// non-retried error output rather than failing (and retrying) the activity.
func TestActionNodeReportsMissingConnectionAsFailedPrecondition(t *testing.T) {
	a := NewActionActivityWith(catalog.MustBuiltin(), httpaction.NewRunner(netguard.New()),
		failingSource{&httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "no such connection"}})
	out, err := executeDirect(t, a, "conn_missing")
	require.NoError(t, err, "a missing connection must not fail the activity or burn retries")
	assert.True(t, out.GetIsError())
	assert.Equal(t, httpaction.CodeFailedPrecondition, out.GetErrorCode())
	assert.Equal(t, "no such connection", out.GetContent())
	assert.Empty(t, out.GetConnectionId())
}

func TestActionNodeWithoutConnectionSourceRefusesInsteadOfRunningUnauthenticated(t *testing.T) {
	a := NewActionActivityWith(catalog.MustBuiltin(), httpaction.NewRunner(netguard.New()), nil)
	out, err := executeDirect(t, a, "conn_x")
	require.NoError(t, err)
	assert.True(t, out.GetIsError())
	assert.Equal(t, httpaction.CodeFailedPrecondition, out.GetErrorCode())
}

// recordingSource captures the request it was asked to resolve, then refuses
// it so no request leaves the test.
type recordingSource struct {
	got []httpaction.CredentialRequest
}

func (r *recordingSource) Credential(_ context.Context, req httpaction.CredentialRequest) (httpaction.Credential, error) {
	r.got = append(r.got, req)
	return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "recorded"}
}

// The owner a connection resolves for is derived from the run id, so the node
// must pass ITS OWN run, never the chat, thread or any other id on the context.
// Every sibling id is set to something distinct so a swap is caught.
func TestActionNodeResolvesForItsOwnRun(t *testing.T) {
	src := &recordingSource{}
	a := NewActionActivityWith(catalog.MustBuiltin(), httpaction.NewRunner(netguard.New()), src)
	node := &reliantv1.Node{Id: "fetch", Type: "action", Args: &reliantv1.Node_Action{Action: &reliantv1.ActionArgs{
		Uses:       &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "http/request@1"}},
		With:       map[string]*structpb.Value{"url": structpb.NewStringValue("https://example.com/")},
		Connection: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "conn_1"}},
	}}}
	runtime := types.RuntimeContext{
		ChatID: "chat-other", Thread: "thread-other", SessionID: "session-other",
		WorkflowID: "run-own", StepID: "fetch", LoopNodeID: "each", LoopIteration: 2,
	}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(a.Execute)
	_, err := env.ExecuteActivity(a.Execute, types.ActivityInput{Runtime: runtime, Node: node})
	require.NoError(t, err)

	require.Len(t, src.got, 1)
	req := src.got[0]
	assert.Equal(t, "run-own", req.RunID, "the connection must resolve for the node's own run")
	assert.Equal(t, "fetch", req.NodeID)
	assert.Equal(t, "run-own:fetch:each#2", req.ToolCallID, "loop iterations get distinct audit ids")
	assert.Equal(t, "conn_1", req.ConnectionID)
	assert.True(t, req.ServerPlaced)
}

// The node carries only the connection reference.
func TestActionNodeInputCarriesOnlyTheReference(t *testing.T) {
	node := &reliantv1.Node{Args: &reliantv1.Node_Action{Action: &reliantv1.ActionArgs{
		Connection: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "conn_abc"}},
	}}}
	raw, err := json.Marshal(types.ActivityInput{Node: node})
	require.NoError(t, err)
	assert.Contains(t, string(raw), "conn_abc")
	assert.NotContains(t, string(raw), "secret")
}
