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
	got, out, err := execWorkflowRunCmd(t, nil, append([]string{"--project-id", "p1"}, args...)...)
	require.NoError(t, err)
	require.NotNil(t, got, "StartChat was called")
	return got, out
}

// execWorkflowRunCmd runs `reliant workflow run` with args against a fake
// ChatService plus whatever other fakes register mounts. It returns the
// StartChat request (nil when none was sent), the output and the error.
func execWorkflowRunCmd(t *testing.T, register func(*http.ServeMux), args ...string) (*reliantv1.StartChatRequest, string, error) {
	t.Helper()
	isolateForgeCredHome(t)
	fake := &fakeRunChatService{}
	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewChatServiceHandler(fake))
	if register != nil {
		register(mux)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv(envServerURL, srv.URL)
	t.Setenv(envToken, "rlat_test")

	cmd := newWorkflowRunCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return fake.got, out.String(), err
}

type fakeRunDaemons struct {
	reliantv1connect.UnimplementedDaemonRegistryServiceHandler
}

func (fakeRunDaemons) ListDaemons(context.Context, *connect.Request[reliantv1.ListDaemonsRequest]) (*connect.Response[reliantv1.ListDaemonsResponse], error) {
	return connect.NewResponse(&reliantv1.ListDaemonsResponse{Daemons: []*reliantv1.DaemonInfo{
		{DaemonId: "d-laptop", Hostname: "laptop"},
		{DaemonId: "d-cloud", Hostname: "cloud"},
	}}), nil
}

type fakeRunProjects struct {
	reliantv1connect.UnimplementedProjectServiceHandler
	rows []*reliantv1.ProjectDaemon
}

func (f fakeRunProjects) ListProjectDaemons(context.Context, *connect.Request[reliantv1.ListProjectDaemonsRequest]) (*connect.Response[reliantv1.ListProjectDaemonsResponse], error) {
	return connect.NewResponse(&reliantv1.ListProjectDaemonsResponse{ProjectDaemons: f.rows}), nil
}

// --project-id wins over --project-path, so nothing looks the project up on
// the --daemon machine: the run is refused before StartChat unless the
// project is checked out there, and sent to that machine when it is.
func TestWorkflowRun_ProjectIDWithDaemonNeedsTheProjectOnThatMachine(t *testing.T) {
	installed := reliantv1.ProjectInstallState_PROJECT_INSTALL_STATE_INSTALLED
	installing := reliantv1.ProjectInstallState_PROJECT_INSTALL_STATE_INSTALLING
	mounts := func(rows ...*reliantv1.ProjectDaemon) func(*http.ServeMux) {
		return func(mux *http.ServeMux) {
			mux.Handle(reliantv1connect.NewDaemonRegistryServiceHandler(fakeRunDaemons{}))
			mux.Handle(reliantv1connect.NewProjectServiceHandler(fakeRunProjects{rows: rows}))
		}
	}

	t.Run("on that machine", func(t *testing.T) {
		got, _, err := execWorkflowRunCmd(t, mounts(
			&reliantv1.ProjectDaemon{ProjectId: "p1", DaemonId: "d-cloud", InstallState: installed},
		), "--project-id", "p1", "--daemon", "cloud", "cicd-probe")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "d-cloud", got.GetDaemonId())
	})

	t.Run("only on another machine", func(t *testing.T) {
		got, _, err := execWorkflowRunCmd(t, mounts(
			&reliantv1.ProjectDaemon{ProjectId: "p1", DaemonId: "d-laptop", InstallState: installed},
			&reliantv1.ProjectDaemon{ProjectId: "p2", DaemonId: "d-cloud", InstallState: installed},
		), "--project-id", "p1", "--daemon", "cloud", "cicd-probe")
		require.ErrorContains(t, err, "project p1 is not on machine d-cloud")
		assert.ErrorContains(t, err, "--project-path")
		assert.Nil(t, got, "no run is started")
	})

	t.Run("still installing there", func(t *testing.T) {
		got, _, err := execWorkflowRunCmd(t, mounts(
			&reliantv1.ProjectDaemon{ProjectId: "p1", DaemonId: "d-cloud", InstallState: installing},
		), "--project-id", "p1", "--daemon", "cloud", "cicd-probe")
		require.ErrorContains(t, err, "not ready on machine d-cloud")
		assert.Nil(t, got)
	})

	t.Run("--project-path is ignored when --project-id is given", func(t *testing.T) {
		got, _, err := execWorkflowRunCmd(t, mounts(),
			"--project-id", "p1", "--project-path", t.TempDir(), "--daemon", "cloud", "cicd-probe")
		require.ErrorContains(t, err, "project p1 is not on machine d-cloud")
		assert.Nil(t, got)
	})
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
