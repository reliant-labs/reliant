// Copyright (c) 2025 Reliant Labs
package workspacecreate

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

type sent struct {
	cmd     string
	payload map[string]any
}

// fakeMachine answers worktree.create by placing the checkout under root.
type fakeMachine struct {
	mu       sync.Mutex
	sent     []sent
	root     string
	failRepo string // project_path suffix whose create returns Success=false
	copyErr  error
}

func (f *fakeMachine) Send(_ context.Context, cmd string, payload, resp any, _ int32) error {
	b, _ := json.Marshal(payload)
	var p map[string]any
	_ = json.Unmarshal(b, &p)
	f.mu.Lock()
	f.sent = append(f.sent, sent{cmd, p})
	f.mu.Unlock()
	switch cmd {
	case "worktree.create":
		pp, _ := p["project_path"].(string)
		out := resp.(*struct {
			Success      bool   `json:"success"`
			WorktreePath string `json:"worktree_path"`
			BaseBranch   string `json:"base_branch,omitempty"`
			Error        string `json:"error,omitempty"`
		})
		if f.failRepo != "" && filepath.Base(pp) == f.failRepo {
			out.Error = "fatal: invalid reference"
			return nil
		}
		out.Success = true
		sub, _ := p["sub_path"].(string)
		out.WorktreePath = filepath.Join(f.root, sub)
		out.BaseBranch = "main"
	case "worktree.copy_paths":
		return f.copyErr
	}
	return nil
}

func (f *fakeMachine) cmds(name string) []sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []sent
	for _, s := range f.sent {
		if s.cmd == name {
			out = append(out, s)
		}
	}
	return out
}

type fakeStore struct {
	rows      map[string]db.Worktree
	updateErr error
}

func (s *fakeStore) CreateWorktree(_ context.Context, w *db.Worktree) error {
	if s.rows == nil {
		s.rows = map[string]db.Worktree{}
	}
	s.rows[w.ID] = *w
	return nil
}

func (s *fakeStore) ArchiveWorktree(_ context.Context, id string) error {
	w := s.rows[id]
	now := time.Now()
	w.DeletedAt = &now
	s.rows[id] = w
	return nil
}

func (s *fakeStore) GetLiveWorktreeByName(_ context.Context, projectID, name string) (*db.Worktree, error) {
	for _, w := range s.rows {
		if w.ProjectID == projectID && w.Name == name && w.DeletedAt == nil {
			w := w
			return &w, nil
		}
	}
	return nil, nil
}

func (s *fakeStore) ListWorktreesByBranch(_ context.Context, projectID, branch string) ([]*db.Worktree, error) {
	var out []*db.Worktree
	for _, w := range s.rows {
		if w.ProjectID == projectID && w.Branch == branch {
			w := w
			out = append(out, &w)
		}
	}
	return out, nil
}

func (s *fakeStore) UpdateWorktree(_ context.Context, w *db.Worktree) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	s.rows[w.ID] = *w
	return nil
}

func twoRepoReq() Request {
	return Request{
		Project: &db.Project{ID: "p1", Name: "demo", Path: "/proj"},
		Repos: []*core.Repo{
			{ID: "r1", Name: "a", RelativePath: "a"},
			{ID: "r2", Name: "b", RelativePath: "b"},
		},
		Name:          "feat",
		Branch:        "feat-x",
		OwnerDaemonID: "d1",
	}
}

func TestInsertThenFinishSuccess(t *testing.T) {
	st, m := &fakeStore{}, &fakeMachine{root: "/home/.reliant/worktrees/demo/feat-1234abcd"}
	req := twoRepoReq()
	req.CopyPaths = []string{".env"}

	wt, err := Insert(context.Background(), st, req)
	require.NoError(t, err)
	assert.Equal(t, int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_CREATING), st.rows[wt.ID].Status)
	assert.Equal(t, "d1", *wt.DaemonID)

	require.NoError(t, Finish(context.Background(), st, m, req, wt))
	row := st.rows[wt.ID]
	assert.Equal(t, int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE), row.Status)
	assert.Equal(t, m.root, row.Path)
	assert.Equal(t, map[string]string{"r1": "main", "r2": "main"}, row.BaseBranches)

	creates := m.cmds("worktree.create")
	require.Len(t, creates, 2)
	for _, c := range creates {
		assert.Equal(t, wt.ID, c.payload["worktree_id"])
		assert.Contains(t, c.payload["workspace_id"], "demo")
	}
	cp := m.cmds("worktree.copy_paths")
	require.Len(t, cp, 1)
	assert.Equal(t, "/proj", cp[0].payload["source_root"])
	assert.Equal(t, m.root, cp[0].payload["dest_root"])
	assert.Empty(t, m.cmds("worktree.delete_directory"))
}

func TestInsertDefaultsBranch(t *testing.T) {
	req := twoRepoReq()
	req.Branch = ""
	wt, err := Insert(context.Background(), &fakeStore{}, req)
	require.NoError(t, err)
	assert.Contains(t, wt.Branch, "worktree/feat-")
}

func TestFinishRepoFailureRollsBackEverything(t *testing.T) {
	st, m := &fakeStore{}, &fakeMachine{root: "/w/demo/feat-1", failRepo: "b"}
	req := twoRepoReq()
	wt, _ := Insert(context.Background(), st, req)

	err := Finish(context.Background(), st, m, req, wt)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid branch or reference")

	row := st.rows[wt.ID]
	assert.Equal(t, int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED), row.Status)
	assert.Empty(t, row.Path)

	var deleted []string
	for _, d := range m.cmds("worktree.delete_directory") {
		deleted = append(deleted, d.payload["worktree_path"].(string))
	}
	assert.ElementsMatch(t, []string{"/w/demo/feat-1/a", "/w/demo/feat-1"}, deleted)
}

func TestFinishForceCleansEachRepo(t *testing.T) {
	st, m := &fakeStore{}, &fakeMachine{root: "/w/r"}
	req := twoRepoReq()
	req.Force = true
	wt, _ := Insert(context.Background(), st, req)
	require.NoError(t, Finish(context.Background(), st, m, req, wt))
	fc := m.cmds("worktree.force_cleanup")
	require.Len(t, fc, 2)
	assert.Equal(t, "feat-x", fc[0].payload["branch"])
}

func TestFinishCopyFailureIsNonFatal(t *testing.T) {
	st, m := &fakeStore{}, &fakeMachine{root: "/w/r", copyErr: errors.New("boom")}
	req := twoRepoReq()
	req.CopyPaths = []string{".env"}
	req.CopySource = "/src/ws"
	wt, _ := Insert(context.Background(), st, req)
	require.NoError(t, Finish(context.Background(), st, m, req, wt))
	assert.Equal(t, int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE), st.rows[wt.ID].Status)
	assert.Equal(t, "/src/ws", m.cmds("worktree.copy_paths")[0].payload["source_root"])
}

func TestFinishPerRepoBaseOverride(t *testing.T) {
	st, m := &fakeStore{}, &fakeMachine{root: "/w/r"}
	req := twoRepoReq()
	req.BaseBranch = "develop"
	req.BaseBranches = map[string]string{"r2": "master"}
	wt, _ := Insert(context.Background(), st, req)
	require.NoError(t, Finish(context.Background(), st, m, req, wt))
	got := map[string]any{}
	for _, c := range m.cmds("worktree.create") {
		got[c.payload["sub_path"].(string)] = c.payload["base_branch"]
	}
	assert.Equal(t, map[string]any{"a": "develop", "b": "master"}, got)
}

func TestFinishRecordFailureRollsBack(t *testing.T) {
	st, m := &fakeStore{}, &fakeMachine{root: "/w/r"}
	req := twoRepoReq()
	wt, _ := Insert(context.Background(), st, req)
	st.updateErr = errors.New("db down")
	err := Finish(context.Background(), st, m, req, wt)
	require.Error(t, err)
	assert.Len(t, m.cmds("worktree.delete_directory"), 3)
}
