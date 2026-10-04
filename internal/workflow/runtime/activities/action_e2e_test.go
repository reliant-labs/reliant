// Copyright (c) 2025 Reliant Labs
package activities

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestActionNodeRunsEndToEnd drives a workflow containing an http action node
// through the real DynamicWorkflow, with the real ActionActivity handler
// calling an httptest server, and asserts the selected output reaches the
// workflow outputs.
func TestActionNodeRunsEndToEnd(t *testing.T) {
	var gotMethod, gotAuthHeader, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotAuthHeader, gotQuery = r.Method, r.Header.Get("X-Demo"), r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"greeting":"hello","n":2}`))
	}))
	defer srv.Close()

	guard := netguard.New()
	guard.AllowLoopback = true
	action := handlers.NewActionActivityWith(catalog.MustBuiltin(), httpaction.NewRunner(guard))

	wf := &reliantv1.Workflow{
		Name:  "action-e2e",
		Entry: []string{"call"},
		Nodes: []*reliantv1.Node{{
			Id: "call", Type: model.NodeTypeAction,
			Args: &reliantv1.Node_Action{Action: &reliantv1.ActionArgs{
				Uses: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "http/request@1"}},
				With: map[string]*structpb.Value{
					"url":     structpb.NewStringValue(srv.URL + "/hi"),
					"headers": structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{"X-Demo": structpb.NewStringValue("yes")}}),
					"query":   structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{"a": structpb.NewStringValue("b")}}),
				},
			}},
		}},
		Outputs: map[string]string{
			"greeting": "{{nodes.call.data.json.greeting}}",
			"status":   "{{nodes.call.status_code}}",
		},
	}
	workflowBytes, err := protojson.Marshal(wf)
	require.NoError(t, err)

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(func(ctx context.Context, in types.ActivityInput) (map[string]interface{}, error) {
		out, err := action.Execute(ctx, in)
		if err != nil {
			return nil, err
		}
		encoded, err := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}.Marshal(out)
		if err != nil {
			return nil, err
		}
		var asMap map[string]interface{}
		return asMap, json.Unmarshal(encoded, &asMap)
	}, activity.RegisterOptions{Name: "Action"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]string) (v2.LoadedWorkflow, error) {
		return v2.LoadedWorkflow{WorkflowJSON: workflowBytes}, nil
	}, activity.RegisterOptions{Name: "ActivityLoadWorkflow"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (interface{}, error) { return nil, nil },
		activity.RegisterOptions{Name: "WorkflowStatus"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"success": true}, nil
	}, activity.RegisterOptions{Name: "WorkflowCheckpoint"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{}, nil
	}, activity.RegisterOptions{Name: "Cleanup"})

	env.ExecuteWorkflow(v2.DynamicWorkflow, v2.WorkflowInput{
		ChatID: "chat-action", WorkflowName: "action-e2e", Inputs: map[string]interface{}{},
		ExecContext: &v2.ExecutionContext{
			WorkflowID: "wf-ignored", ChatID: "chat-action", Thread: "thread-action",
			ThreadMode: model.ThreadModeNew, WorkflowName: "action-e2e",
		},
	})
	require.NoError(t, env.GetWorkflowError())

	var result v2.WorkflowResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "hello", result.Outputs["greeting"])
	require.EqualValues(t, 200, result.Outputs["status"])
	require.Equal(t, "GET", gotMethod)
	require.Equal(t, "yes", gotAuthHeader)
	require.Equal(t, "a=b", gotQuery)
}
