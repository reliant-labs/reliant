package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/stepsecrets"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// secretActionNode is a signer/sign action whose text param carries a marker,
// as the runtime hands it to the activity.
func secretActionNode(text string, refs ...*reliantv1.NodeSecretRef) *reliantv1.Node {
	return &reliantv1.Node{Id: "sign", Type: "action", SecretRefs: refs, Args: &reliantv1.Node_Action{Action: &reliantv1.ActionArgs{
		Uses: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "signer/sign@1"}},
		With: map[string]*structpb.Value{"text": structpb.NewStringValue(text)},
	}}}
}

func traderRef() *reliantv1.NodeSecretRef {
	return &reliantv1.NodeSecretRef{Name: "TRADER_KEY", Environment: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "trader/staging"}}}
}

func secretActionActivity(t *testing.T, resolver SecretResolver, handler func(text string) *httpaction.Result) *ActionActivity {
	t.Helper()
	m, err := manifest.Parse([]byte(signManifest), manifest.TrustCurated)
	require.NoError(t, err)
	reg := httpaction.NewExecutorRegistry()
	require.NoError(t, reg.Register("signer.sign", func(_ context.Context, call httpaction.ExecutorCall) (*httpaction.Result, error) {
		return handler(call.Params["text"].(string)), nil
	}))
	a := NewActionActivityWith(catalog.New([]*reliantv1.IntegrationManifest{m}),
		httpaction.NewRunner(netguard.New()).WithExecutors(reg), staticCred{id: "conn_s", secret: "k-secret-9"})
	return a.WithSecretResolver(resolver)
}

func runSecretAction(t *testing.T, a *ActionActivity, node *reliantv1.Node) (*reliantv1.ActionOutput, types.ActivityInput) {
	t.Helper()
	in := types.ActivityInput{Runtime: types.RuntimeContext{WorkflowID: "run-1", StepID: "sign"}, Node: node}
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(a.Execute)
	val, err := env.ExecuteActivity(a.Execute, in)
	require.NoError(t, err)
	var out reliantv1.ActionOutput
	require.NoError(t, val.Get(&out))
	return &out, in
}

func TestActionSubstitutesSecretsAndMasksEverythingItReturns(t *testing.T) {
	var sent string
	resolver := traderResolver()
	a := secretActionActivity(t, resolver, func(text string) *httpaction.Result {
		sent = text
		return &httpaction.Result{StatusCode: 200, Content: "echo " + text, Data: map[string]any{"signature": "sig:" + text, "n": 1.0}}
	})
	node := secretActionNode("Bearer "+stepsecrets.Marker("TRADER_KEY"), traderRef())

	out, in := runSecretAction(t, a, node)

	assert.Equal(t, "Bearer "+stagingKey, sent, "the executor receives the real value")
	require.Len(t, resolver.calls, 1)
	assert.Equal(t, "run-1", resolver.calls[0].runID)
	assert.Equal(t, "sign", resolver.calls[0].nodeID)
	assert.Equal(t, []stepsecrets.Ref{{Environment: "trader/staging", Name: "TRADER_KEY"}}, resolver.calls[0].refs)

	result, err := json.Marshal(out)
	require.NoError(t, err)
	assert.NotContains(t, string(result), stagingKey)
	assert.Equal(t, "echo Bearer ***", out.GetContent())

	input, err := json.Marshal(in)
	require.NoError(t, err)
	assert.NotContains(t, string(input), stagingKey, "the activity input holds the marker only")
}

func TestActionDeniedSecretShowsTheControlPlaneReason(t *testing.T) {
	reason := "TRADER_KEY in trader/staging is not available to this workflow: add it to the secret's access list"
	a := secretActionActivity(t, &fakeResolver{err: &stepsecrets.DeniedError{Reason: reason}}, func(string) *httpaction.Result {
		t.Fatal("a denied secret must not reach the executor")
		return nil
	})
	out, _ := runSecretAction(t, a, secretActionNode(stepsecrets.Marker("TRADER_KEY"), traderRef()))
	assert.True(t, out.GetIsError())
	assert.Equal(t, "permission_denied", out.GetErrorCode())
	assert.Equal(t, reason, out.GetContent())
}

func TestActionWithSecretsAndNoResolverFailsInsteadOfSendingThePlaceholder(t *testing.T) {
	a := secretActionActivity(t, nil, func(string) *httpaction.Result {
		t.Fatal("must not run without a resolver")
		return nil
	})
	out, _ := runSecretAction(t, a, secretActionNode(stepsecrets.Marker("TRADER_KEY"), traderRef()))
	assert.True(t, out.GetIsError())
	assert.Equal(t, "failed_precondition", out.GetErrorCode())
	assert.True(t, strings.Contains(out.GetContent(), "no secret resolver"), out.GetContent())
}

func TestActionNamingAnUndeclaredSecretFails(t *testing.T) {
	a := secretActionActivity(t, traderResolver(), func(string) *httpaction.Result {
		t.Fatal("must not run")
		return nil
	})
	out, _ := runSecretAction(t, a, secretActionNode(stepsecrets.Marker("OTHER")))
	assert.True(t, out.GetIsError())
	assert.Equal(t, "invalid_argument", out.GetErrorCode())
}

func TestActionWithoutSecretsNeverCallsTheResolver(t *testing.T) {
	resolver := traderResolver()
	a := secretActionActivity(t, resolver, func(text string) *httpaction.Result {
		return &httpaction.Result{StatusCode: 200, Data: map[string]any{"signature": text}}
	})
	out, _ := runSecretAction(t, a, secretActionNode("plain", traderRef()))
	assert.False(t, out.GetIsError())
	assert.Empty(t, resolver.calls)
}
