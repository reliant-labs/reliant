// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

type fakeRunChatService struct {
	reliantv1connect.UnimplementedChatServiceHandler
	got *reliantv1.StartChatRequest
}

func (f *fakeRunChatService) StartChat(_ context.Context, r *connect.Request[reliantv1.StartChatRequest]) (*connect.Response[reliantv1.StartChatResponse], error) {
	f.got = r.Msg
	return connect.NewResponse(&reliantv1.StartChatResponse{
		Chat: &reliantv1.Chat{Id: "chat-1", Title: r.Msg.GetTitle()},
	}), nil
}

// runWorkflowRunCmd runs `reliant workflow run` against a fake ChatService and
// returns the StartChat request it sent and what it printed.
func runWorkflowRunCmd(t *testing.T, args ...string) (*reliantv1.StartChatRequest, string) {
	t.Helper()
	isolateForgeCredHome(t)
	fake := &fakeRunChatService{}
	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewChatServiceHandler(fake))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv(envServerURL, srv.URL)
	t.Setenv(envToken, "rlat_test")

	cmd := newWorkflowRunCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(append([]string{"--project-id", "p1"}, args...))
	require.NoError(t, cmd.Execute())
	require.NotNil(t, fake.got, "StartChat was called")
	return fake.got, out.String()
}

// A command-line run is recorded as cli.run, which keeps it out of the
// sidebar, and the server titles it from the workflow and the message.
func TestWorkflowRun_StartsACLIRun(t *testing.T) {
	req, out := runWorkflowRunCmd(t, "cicd-probe", "-m", "inventory the machine")
	assert.True(t, req.GetCliRun())
	assert.Nil(t, req.Title, "the server titles it from the message")
	assert.Equal(t, "inventory the machine", req.GetMessages()[0].GetContent())
	assert.Contains(t, out, "chat-1")
}

// With no message the CLI sends a kick message, which names no subject, so
// the title is the workflow and the start time rather than the kick message.
func TestWorkflowRun_TitlesAMessagelessRunByWorkflowAndTime(t *testing.T) {
	req, out := runWorkflowRunCmd(t, "cicd-probe")
	require.NotNil(t, req.Title)
	assert.True(t, strings.HasPrefix(req.GetTitle(), "cicd-probe · "), req.GetTitle())
	assert.NotContains(t, req.GetTitle(), "Run the")
	assert.Contains(t, out, "listed under Runs")
}

// Bug 5b73a559: the server never reads text as a boolean or number, so
// `--input asap=true` must reach it as a boolean (it used to be the text
// "true", which a boolean input refuses). Text that a number would lose stays
// text, and a quoted value is always text.
func TestWorkflowRun_InputsKeepTheirType(t *testing.T) {
	req, _ := runWorkflowRunCmd(t, "release-forge",
		"--input", "asap=true", "--input", "every=10", "--input", "version=1.10",
		"--input", `sha="0123"`, "--input", "ref=main")
	params := req.GetWorkflowParams()
	assert.Equal(t, true, params["asap"].AsInterface())
	assert.Equal(t, float64(10), params["every"].AsInterface())
	assert.Equal(t, "1.10", params["version"].AsInterface())
	assert.Equal(t, "0123", params["sha"].AsInterface())
	assert.Equal(t, "main", params["ref"].AsInterface())
}

func TestWorkflowRun_KeepsAnExplicitTitle(t *testing.T) {
	req, _ := runWorkflowRunCmd(t, "cicd-probe", "--title", "probe: kube access")
	assert.Equal(t, "probe: kube access", req.GetTitle())
}
