// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

// fakePipeline is a PipelineService holding one enrolled repository.
type fakePipeline struct {
	reliantv1connect.UnimplementedPipelineServiceHandler
	repo    *reliantv1.PipelineRepository
	setMode *reliantv1.SetPipelineModeRequest
}

func (f *fakePipeline) GetPipelineState(_ context.Context, req *connect.Request[reliantv1.GetPipelineStateRequest]) (*connect.Response[reliantv1.GetPipelineStateResponse], error) {
	if req.Msg.GetRepository() != f.repo.GetRepository() && req.Msg.GetRepoId() != f.repo.GetId() {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}
	return connect.NewResponse(&reliantv1.GetPipelineStateResponse{
		Repository: f.repo,
		PrStates:   []*reliantv1.PipelineStateCount{{State: "awaiting_review", Count: 2}},
	}), nil
}

func (f *fakePipeline) SetPipelineMode(_ context.Context, req *connect.Request[reliantv1.SetPipelineModeRequest]) (*connect.Response[reliantv1.SetPipelineModeResponse], error) {
	f.setMode = req.Msg
	updated := proto.CloneOf(f.repo)
	updated.Mode, updated.Version = req.Msg.GetMode(), f.repo.GetVersion()+1
	return connect.NewResponse(&reliantv1.SetPipelineModeResponse{Repository: updated}), nil
}

func (f *fakePipeline) ListQueue(_ context.Context, req *connect.Request[reliantv1.ListQueueRequest]) (*connect.Response[reliantv1.ListQueueResponse], error) {
	if req.Msg.GetRepoId() != f.repo.GetId() {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}
	return connect.NewResponse(&reliantv1.ListQueueResponse{
		Entries: []*reliantv1.PipelineQueueEntry{{Id: "q-1", Number: 17, HeadSha: "aaaaaaaaaaaaaaaa", State: "waiting", Priority: 50}},
	}), nil
}

// `reliant pipeline` dials the PipelineService through the CLI's own
// connection: status, mode (resolving owner/name to the repository and its
// version first) and queue, as text and --json.
func TestPipelineCommandsDialTheServer(t *testing.T) {
	isolateControlPlaneEnv(t)
	fake := &fakePipeline{repo: &reliantv1.PipelineRepository{
		Id: "r-1", Repository: "acme/widgets", Mode: "off", QueueState: "running", BaseBranch: "main", Version: 3,
	}}
	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewPipelineServiceHandler(fake))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv(envServerURL, srv.URL)
	t.Setenv(envToken, "rlat_login")

	out, err := executeRoot(t, "pipeline", "status", "acme/widgets")
	require.NoError(t, err)
	assert.Contains(t, out, "acme/widgets  repo r-1  mode off")
	assert.Contains(t, out, "PRs: awaiting_review=2")

	out, err = executeRoot(t, "pipeline", "status", "acme/widgets", "--json")
	require.NoError(t, err)
	var state map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &state), out)
	assert.Equal(t, "r-1", state["repository"].(map[string]any)["id"])

	out, err = executeRoot(t, "pipeline", "mode", "acme/widgets", "shadow")
	require.NoError(t, err)
	assert.Contains(t, out, "acme/widgets: mode shadow (was off).")
	require.NotNil(t, fake.setMode)
	assert.Equal(t, "r-1", fake.setMode.GetRepoId())
	assert.Equal(t, int64(3), fake.setMode.GetExpectedVersion(), "the version it read")

	_, err = executeRoot(t, "pipeline", "mode", "acme/widgets", "loud")
	require.Error(t, err)

	out, err = executeRoot(t, "pipeline", "queue", "acme/widgets", "--json")
	require.NoError(t, err)
	assert.Contains(t, out, `"number": 17`)
	out, err = executeRoot(t, "pipeline", "queue", "acme/widgets")
	require.NoError(t, err)
	assert.Contains(t, out, "#17")
	assert.Contains(t, out, "Current batch: none")
}
