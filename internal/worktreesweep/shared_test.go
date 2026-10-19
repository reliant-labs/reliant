// Copyright (c) 2025 Reliant Labs
package worktreesweep

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// liveProject creates another project of the env's user at dir, with one repo
// per rel, and its live main row at dir.
func (e *env) liveProject(t *testing.T, dir string, rels ...string) *db.Worktree {
	t.Helper()
	now := time.Now().UTC()
	p := &db.Project{ID: "p-" + uuid.NewString(), UserID: e.userID, Name: filepath.Base(dir), Path: dir, CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, e.repo.CreateProject(e.ctx, p))
	for _, rel := range rels {
		require.NoError(t, e.repo.CreateRepo(e.ctx, &core.Repo{ID: uuid.NewString(), ProjectID: p.ID, Name: rel, RelativePath: rel, CreatedAt: now, UpdatedAt: now}))
	}
	wt := &db.Worktree{ID: uuid.NewString(), Name: "main", Path: dir, Branch: "main", BaseBranch: "main", ProjectID: p.ID, IsMain: true,
		DaemonID: &e.daemon, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, e.repo.CreateWorktree(e.ctx, wt))
	return wt
}

// A project rooted at a home directory has a live main row whose path contains
// every worktree on the machine. That must not keep archived worktrees: it
// kept every one of them forever.
func TestSweep_ALiveRowAtAParentPathDoesNotShieldArchivedWorktrees(t *testing.T) {
	e := newEnv(t)
	wt, path := e.worktree(t, "under-home", true)
	e.liveProject(t, filepath.Dir(e.root), ".")
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	assert.False(t, exists(path), "an archived worktree under a live project's directory is still removed")
	got := e.get(t, wt.ID)
	require.NotNil(t, got.CleanupMetadata)
	assert.True(t, got.CleanupMetadata.DirectoryDeleted)
	assert.Empty(t, got.CleanupMetadata.HeldReason)
}

// A live row still keeps its OWN checkout: here the archived row's directory is
// the checkout another project's live row has for its repo "x".
func TestSweep_ALiveRowStillShieldsItsOwnCheckout(t *testing.T) {
	e := newEnv(t)
	wt, path := e.worktree(t, "x", true)
	e.liveProject(t, filepath.Dir(path), "x")
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	assert.True(t, exists(path), "a live row's checkout is never removed")
	assert.Empty(t, e.sender.calls, "a kept archived row is never sent to the daemon")
	meta := e.get(t, wt.ID).CleanupMetadata
	require.NotNil(t, meta, "the hold is recorded, never silent")
	assert.Equal(t, string(worktreereclaim.ReasonSharedWithLive), meta.HeldReason)
	assert.Contains(t, meta.HeldDetail, "checkout of the open workspace at "+filepath.Dir(path))
	assert.NotNil(t, meta.CheckedAt, "the hold backs off like any other")
	assert.False(t, worktreereclaim.ReasonSharedWithLive.Removable(), "Clean up never removes a live row's checkout")

	// The confirmed Clean up skips it with the same reason.
	results, _, err := e.sweeper.Cleanup(e.ctx, e.repo, e.userID, e.daemon, []string{wt.ID})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, CleanupSkipped, results[0].Outcome)
	assert.True(t, exists(path))
}

// A live row inside an archived directory keeps it: removing the archived
// directory would remove the live one.
func TestSweep_ALiveRowInsideAnArchivedDirectoryShieldsIt(t *testing.T) {
	e := newEnv(t)
	wt, path := e.worktree(t, "outer", true)
	inner := filepath.Join(path, "nested")
	require.NoError(t, os.MkdirAll(inner, 0o755))
	e.liveProject(t, inner)
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	assert.True(t, exists(inner))
	meta := e.get(t, wt.ID).CleanupMetadata
	require.NotNil(t, meta)
	assert.Equal(t, string(worktreereclaim.ReasonSharedWithLive), meta.HeldReason)
	assert.Contains(t, meta.HeldDetail, inner)
}

func TestSharedWithLive(t *testing.T) {
	arch := &core.Worktree{ID: "arch", Path: "/home/u/.reliant/worktrees/proj/wt-1"}
	mk := func(path string, complete bool, rels ...string) occupied {
		o := occupied{id: "live", path: path, dirs: []string{path}, complete: complete}
		for _, r := range rels {
			o.dirs = append(o.dirs, filepath.Join(path, r))
		}
		return o
	}
	for _, tc := range []struct {
		name   string
		live   occupied
		shared bool
	}{
		{"same directory", mk(arch.Path, true), true},
		{"its checkout for a repo", mk("/home/u/.reliant/worktrees/proj", true, "wt-1"), true},
		{"a live checkout inside it", mk("/home/u/.reliant/worktrees/proj", true, "wt-1/sub"), true},
		{"a live directory inside it", mk(arch.Path+"/sub", true), true},
		{"a project at the home directory", mk("/home/u", true, "."), false},
		{"a project at the home directory with nested repos", mk("/home/u", true, "projects/a", "projects/b"), false},
		{"a project at the filesystem root", mk("/", true), false},
		{"the parent directory", mk("/home/u/.reliant/worktrees/proj", true), false},
		{"a sibling with a common prefix", mk(arch.Path+"-other", true), false},
		{"a container whose checkouts are unknown fails closed", mk("/home/u", false), true},
		{"unknown checkouts elsewhere keep nothing", mk("/srv/other", false), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail, shared := sharedWithLive(arch, []occupied{tc.live})
			assert.Equal(t, tc.shared, shared, detail)
			if shared {
				assert.NotEmpty(t, detail)
			}
		})
	}
	_, shared := sharedWithLive(arch, []occupied{{id: arch.ID, path: arch.Path, dirs: []string{arch.Path}, complete: true}})
	assert.False(t, shared, "a row never keeps itself")
}

func TestOccupiedBy_ListsEachLiveRowsCheckouts(t *testing.T) {
	repos := map[string][]*core.Repo{"p1": {{RelativePath: "."}, {RelativePath: "forge"}}}
	got := occupiedBy([]core.WorktreePath{{ID: "a", Path: "/w/a/", ProjectID: "p1"}, {ID: "b", Path: "/w/b", ProjectID: "gone"}},
		func(id string) ([]*core.Repo, bool) { r, ok := repos[id]; return r, ok })
	require.Len(t, got, 2)
	assert.Equal(t, []string{"/w/a", "/w/a/forge"}, got[0].dirs)
	assert.True(t, got[0].complete)
	assert.False(t, got[1].complete, "a project whose repos could not be listed is incomplete")
}
