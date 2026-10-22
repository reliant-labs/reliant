// Copyright (c) 2025 Reliant Labs
package worktree_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	worktree "github.com/reliant-labs/reliant/internal/handlers/worktree"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// errOpaqueSecret is a store failure nobody on this side of the wire wrote: its
// text must reach the log, never the client.
var errOpaqueSecret = errors.New("dsn=postgres://u:secret@h")

func requireCode(t *testing.T, err error, code connect.Code) {
	t.Helper()
	require.Error(t, err)
	assert.Equal(t, code, svcerr.Code(err), "err = %v", err)
	assert.NotContains(t, svcerr.ToConnect(err).Message(), "secret", "the store's text must not reach the client")
}

// The ownership check keeps hiding existence — a worktree in someone else's
// project is "worktree not found", exactly like a missing one — but a failure
// to look either up is no longer "not found": it is the failure it is.
func TestWorktreeOwnership_ErrorClasses(t *testing.T) {
	t.Parallel()
	get := func(t *testing.T, store *worktree.StubRepo, id string) error {
		svc := worktree.NewTestWorktree(t, worktree.WithWorktreeDeps(worktree.Deps{Repo: store}))
		_, err := svc.GetWorktree(worktree.AuthedContext(t), connect.NewRequest(&reliantv1.GetWorktreeRequest{WorktreeId: id}))
		return err
	}

	t.Run("someone else's worktree is not found", func(t *testing.T) {
		t.Parallel()
		err := get(t, ownedWorktreeRepo(t), otherWorktreeID)
		requireCode(t, err, connect.CodeNotFound)
		assert.Equal(t, "worktree not found", svcerr.ToConnect(err).Message(), "nothing says the project exists")
	})
	t.Run("a missing worktree is not found", func(t *testing.T) {
		t.Parallel()
		requireCode(t, get(t, ownedWorktreeRepo(t), unknownID), connect.CodeNotFound)
	})

	for _, tc := range []struct {
		name string
		err  error
		code connect.Code
	}{
		{"a deadline", fmt.Errorf("query: %w", context.DeadlineExceeded), connect.CodeDeadlineExceeded},
		{"a cancellation", fmt.Errorf("query: %w", context.Canceled), connect.CodeCanceled},
		{"an opaque failure", errOpaqueSecret, connect.CodeInternal},
	} {
		t.Run("reading the worktree: "+tc.name, func(t *testing.T) {
			t.Parallel()
			store := ownedWorktreeRepo(t)
			store.GetWorktreeFunc = func(context.Context, string) (*db.Worktree, error) { return nil, tc.err }
			requireCode(t, get(t, store, ownWorktreeID), tc.code)
		})
		t.Run("reading its project: "+tc.name, func(t *testing.T) {
			t.Parallel()
			store := ownedWorktreeRepo(t)
			store.GetProjectWithUserCheckFunc = func(context.Context, string, string) (*db.Project, error) { return nil, tc.err }
			requireCode(t, get(t, store, ownWorktreeID), tc.code)
		})
	}
}

// ListWorktrees' store read: a deadline is a deadline, an outage Internal
// with the rpc's sentence and none of the store's.
func TestListWorktrees_StoreErrorClasses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		code connect.Code
	}{
		{"a deadline", fmt.Errorf("query: %w", context.DeadlineExceeded), connect.CodeDeadlineExceeded},
		{"a cancellation", fmt.Errorf("query: %w", context.Canceled), connect.CodeCanceled},
		{"an opaque failure", errOpaqueSecret, connect.CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := ownedWorktreeRepo(t)
			store.ListWorktreesFunc = func(context.Context, db.WorktreeFilters) ([]*db.Worktree, error) { return nil, tc.err }
			svc := worktree.NewTestWorktree(t, worktree.WithWorktreeDeps(worktree.Deps{Repo: store}))
			_, err := svc.ListWorktrees(worktree.AuthedContext(t), connect.NewRequest(&reliantv1.ListWorktreesRequest{ProjectId: ownProjectID}))
			requireCode(t, err, tc.code)
			if tc.code == connect.CodeInternal {
				assert.Equal(t, "failed to list worktrees", svcerr.ToConnect(err).Message())
			}
		})
	}
}

// A git operation's failure, through daemonerr: git's words (from the user's
// own machine) reach the user, the transport's do not, and the machine's
// state picks the code and keeps the marker the web waits on.
func TestPushWorktree_DaemonFailureClasses(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		send     error  // the router's failure of worktree.push
		answer   string // or the daemon's own Success=false answer
		code     connect.Code
		contains string
		withheld string
	}{
		"the daemon's git rejection": {
			send: toolexec.DaemonReportedFailure("worktree.push", "! [rejected] feature -> feature (fetch first)"),
			code: connect.CodeUnavailable, contains: "[rejected] feature -> feature",
		},
		"git refused, answered by the daemon": {
			answer: "Updates were rejected because the remote contains work that you do not have locally",
			code:   connect.CodeInternal, contains: "failed to push: Updates were rejected",
		},
		"a transport failure": {
			send: errors.New("nats: timeout waiting on reliant.daemon.d-1.cmd"),
			code: connect.CodeUnavailable, contains: "push failed", withheld: "nats",
		},
		"the machine is starting": {
			send: fmt.Errorf("resolve: %w", toolexec.ErrDaemonPending),
			code: connect.CodeUnavailable, contains: "no daemon connected",
		},
		"no machine at all": {
			send: toolexec.ErrNoDaemon,
			code: connect.CodeFailedPrecondition, contains: "no daemon available",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := ownedWorktreeRepo(t)
			store.ListReposByProjectFunc = func(context.Context, string) ([]*core.Repo, error) { return nil, nil }
			send := func(_ context.Context, _, commandType string, _ []byte, _ int32) ([]byte, error) {
				switch commandType {
				case "worktree.validate_path":
					return json.Marshal(map[string]any{"exists": true})
				case "worktree.push":
					if tc.send != nil {
						return nil, tc.send
					}
					return json.Marshal(map[string]any{"success": false, "error": tc.answer})
				}
				return nil, fmt.Errorf("unexpected command %s", commandType)
			}
			router := &worktree.StubRouter{
				SendDaemonCommandFunc: send,
				SendDaemonCommandToDaemonFunc: func(ctx context.Context, userID, _, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
					return send(ctx, userID, commandType, payload, timeoutMs)
				},
			}
			svc := worktree.NewTestWorktree(t, worktree.WithWorktreeDeps(worktree.Deps{Repo: store, Router: router}))
			_, err := svc.PushWorktree(worktree.AuthedContext(t), connect.NewRequest(&reliantv1.PushWorktreeRequest{WorktreeId: ownWorktreeID}))
			require.Error(t, err)
			assert.Equal(t, tc.code, svcerr.Code(err), "err = %v", err)
			msg := svcerr.ToConnect(err).Message()
			assert.Contains(t, msg, tc.contains)
			if tc.withheld != "" {
				assert.NotContains(t, msg, tc.withheld)
			}
		})
	}
}
