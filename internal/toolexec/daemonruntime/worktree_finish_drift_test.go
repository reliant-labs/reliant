package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/workspacecreate"
	"github.com/stretchr/testify/require"
)

// This deliberately goes through Finish, not just the daemon handler: an
// earlier force_cleanup command would destroy the preexisting branch first.
func TestWorktreeFinishDriftPreservesExistingWorktreeAndBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("real git fetch and full Finish daemon path; full scoped test")
	}
	r := newBaseRepos(t)
	expected := r.run(r.clone, "rev-parse", "refs/remotes/origin/main")
	existing := filepath.Join(t.TempDir(), "existing")
	r.run(r.clone, "worktree", "add", "-b", "worktree/review", existing, expected)
	sentinel := filepath.Join(existing, "uncommitted.txt")
	require.NoError(t, os.WriteFile(sentinel, []byte("keep my work"), 0600))
	store := &finishDriftStore{}
	machine := &finishDriftMachine{}
	req := workspacecreate.Request{Project: &db.Project{ID: "p", Name: "p", Path: r.clone}, Repos: []*core.Repo{{ID: "r", Name: "r", RelativePath: ""}}, Name: "review", Branch: "worktree/review", Force: true, FetchRepoID: "r", FetchBranch: "feat/x", ExpectedHead: expected}
	wt, err := workspacecreate.Insert(context.Background(), store, req)
	require.NoError(t, err)
	err = workspacecreate.Finish(context.Background(), store, machine, req, wt)
	require.Error(t, err)
	require.Contains(t, machine.replyError, "head moved")
	require.Equal(t, []string{"worktree.create"}, machine.commands, "no cleanup may precede drift validation or run on its refusal")
	require.Equal(t, expected, r.run(r.clone, "rev-parse", "refs/heads/worktree/review"))
	require.Equal(t, expected, r.run(existing, "rev-parse", "HEAD"))
	content, err := os.ReadFile(sentinel)
	require.NoError(t, err)
	require.Equal(t, "keep my work", string(content))
}

type finishDriftMachine struct {
	commands   []string
	replyError string
}

func (m *finishDriftMachine) Send(ctx context.Context, command string, payload, resp any, _ int32) error {
	m.commands = append(m.commands, command)
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var out []byte
	switch command {
	case "worktree.create":
		out, err = handleWorktreeCreate(ctx, data)
	case "worktree.force_cleanup":
		out, err = handleWorktreeForceCleanup(ctx, data)
	default:
		return fmt.Errorf("unexpected command %s", command)
	}
	if err != nil {
		return err
	}
	if command == "worktree.create" {
		var result worktreeCreateResponse
		_ = json.Unmarshal(out, &result)
		m.replyError = result.Error
	}
	if resp != nil {
		return json.Unmarshal(out, resp)
	}
	return nil
}

type finishDriftStore struct{}

func (*finishDriftStore) CreateWorktree(context.Context, *db.Worktree) error { return nil }
func (*finishDriftStore) UpdateWorktree(context.Context, *db.Worktree) error { return nil }
func (*finishDriftStore) ArchiveWorktree(context.Context, string) error      { return nil }
func (*finishDriftStore) GetLiveWorktreeByName(context.Context, string, string) (*db.Worktree, error) {
	return nil, nil
}
func (*finishDriftStore) ListWorktreesByBranch(context.Context, string, string) ([]*db.Worktree, error) {
	return nil, nil
}
