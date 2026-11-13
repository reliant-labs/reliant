// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// fakeMoveClient answers the move's rpcs from fixed values and records the
// move asked for.
type fakeMoveClient struct {
	preview    *reliantv1.PreviewProjectOrgMoveResponse
	previewErr error
	moved      *reliantv1.MoveProjectToOrgRequest
	answer     reliantv1.ProjectOrgMoveState
	reads      []reliantv1.ProjectOrgMoveState
}

func (f *fakeMoveClient) ListProjects(context.Context, *connect.Request[reliantv1.ListProjectsRequest]) (*connect.Response[reliantv1.ListProjectsResponse], error) {
	return connect.NewResponse(&reliantv1.ListProjectsResponse{Projects: []*reliantv1.Project{
		{Id: "p1", Name: "web", Path: "/home/me/web"},
		{Id: "p2", Name: "api", Path: "/home/me/api"},
		{Id: "p3", Name: "api", Path: "/home/me/api-2"},
	}}), nil
}

func (f *fakeMoveClient) PreviewProjectOrgMove(context.Context, *connect.Request[reliantv1.PreviewProjectOrgMoveRequest]) (*connect.Response[reliantv1.PreviewProjectOrgMoveResponse], error) {
	if f.previewErr != nil {
		return nil, f.previewErr
	}
	return connect.NewResponse(f.preview), nil
}

func (f *fakeMoveClient) MoveProjectToOrg(_ context.Context, req *connect.Request[reliantv1.MoveProjectToOrgRequest]) (*connect.Response[reliantv1.MoveProjectToOrgResponse], error) {
	f.moved = req.Msg
	return connect.NewResponse(&reliantv1.MoveProjectToOrgResponse{Move: &reliantv1.ProjectOrgMove{Id: "m1", ToOrgId: req.Msg.GetToOrgId(), State: f.answer}}), nil
}

func (f *fakeMoveClient) GetProjectOrgMove(context.Context, *connect.Request[reliantv1.GetProjectOrgMoveRequest]) (*connect.Response[reliantv1.GetProjectOrgMoveResponse], error) {
	state := f.reads[0]
	if len(f.reads) > 1 {
		f.reads = f.reads[1:]
	}
	return connect.NewResponse(&reliantv1.GetProjectOrgMoveResponse{Move: &reliantv1.ProjectOrgMove{Id: "m1", ToOrgId: "globex", State: state}}), nil
}

func idlePreview() *reliantv1.PreviewProjectOrgMoveResponse {
	return &reliantv1.PreviewProjectOrgMoveResponse{
		FromOrgId: "acme", ToOrgId: "globex",
		Blockers: &reliantv1.ProjectOrgMoveBlockers{},
		Effects:  &reliantv1.ProjectOrgMoveEffects{Chats: 12, Runs: 40},
	}
}

func runMove(f *fakeMoveClient, stdin string, dryRun, yes bool, ref string) (string, error) {
	var out bytes.Buffer
	m := projectMove{client: f, out: &out, in: strings.NewReader(stdin), dryRun: dryRun, yes: yes}
	err := m.run(context.Background(), ref, "globex")
	return out.String(), err
}

func TestProjectMove(t *testing.T) {
	t.Run("a dry run previews and moves nothing", func(t *testing.T) {
		f := &fakeMoveClient{preview: idlePreview()}
		out, err := runMove(f, "", true, false, "web")
		require.NoError(t, err)
		assert.Contains(t, out, "12 chats and 40 runs move with it")
		assert.Contains(t, out, "Dry run: nothing moved.")
		assert.Nil(t, f.moved)
	})

	t.Run("the typed name confirms the move it previewed", func(t *testing.T) {
		f := &fakeMoveClient{preview: idlePreview(), answer: reliantv1.ProjectOrgMoveState_PROJECT_ORG_MOVE_STATE_COMPLETED}
		out, err := runMove(f, "web\n", false, false, "/home/me/web")
		require.NoError(t, err)
		require.NotNil(t, f.moved)
		assert.Equal(t, "p1", f.moved.GetProjectId())
		assert.Equal(t, "acme", f.moved.GetExpectedFromOrgId(), "the org the preview saw it in")
		assert.NotEmpty(t, f.moved.GetRequestKey())
		assert.Contains(t, out, "Moved web to globex")
	})

	t.Run("a wrong name moves nothing", func(t *testing.T) {
		f := &fakeMoveClient{preview: idlePreview()}
		_, err := runMove(f, "wbe\n", false, false, "p1")
		require.ErrorContains(t, err, "not confirmed")
		assert.Nil(t, f.moved)
	})

	t.Run("running work refuses before anything moves", func(t *testing.T) {
		p := idlePreview()
		p.Blockers = &reliantv1.ProjectOrgMoveBlockers{ChatsTotal: 1, Chats: []*reliantv1.ProjectOrgMoveBlockedChat{{ChatId: "c1", Title: "Refactor"}}}
		f := &fakeMoveClient{preview: p}
		out, err := runMove(f, "", false, true, "web")
		require.ErrorContains(t, err, "running work refuses the move")
		assert.Contains(t, out, "c1  Refactor")
		assert.Nil(t, f.moved)
	})

	t.Run("a move under way is followed until it settles", func(t *testing.T) {
		movePollInterval = time.Millisecond
		t.Cleanup(func() { movePollInterval = 1500 * time.Millisecond })
		f := &fakeMoveClient{
			preview: idlePreview(),
			answer:  reliantv1.ProjectOrgMoveState_PROJECT_ORG_MOVE_STATE_PREPARING,
			reads:   []reliantv1.ProjectOrgMoveState{reliantv1.ProjectOrgMoveState_PROJECT_ORG_MOVE_STATE_PREPARED, reliantv1.ProjectOrgMoveState_PROJECT_ORG_MOVE_STATE_MOVED},
		}
		out, err := runMove(f, "", false, true, "web")
		require.NoError(t, err)
		assert.Contains(t, out, "automations are being turned back on")
	})

	t.Run("an ambiguous name asks for the id", func(t *testing.T) {
		_, err := runMove(&fakeMoveClient{preview: idlePreview()}, "", true, false, "api")
		require.ErrorContains(t, err, "2 projects are named")
	})

	t.Run("a person-only refusal says where to move it", func(t *testing.T) {
		refusal := connect.NewError(connect.CodePermissionDenied, errors.New("only a person"))
		refusal.Meta().Set(svcerr.ReasonHeader, "person_required")
		_, err := runMove(&fakeMoveClient{previewErr: refusal}, "", true, false, "web")
		require.ErrorContains(t, err, "Settings → Projects → Danger zone")
	})
}

// An agent's shell, and a machine's credential, are refused before the
// command reaches the server.
func TestProjectMoveRefusesSoftware(t *testing.T) {
	t.Setenv(daemonpolicy.AgentShellEnv, "1")
	cmd := newProjectCmd()
	cmd.SetArgs([]string{"move", "web", "--to-org", "globex"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	require.ErrorIs(t, cmd.Execute(), errAgentCannotMoveProjects)

	t.Setenv(daemonpolicy.AgentShellEnv, "")
	cmd = newProjectCmd()
	cmd.SetArgs([]string{"move", "web", "--to-org", "globex", "--daemon-session"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	require.ErrorContains(t, cmd.Execute(), "machine's credential")
}
