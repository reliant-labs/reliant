// Copyright (c) 2025 Reliant Labs

package ghdelegated

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// TestHostedGitHubActionNodeEndToEnd proves the hosted path for a GitHub
// action: a workflow `action` node using github/issue.comment@1 runs through
// the real DynamicWorkflow and ActionActivity, the credential source finds no
// saved connection and falls back to the delegated control-plane broker, the
// broker fetches the RUN OWNER's token (owner read from the run record), and
// the request reaches the GitHub fake as that owner's bearer token. The
// result, with the token scrubbed, reaches the workflow outputs.
func TestHostedGitHubActionNodeEndToEnd(t *testing.T) {
	e := newEnv(t)
	runID := e.newRun("idp|alice")

	var gotAuth, gotPath, gotAccept, gotVersion string
	var gotBody map[string]any
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.EscapedPath()
		gotAccept, gotVersion = r.Header.Get("Accept"), r.Header.Get("X-GitHub-Api-Version")
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &gotBody))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		// A hostile upstream echoes the token back; it must not reach the run.
		_, _ = w.Write([]byte(`{"id": 555, "html_url": "https://github.com/acme/app/issues/42#issuecomment-555",
		  "created_at": "2026-01-04T00:00:00Z", "body": "` + r.Header.Get("Authorization") + `"}`))
	}))
	defer srv.Close()
	host, err := url.Parse(srv.URL)
	require.NoError(t, err)

	// The real catalog, with github's base_url pointed at the fake and the
	// broker pinned to the fake's host instead of api.github.com.
	cat := githubCatalogAt(t, srv.URL)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	guard := netguard.New()
	guard.AllowLoopback = true
	action := handlers.NewActionActivityWith(cat, httpaction.NewRunner(guard).WithRootCAs(pool), e.source(host.Host))

	wf := &reliantv1.Workflow{
		Name:  "gh-comment",
		Entry: []string{"comment"},
		Nodes: []*reliantv1.Node{{
			Id: "comment", Type: model.NodeTypeAction,
			Args: &reliantv1.Node_Action{Action: &reliantv1.ActionArgs{
				Uses: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "github/issue.comment@1"}},
				With: map[string]*structpb.Value{
					"owner":        structpb.NewStringValue("acme"),
					"repo":         structpb.NewStringValue("app"),
					"issue_number": structpb.NewStringValue("{{ inputs.issue }}"),
					"body":         structpb.NewStringValue("Triaged by Reliant"),
				},
			}},
		}},
		Outputs: map[string]string{
			"comment_url": "{{nodes.comment.data.html_url}}",
			"comment_id":  "{{nodes.comment.data.id}}",
			"connection":  "{{nodes.comment.connection_id}}",
			"is_error":    "{{nodes.comment.is_error}}",
		},
	}
	workflowBytes, err := protojson.Marshal(wf)
	require.NoError(t, err)

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	// The workflow id IS the run id the owner is resolved from.
	env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: runID})
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
		ChatID: runID, WorkflowName: "gh-comment", Inputs: map[string]interface{}{"issue": 42},
		ExecContext: &v2.ExecutionContext{
			ChatID: runID, Thread: runID, ThreadMode: model.ThreadModeNew, WorkflowName: "gh-comment",
		},
	})
	require.NoError(t, env.GetWorkflowError())
	var result v2.WorkflowResult
	require.NoError(t, env.GetWorkflowResult(&result))

	// The request: the run owner's delegated token, GitHub's headers, the
	// manifest's path and body, with the templated issue number kept numeric.
	assert.Equal(t, "Bearer ghu_alice_token_9f3c1e", gotAuth, "the run owner's control-plane token")
	assert.Equal(t, "/repos/acme/app/issues/42/comments", gotPath)
	assert.Equal(t, "application/vnd.github+json", gotAccept)
	assert.Equal(t, "2022-11-28", gotVersion)
	assert.Equal(t, map[string]any{"body": "Triaged by Reliant"}, gotBody)
	assert.Equal(t, []string{"idp|alice|github"}, e.cp.asked, "control-plane was asked for exactly the run owner")

	// The outputs: mapped fields, the delegated connection id for audit.
	assert.Equal(t, false, result.Outputs["is_error"])
	assert.Equal(t, "https://github.com/acme/app/issues/42#issuecomment-555", result.Outputs["comment_url"])
	assert.EqualValues(t, 555, result.Outputs["comment_id"])
	assert.Equal(t, ConnectionID, result.Outputs["connection"])
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "ghu_alice_token_9f3c1e", "the token never enters workflow state")
}

// githubCatalogAt is the embedded catalog with the github manifest's base_url
// pointed at baseURL: the shipped action declarations, a different host.
func githubCatalogAt(t *testing.T, baseURL string) *catalog.Catalog {
	t.Helper()
	var ms []*reliantv1.IntegrationManifest
	for _, m := range catalog.MustBuiltin().Manifests() {
		if m.GetId() == IntegrationID {
			m = proto.Clone(m).(*reliantv1.IntegrationManifest)
			m.Connection.BaseUrl = baseURL
		}
		ms = append(ms, m)
	}
	return catalog.New(ms)
}
