// Copyright (c) 2025 Reliant Labs
package worktree_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	worktree "github.com/reliant-labs/reliant/internal/handlers/worktree"
)

// The polled git reads send the worktree.validate_path probe beside the git
// command, not before it (gitReadOnDisk): a read that answers stands without
// the probe, and a read that fails reports a missing directory as before.

// readRouter answers worktree commands with fn, for both send paths.
func readRouter(fn func(ctx context.Context, commandType string, payload []byte) ([]byte, error)) *worktree.StubRouter {
	send := func(ctx context.Context, _, commandType string, payload []byte, _ int32) ([]byte, error) {
		return fn(ctx, commandType, payload)
	}
	return &worktree.StubRouter{
		SendDaemonCommandFunc: send,
		SendDaemonCommandToDaemonFunc: func(ctx context.Context, userID, _, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
			return send(ctx, userID, commandType, payload, timeoutMs)
		},
	}
}

func readService(t *testing.T, store *worktree.MockRepository, router *worktree.StubRouter) *worktree.Service {
	t.Helper()
	return worktree.NewTestWorktree(t, worktree.WithWorktreeDeps(worktree.Deps{Repo: store, Router: router}))
}

func singleRepoStore(t *testing.T) *worktree.MockRepository {
	store := ownedWorktreeRepo(t)
	store.ListReposByProjectFunc = func(context.Context, string) ([]*core.Repo, error) {
		return []*core.Repo{{ID: "r-1", Name: "app"}}, nil
	}
	return store
}

func TestGitReadSendsTheDirectoryProbeBesideTheRead(t *testing.T) {
	t.Parallel()
	// Each command waits until the other has been sent: sent one after the
	// other, neither ever answers.
	var both sync.WaitGroup
	both.Add(2)
	router := readRouter(func(ctx context.Context, commandType string, _ []byte) ([]byte, error) {
		switch commandType {
		case "worktree.validate_path", "worktree.git_changes":
			both.Done()
		default:
			return nil, fmt.Errorf("unexpected command %s", commandType)
		}
		done := make(chan struct{})
		go func() { both.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			return nil, errors.New("the commands were sent one after the other")
		}
		if commandType == "worktree.validate_path" {
			return json.Marshal(map[string]any{"exists": true})
		}
		return json.Marshal(map[string]any{"branch": "feature", "total_files": 1})
	})
	resp, err := readService(t, singleRepoStore(t), router).GetWorktreeChanges(worktree.AuthedContext(t),
		connect.NewRequest(&reliantv1.GetWorktreeChangesRequest{WorktreeId: ownWorktreeID}))
	require.NoError(t, err)
	assert.Equal(t, "feature", resp.Msg.GetBranch())
}

func TestGitReadThatAnswersDoesNotWaitForTheProbe(t *testing.T) {
	t.Parallel()
	var probeReturned atomic.Bool
	router := readRouter(func(ctx context.Context, commandType string, _ []byte) ([]byte, error) {
		switch commandType {
		case "worktree.validate_path":
			// A slow probe: it answers only once the request is over.
			<-ctx.Done()
			probeReturned.Store(true)
			return nil, ctx.Err()
		case "worktree.git_status":
			return json.Marshal(map[string]any{"branch": "feature", "status": "clean"})
		}
		return nil, fmt.Errorf("unexpected command %s", commandType)
	})
	ctx, cancel := context.WithTimeout(worktree.AuthedContext(t), 10*time.Second)
	defer cancel()
	resp, err := readService(t, singleRepoStore(t), router).GetWorktreeGitStatus(ctx,
		connect.NewRequest(&reliantv1.GetWorktreeGitStatusRequest{WorktreeId: ownWorktreeID}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetClean())
	require.NoError(t, ctx.Err(), "the read waited for the probe")
	assert.True(t, probeReturned.Load(), "the probe outlived the rpc")
}

func TestGitReadOfAMissingDirectoryReportsTheDirectory(t *testing.T) {
	t.Parallel()
	router := readRouter(func(_ context.Context, commandType string, _ []byte) ([]byte, error) {
		switch commandType {
		case "worktree.validate_path":
			return json.Marshal(map[string]any{"exists": false, "error": "not_found"})
		case "worktree.git_changes":
			return json.Marshal(map[string]any{"error": "fatal: not a git repository"})
		}
		return nil, fmt.Errorf("unexpected command %s", commandType)
	})
	_, err := readService(t, singleRepoStore(t), router).GetWorktreeChanges(worktree.AuthedContext(t),
		connect.NewRequest(&reliantv1.GetWorktreeChangesRequest{WorktreeId: ownWorktreeID}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "err = %v", err)
	assert.Contains(t, svcerr.ToConnect(err).Message(), "worktree directory does not exist")
}

func TestGitReadFailureWithTheDirectoryPresentIsTheReadsOwn(t *testing.T) {
	t.Parallel()
	router := readRouter(func(_ context.Context, commandType string, _ []byte) ([]byte, error) {
		switch commandType {
		case "worktree.validate_path":
			return json.Marshal(map[string]any{"exists": true})
		case "worktree.git_changes":
			return json.Marshal(map[string]any{"error": "fatal: bad revision"})
		}
		return nil, fmt.Errorf("unexpected command %s", commandType)
	})
	_, err := readService(t, singleRepoStore(t), router).GetWorktreeChanges(worktree.AuthedContext(t),
		connect.NewRequest(&reliantv1.GetWorktreeChangesRequest{WorktreeId: ownWorktreeID}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, svcerr.Code(err), "err = %v", err)
	assert.Contains(t, svcerr.ToConnect(err).Message(), "bad revision")
}

func TestGitReadOfAnArchivedWorktreeSendsNothing(t *testing.T) {
	t.Parallel()
	store := singleRepoStore(t)
	get := store.GetWorktreeFunc
	store.GetWorktreeFunc = func(ctx context.Context, id string) (*db.Worktree, error) {
		w, err := get(ctx, id)
		if w != nil {
			at := time.Now()
			w.DeletedAt = &at
		}
		return w, err
	}
	router := readRouter(func(_ context.Context, commandType string, _ []byte) ([]byte, error) {
		return nil, fmt.Errorf("unexpected command %s", commandType)
	})
	_, err := readService(t, store, router).GetWorktreeGitStatus(worktree.AuthedContext(t),
		connect.NewRequest(&reliantv1.GetWorktreeGitStatusRequest{WorktreeId: ownWorktreeID}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "err = %v", err)
	assert.Contains(t, svcerr.ToConnect(err).Message(), "archived")
}

func TestGitCommitsWithTheDirectoryPresentAnswerEmptyWhenTheSendFails(t *testing.T) {
	t.Parallel()
	router := readRouter(func(_ context.Context, commandType string, _ []byte) ([]byte, error) {
		switch commandType {
		case "worktree.validate_path":
			return json.Marshal(map[string]any{"exists": true})
		case "worktree.git_commits":
			return nil, errors.New("git log failed")
		}
		return nil, fmt.Errorf("unexpected command %s", commandType)
	})
	resp, err := readService(t, singleRepoStore(t), router).GetWorktreeCommits(worktree.AuthedContext(t),
		connect.NewRequest(&reliantv1.GetWorktreeCommitsRequest{WorktreeId: ownWorktreeID}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetCommits())
}

func TestRepoStatusesAskEveryRepoAtOnce(t *testing.T) {
	t.Parallel()
	store := ownedWorktreeRepo(t)
	repos := []*core.Repo{
		{ID: "r-1", Name: "a", RelativePath: "a"},
		{ID: "r-2", Name: "b", RelativePath: "b"},
		{ID: "r-3", Name: "c", RelativePath: "c"},
	}
	store.ListReposByProjectFunc = func(context.Context, string) ([]*core.Repo, error) { return repos, nil }
	// Every git_status waits until all three were sent.
	var all sync.WaitGroup
	all.Add(len(repos))
	router := readRouter(func(_ context.Context, commandType string, payload []byte) ([]byte, error) {
		switch commandType {
		case "worktree.validate_path":
			return json.Marshal(map[string]any{"exists": true})
		case "worktree.git_status":
		default:
			return nil, fmt.Errorf("unexpected command %s", commandType)
		}
		all.Done()
		done := make(chan struct{})
		go func() { all.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			return nil, errors.New("the repos were asked one after the other")
		}
		var req map[string]string
		_ = json.Unmarshal(payload, &req)
		return json.Marshal(map[string]any{"branch": req["worktree_path"]})
	})
	resp, err := readService(t, store, router).ListWorktreeRepoStatuses(worktree.AuthedContext(t),
		connect.NewRequest(&reliantv1.ListWorktreeRepoStatusesRequest{WorktreeId: ownWorktreeID}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetStatuses(), 3)
	for i, st := range resp.Msg.GetStatuses() {
		assert.Equal(t, repos[i].ID, st.GetRepoId(), "rows keep the repos' order")
		assert.Empty(t, st.GetError())
		assert.Equal(t, "/work/.reliant/worktrees/w-1/"+repos[i].RelativePath, st.GetCurrentBranch())
	}
}

func TestRepoStatusesOfAMissingDirectoryReportTheDirectory(t *testing.T) {
	t.Parallel()
	store := ownedWorktreeRepo(t)
	store.ListReposByProjectFunc = func(context.Context, string) ([]*core.Repo, error) {
		return []*core.Repo{{ID: "r-1", Name: "a", RelativePath: "a"}}, nil
	}
	router := readRouter(func(_ context.Context, commandType string, _ []byte) ([]byte, error) {
		switch commandType {
		case "worktree.validate_path":
			return json.Marshal(map[string]any{"exists": false, "error": "not_found"})
		case "worktree.git_status":
			return json.Marshal(map[string]any{"error": "no such directory"})
		}
		return nil, fmt.Errorf("unexpected command %s", commandType)
	})
	_, err := readService(t, store, router).ListWorktreeRepoStatuses(worktree.AuthedContext(t),
		connect.NewRequest(&reliantv1.ListWorktreeRepoStatusesRequest{WorktreeId: ownWorktreeID}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "err = %v", err)
}

func TestRepoStatusesKeepAOneRepoFailureInItsRow(t *testing.T) {
	t.Parallel()
	store := ownedWorktreeRepo(t)
	store.ListReposByProjectFunc = func(context.Context, string) ([]*core.Repo, error) {
		return []*core.Repo{{ID: "r-1", Name: "a", RelativePath: "a"}, {ID: "r-2", Name: "b", RelativePath: "b"}}, nil
	}
	router := readRouter(func(_ context.Context, commandType string, payload []byte) ([]byte, error) {
		switch commandType {
		case "worktree.validate_path":
			return json.Marshal(map[string]any{"exists": true})
		case "worktree.git_status":
			var req map[string]string
			_ = json.Unmarshal(payload, &req)
			if req["worktree_path"] == "/work/.reliant/worktrees/w-1/b" {
				return json.Marshal(map[string]any{"error": "not a git repository"})
			}
			return json.Marshal(map[string]any{"branch": "feature"})
		}
		return nil, fmt.Errorf("unexpected command %s", commandType)
	})
	resp, err := readService(t, store, router).ListWorktreeRepoStatuses(worktree.AuthedContext(t),
		connect.NewRequest(&reliantv1.ListWorktreeRepoStatusesRequest{WorktreeId: ownWorktreeID}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetStatuses(), 2)
	assert.Empty(t, resp.Msg.GetStatuses()[0].GetError())
	assert.Equal(t, "not a git repository", resp.Msg.GetStatuses()[1].GetError())
}
