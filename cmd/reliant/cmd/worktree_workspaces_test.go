// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

type workspaceProjects struct {
	reliantv1connect.UnimplementedProjectServiceHandler
	projects []*reliantv1.Project
}

func (h workspaceProjects) ListProjects(context.Context, *connect.Request[reliantv1.ListProjectsRequest]) (*connect.Response[reliantv1.ListProjectsResponse], error) {
	return connect.NewResponse(&reliantv1.ListProjectsResponse{Projects: h.projects}), nil
}

// fakeWorkspaces serves one project's workspaces, a page at a time.
type fakeWorkspaces struct {
	reliantv1connect.UnimplementedWorktreeServiceHandler
	// noArchivedList plays a server older than ListArchivedWorktrees.
	noArchivedList bool
	mu             sync.Mutex
	pages          [][]*reliantv1.Worktree
	archived       []*reliantv1.Worktree
	listedIn       []string
	archive        []*reliantv1.ArchiveWorktreeRequest
}

func (f *fakeWorkspaces) ListWorktrees(_ context.Context, req *connect.Request[reliantv1.ListWorktreesRequest]) (*connect.Response[reliantv1.ListWorktreesResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listedIn = append(f.listedIn, req.Msg.GetProjectId())
	page := 0
	if req.Msg.GetPageToken() == "p2" {
		page = 1
	}
	resp := &reliantv1.ListWorktreesResponse{Worktrees: f.pages[page]}
	if page+1 < len(f.pages) {
		resp.NextPageToken = "p2"
	}
	return connect.NewResponse(resp), nil
}

func (f *fakeWorkspaces) ListArchivedWorktrees(ctx context.Context, req *connect.Request[reliantv1.ListArchivedWorktreesRequest]) (*connect.Response[reliantv1.ListArchivedWorktreesResponse], error) {
	if f.noArchivedList {
		return f.UnimplementedWorktreeServiceHandler.ListArchivedWorktrees(ctx, req)
	}
	return connect.NewResponse(&reliantv1.ListArchivedWorktreesResponse{Worktrees: f.archived}), nil
}

func (f *fakeWorkspaces) GetWorktree(_ context.Context, req *connect.Request[reliantv1.GetWorktreeRequest]) (*connect.Response[reliantv1.GetWorktreeResponse], error) {
	for _, page := range f.pages {
		for _, w := range page {
			if w.GetId() == req.Msg.GetWorktreeId() {
				return connect.NewResponse(&reliantv1.GetWorktreeResponse{Worktree: w}), nil
			}
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, nil)
}

func (f *fakeWorkspaces) ArchiveWorktree(_ context.Context, req *connect.Request[reliantv1.ArchiveWorktreeRequest]) (*connect.Response[reliantv1.ArchiveWorktreeResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.archive = append(f.archive, req.Msg)
	return connect.NewResponse(&reliantv1.ArchiveWorktreeResponse{Message: "Its directory is removed once it is clean and pushed."}), nil
}

// workspaceServer serves the fakes and points the CLI at them, from a
// directory inside the project's folder.
func workspaceServer(t *testing.T) *fakeWorkspaces {
	t.Helper()
	isolateControlPlaneEnv(t)
	t.Setenv(daemonpolicy.AgentShellEnv, "")
	require.NoError(t, os.Unsetenv(daemonpolicy.AgentShellEnv))
	dir := t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	other := filepath.Join(dir, "other")
	proj := filepath.Join(dir, "app")
	require.NoError(t, os.MkdirAll(filepath.Join(proj, "src"), 0o755))
	f := &fakeWorkspaces{pages: [][]*reliantv1.Worktree{
		{{Id: "w-main", Name: "main", Branch: "main", IsMain: true, Path: proj, Status: reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE}},
		{{Id: "w-feat", Name: "fix-login", Branch: "fix/login", Path: "/wt/fix-login", Status: reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE, LastActive: "2026-11-20T10:00:00Z"}},
	}, archived: []*reliantv1.Worktree{{Id: "w-old", Name: "old", Branch: "old", DeletedAt: proto.String("2026-11-19T10:00:00Z")}}}
	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewWorktreeServiceHandler(f))
	mux.Handle(reliantv1connect.NewProjectServiceHandler(workspaceProjects{projects: []*reliantv1.Project{
		{Id: "p-other", Name: "other", Path: other},
		{Id: "p-app", Name: "app", Path: proj},
	}}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv(envServerURL, srv.URL)
	t.Setenv(envToken, "rlat_login")
	t.Chdir(filepath.Join(proj, "src"))
	return f
}

// TestWorktreeListAndArchiveDialTheServer: `reliant worktree list` is the
// app's workspace list (every page, of the project holding the current
// directory), and `archive` archives one named by name or id through
// ArchiveWorktree (north-star AW-38).
func TestWorktreeListAndArchiveDialTheServer(t *testing.T) {
	f := workspaceServer(t)

	out, err := executeRoot(t, "worktree", "list")
	require.NoError(t, err, out)
	assert.Contains(t, out, "main (main)")
	assert.Contains(t, out, "fix/login", "the second page is listed")
	assert.Equal(t, []string{"p-app", "p-app"}, f.listedIn, "the project holding the current directory, both pages")

	out, err = executeRoot(t, "worktree", "list", "--json")
	require.NoError(t, err, out)
	var doc struct {
		Worktrees []map[string]any `json:"worktrees"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &doc), out)
	require.Len(t, doc.Worktrees, 2)
	assert.Equal(t, "w-feat", doc.Worktrees[1]["id"])

	out, err = executeRoot(t, "worktree", "list", "--archived")
	require.NoError(t, err, out)
	assert.Contains(t, out, "w-old")

	out, err = executeRoot(t, "worktree", "archive", "fix-login")
	require.NoError(t, err, out)
	assert.Contains(t, out, "Archived workspace fix-login (w-feat).")
	require.Len(t, f.archive, 1)
	assert.Equal(t, "w-feat", f.archive[0].GetWorktreeId())
	assert.False(t, f.archive[0].GetDeleteGitBranch())

	_, err = executeRoot(t, "worktree", "archive", "w-feat", "--delete-branch", "--json")
	require.NoError(t, err)
	require.Len(t, f.archive, 2)
	assert.True(t, f.archive[1].GetDeleteGitBranch())

	_, err = executeRoot(t, "worktree", "archive", "main")
	require.ErrorContains(t, err, "main checkout")
	_, err = executeRoot(t, "worktree", "archive", "nope")
	require.ErrorContains(t, err, `no live workspace of app has the id, name or branch "nope"`)
	assert.Len(t, f.archive, 2, "a refused archive calls nothing")
}

// An agent's shell is refused `worktree archive` before anything is called;
// listing stays open to it.
func TestWorktreeArchiveRefusedInAnAgentShell(t *testing.T) {
	f := workspaceServer(t)
	t.Setenv(daemonpolicy.AgentShellEnv, "1")

	_, err := executeRoot(t, "worktree", "archive", "fix-login")
	require.ErrorIs(t, err, errAgentCannotArchiveWorkspaces)
	assert.Empty(t, f.archive)

	out, err := executeRoot(t, "worktree", "list")
	require.NoError(t, err, out)
	assert.Contains(t, out, "fix-login")
}

// A server that predates ListArchivedWorktrees answers --archived with
// Unimplemented: the CLI says the server is too old and that the live list
// still works, and the live list does.
func TestWorktreeListArchivedOnAnOlderServer(t *testing.T) {
	f := workspaceServer(t)
	f.noArchivedList = true

	_, err := executeRoot(t, "worktree", "list", "--archived")
	require.ErrorContains(t, err, "this Reliant server predates the archived list")
	assert.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))

	out, err := executeRoot(t, "worktree", "list")
	require.NoError(t, err, out)
	assert.Contains(t, out, "main (main)")
}
