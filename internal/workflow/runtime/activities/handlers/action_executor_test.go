// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"net/http"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

const signManifest = `
id: signer
version: 1
display_name: Signer
connection:
  base_url: https://api.signer.example.com
  auth:
    - api_key: { in: header, name: X-Key }
actions:
  - id: sign
    placement: server
    executor: go:signer.sign
    params:
      type: object
      required: [text]
      properties: { text: { type: string } }
    output: { schema: { type: object, properties: { signature: { type: string } } } }
`

type staticCred struct{ id, secret string }

func (c staticCred) ConnectionID() string        { return c.id }
func (c staticCred) Apply(r *http.Request) error { r.Header.Set("X-Key", c.secret); return nil }
func (c staticCred) Scrub(s string) string       { return strings.ReplaceAll(s, c.secret, "[redacted]") }
func (c staticCred) Params() map[string]string   { return nil }
func (s staticCred) Credential(context.Context, httpaction.CredentialRequest) (httpaction.Credential, error) {
	return s, nil
}

// An action node whose manifest names a go: executor runs the registered Go
// function, with the node's `with` as validated params and the resolved
// connection, and returns the same ActionOutput a declarative action does.
func TestActionNodeDispatchesGoExecutor(t *testing.T) {
	m, err := manifest.Parse([]byte(signManifest), manifest.TrustCurated)
	require.NoError(t, err)
	reg := httpaction.NewExecutorRegistry()
	var gotText string
	require.NoError(t, reg.Register("signer.sign", func(_ context.Context, call httpaction.ExecutorCall) (*httpaction.Result, error) {
		gotText = call.Params["text"].(string)
		return &httpaction.Result{StatusCode: 200, Data: map[string]any{"signature": "sig(" + gotText + ")k-secret-9"}}, nil
	}))
	a := NewActionActivityWith(catalog.New([]*reliantv1.IntegrationManifest{m}),
		httpaction.NewRunner(netguard.New()).WithExecutors(reg), staticCred{id: "conn_s", secret: "k-secret-9"})

	node := &reliantv1.Node{Id: "sign", Type: "action", Args: &reliantv1.Node_Action{Action: &reliantv1.ActionArgs{
		Uses: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "signer/sign@1"}},
		With: map[string]*structpb.Value{"text": structpb.NewStringValue("hello")},
	}}}
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(a.Execute)
	val, err := env.ExecuteActivity(a.Execute, types.ActivityInput{Runtime: types.RuntimeContext{WorkflowID: "run-1", StepID: "sign"}, Node: node})
	require.NoError(t, err)
	var out reliantv1.ActionOutput
	require.NoError(t, val.Get(&out))

	assert.Equal(t, "hello", gotText)
	assert.False(t, out.GetIsError(), out.GetContent())
	assert.Equal(t, "conn_s", out.GetConnectionId())
	assert.Equal(t, "sig(hello)[redacted]", out.GetData().AsMap()["signature"], "executor output is scrubbed like an HTTP response")
	assert.NotContains(t, out.GetContent(), "k-secret-9")
}
