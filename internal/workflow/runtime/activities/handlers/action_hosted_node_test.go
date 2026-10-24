package handlers

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestHostedUseActionNodeUsesOnlyFixedExecutor(t *testing.T) {
	f := &hostedFake{}
	// No credential source: a hosted node must not try to resolve a saved key.
	a := NewActionActivity(nil).WithHostedExecutor(f)
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(a.Execute)
	literal := func(s string) *reliantv1.CelString {
		return &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: s}}
	}
	node := &reliantv1.Node{Id: "fixed", Type: "action", Args: &reliantv1.Node_Action{Action: &reliantv1.ActionArgs{Uses: literal("http/request@1"), HostedUse: literal("grant"), With: map[string]*structpb.Value{"url": structpb.NewStringValue("https://example.com/rpc"), "method": structpb.NewStringValue("POST")}}}}
	val, err := env.ExecuteActivity(a.Execute, types.ActivityInput{Runtime: types.RuntimeContext{WorkflowID: "run", StepID: "fixed"}, Node: node})
	require.NoError(t, err)
	var out reliantv1.ActionOutput
	require.NoError(t, val.Get(&out))
	require.False(t, out.IsError)
	require.Equal(t, 1, f.calls)
}
