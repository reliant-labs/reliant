// Copyright (c) 2025 Reliant Labs
package project

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/rowreads"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/projectaccess/projectaccesstest"
)

// unmovedRouter answers git.unmoved_work as the machine would.
type unmovedRouter struct {
	createProjectRowRouter
	reply   any
	err     error
	asked   int
	payload map[string]any
}

func (r *unmovedRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	if commandType != unmovedWorkCommand {
		return r.createProjectRowRouter.SendDaemonCommandToDaemon(ctx, userID, daemonID, commandType, payload, timeoutMs)
	}
	r.asked++
	_ = json.Unmarshal(payload, &r.payload)
	if r.err != nil {
		return nil, r.err
	}
	return json.Marshal(r.reply)
}

type previewFixture struct {
	svc     *Service
	repo    *db.Repo
	router  *unmovedRouter
	ctx     context.Context
	userID  string
	machine string
	project *db.Project
}

func newPreviewFixture(t *testing.T) *previewFixture {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	router := &unmovedRouter{}
	svc := NewTestProject(t, WithProjectDeps(withStores(Deps{Repo: rowreads.WithRepo(repo), Projects: projectaccesstest.For(repo), Router: router})))
	ctx, userID := fromRepoCtx()
	machine := "laptop-" + uuid.NewString()[:8]
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: machine, UserID: userID, Hostname: ptrStr("laptop")}))
	p := seedMultiRepoProject(t, ctx, repo, userID, "git@github.com:reliant-labs/"+uuid.NewString()+".git")
	require.NoError(t, repo.UpsertProjectDaemon(ctx, p.ID, machine, "/Users/me/src/reliant-labs", nil))
	now := time.Now().UTC()
	require.NoError(t, repo.CreateWorktree(ctx, &db.Worktree{
		ID: uuid.NewString(), Name: "fix-login", Path: "/Users/me/.reliant/worktrees/reliant-labs/fix-login", Branch: "fix-login",
		BaseBranch: "main", ProjectID: p.ID, DaemonID: &machine,
		Status:    int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return &previewFixture{svc: svc, repo: repo, router: router, ctx: ctx, userID: userID, machine: machine, project: p}
}

func (f *previewFixture) connect(t *testing.T) {
	t.Helper()
	require.NoError(t, f.repo.UpsertDaemonAttachment(f.ctx, &db.DaemonAttachment{DaemonID: f.machine, UserID: f.userID, Source: db.DaemonAttachmentSourceInbound}))
}

func (f *previewFixture) preview(t *testing.T) *reliantv1.PreviewProjectMachineMoveResponse {
	t.Helper()
	resp, err := f.svc.PreviewProjectMachineMove(f.ctx, connect.NewRequest(&reliantv1.PreviewProjectMachineMoveRequest{
		ProjectId: f.project.ID, DaemonId: f.machine,
	}))
	require.NoError(t, err)
	return resp.Msg
}

func TestPreviewProjectMachineMove_OfflineSaysItCannotCheckAndNeverAsks(t *testing.T) {
	f := newPreviewFixture(t)
	got := f.preview(t)

	assert.Equal(t, reliantv1.ProjectMachineMoveCheck_PROJECT_MACHINE_MOVE_CHECK_MACHINE_OFFLINE, got.GetCheck())
	assert.Zero(t, f.router.asked, "a machine that is not connected is never asked (nor woken)")
	assert.Equal(t, "laptop", got.GetDaemonName())
	// What Reliant records is still there: the repositories, root first, with
	// the one that has no remote marked, and the branch workspace.
	require.Len(t, got.GetRepos(), 3)
	assert.Equal(t, "", got.GetRepos()[0].GetRelativePath())
	assert.Equal(t, "forge", got.GetRepos()[1].GetRelativePath())
	assert.True(t, got.GetRepos()[1].GetClonable())
	assert.Equal(t, "scratch", got.GetRepos()[2].GetRelativePath())
	assert.False(t, got.GetRepos()[2].GetClonable(), "a repository with no remote cannot be cloned elsewhere")
	require.Len(t, got.GetWorkspaces(), 1)
	assert.Equal(t, "fix-login", got.GetWorkspaces()[0].GetBranch())
}

func TestPreviewProjectMachineMove_ListsWhatStaysBehind(t *testing.T) {
	f := newPreviewFixture(t)
	f.connect(t)
	f.router.reply = map[string]any{"repos": []map[string]any{
		{
			"relative_path": "", "changed_file_count": 3, "changed_files": []string{"a.go", "b.go", "c.go"},
			"unpushed_branches": []map[string]any{{"name": "wip", "commits": 2}}, "stash_count": 1,
			"worktrees": []map[string]any{
				// The branch workspace: listed once, as a workspace.
				{"path": "/Users/me/.reliant/worktrees/reliant-labs/fix-login", "branch": "fix-login"},
				{"path": "/tmp/hand-made", "branch": "spike"},
			},
		},
		{"relative_path": "forge"},
		{"relative_path": "scratch", "error": "no git repository at /Users/me/src/reliant-labs/scratch"},
	}}
	got := f.preview(t)

	assert.Equal(t, reliantv1.ProjectMachineMoveCheck_PROJECT_MACHINE_MOVE_CHECK_CHECKED, got.GetCheck())
	assert.Equal(t, "/Users/me/src/reliant-labs", f.router.payload["path"])
	assert.Equal(t, []any{"", "forge", "scratch"}, f.router.payload["repos"])
	root := got.GetRepos()[0]
	assert.EqualValues(t, 3, root.GetChangedFileCount())
	require.Len(t, root.GetUnpushedBranches(), 1)
	assert.Equal(t, "wip", root.GetUnpushedBranches()[0].GetName())
	assert.EqualValues(t, 2, root.GetUnpushedBranches()[0].GetUnpushedCommits())
	assert.EqualValues(t, 1, root.GetStashCount())
	require.Len(t, root.GetOtherWorktrees(), 1)
	assert.Equal(t, "/tmp/hand-made", root.GetOtherWorktrees()[0].GetPath())
	assert.Zero(t, got.GetRepos()[1].GetChangedFileCount())
	assert.Contains(t, got.GetRepos()[2].GetCheckError(), "no git repository")
}

func TestPreviewProjectMachineMove_AnOlderMachineSaysSo(t *testing.T) {
	f := newPreviewFixture(t)
	f.connect(t)
	f.router.err = errors.New(`daemon command git.unmoved_work: unknown command "git.unmoved_work": this machine runs an older version of Reliant`)
	got := f.preview(t)
	assert.Equal(t, reliantv1.ProjectMachineMoveCheck_PROJECT_MACHINE_MOVE_CHECK_MACHINE_OUTDATED, got.GetCheck())
	assert.Len(t, got.GetRepos(), 3)
}

func TestPreviewProjectMachineMove_NotOnTheMachine(t *testing.T) {
	f := newPreviewFixture(t)
	other := "other-" + uuid.NewString()[:8]
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: other, UserID: f.userID}))
	resp, err := f.svc.PreviewProjectMachineMove(f.ctx, connect.NewRequest(&reliantv1.PreviewProjectMachineMoveRequest{
		ProjectId: f.project.ID, DaemonId: other,
	}))
	require.NoError(t, err)
	assert.Equal(t, reliantv1.ProjectMachineMoveCheck_PROJECT_MACHINE_MOVE_CHECK_NOT_ON_MACHINE, resp.Msg.GetCheck())
}

func TestPreviewProjectMachineMove_RefusesAnotherUsersMachine(t *testing.T) {
	f := newPreviewFixture(t)
	foreign := "foreign-" + uuid.NewString()[:8]
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: foreign, UserID: "someone-else"}))
	_, err := f.svc.PreviewProjectMachineMove(f.ctx, connect.NewRequest(&reliantv1.PreviewProjectMachineMoveRequest{
		ProjectId: f.project.ID, DaemonId: foreign,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, codeOf(err))
}
