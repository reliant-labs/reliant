package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/testkit"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/workspacecreate"
)

// The class of a refused create must survive the daemon → server hop.
//
// The hop is JSON: the server marshals the command, the daemon's registry
// runs the handler, and the server unmarshals the reply into its own struct.
// No Go type crosses it. This drives the REAL daemon handler through the
// registry and the REAL server-side Finish, joined by exactly that marshal /
// unmarshal, so a field renamed or dropped on either side fails here rather
// than as a Sentry issue in prod.

// registryMachine is workspacecreate.Machine served by this process's daemon
// command registry, with the same JSON round trip the NATS router does.
type registryMachine struct{}

func (registryMachine) Send(ctx context.Context, commandType string, payload, resp any, _ int32) error {
	in, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	out, err := DefaultRegistry().Handle(ctx, commandType, in)
	if err != nil {
		return fmt.Errorf("daemon command %s: %w", commandType, err)
	}
	if resp == nil {
		return nil
	}
	return json.Unmarshal(out, resp)
}

func finishAcrossTheBoundary(t *testing.T, base string) (*db.Worktree, error) {
	t.Helper()
	repo := diagnoseRepo(t)

	var settled *db.Worktree
	store := &workspacecreate.MockStore{
		GetLiveWorktreeByNameFunc: func(context.Context, string, string) (*db.Worktree, error) { return nil, nil },
		ListWorktreesByBranchFunc: func(context.Context, string, string) ([]*db.Worktree, error) { return nil, nil },
		CreateWorktreeFunc:        func(context.Context, *db.Worktree) error { return nil },
		UpdateWorktreeFunc: func(_ context.Context, w *db.Worktree) error {
			row := *w
			settled = &row
			return nil
		},
	}
	req := workspacecreate.Request{
		Project:       &db.Project{ID: "p1", Name: "reliant", Path: repo},
		Repos:         []*core.Repo{{ID: "r1", Name: "reliant", RelativePath: ""}},
		Name:          "telemetry",
		Branch:        "feat/telemetry-delivery-c3",
		BaseBranch:    base,
		OwnerDaemonID: "d1",
		WorkspaceID:   uniqueWorkspaceID(t, "reliant-telemetry"),
	}
	wt, err := workspacecreate.Insert(context.Background(), store, req)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	err = workspacecreate.Finish(context.Background(), store, registryMachine{}, req, wt)
	return settled, err
}

func TestWorktreeCreate_UserErrorClassSurvivesTheDaemonBoundary(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts and worktrees on disk")
	t.Parallel()
	row, err := finishAcrossTheBoundary(t, "feat/telemetry-delivery-c2")
	if err == nil {
		t.Fatal("expected the create to fail: the base branch does not exist")
	}
	if got := svcerr.Classify(err); got != svcerr.ClassUser {
		t.Fatalf("class = %v, want user: the daemon's diagnosis was lost crossing the boundary (err: %v)", got, err)
	}
	if got := svcerr.Code(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want %v", got, connect.CodeNotFound)
	}
	want := "base branch 'feat/telemetry-delivery-c2' doesn't exist in reliant"
	if msg := err.Error(); len(msg) < len(want) || msg[:len(want)] != want {
		t.Errorf("message = %q, want it to start %q", msg, want)
	}
	if row == nil || row.Status != int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED) {
		t.Fatalf("row = %+v, want it settled FAILED", row)
	}
	if row.FailureReason != err.Error() {
		t.Errorf("failure_reason = %q, want the user-facing reason %q", row.FailureReason, err.Error())
	}
}

// The same path succeeds for a base that exists, so the test above cannot
// pass by the boundary being broken in some other way.
func TestWorktreeCreate_SucceedsAcrossTheDaemonBoundary(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts and worktrees on disk")
	t.Parallel()
	row, err := finishAcrossTheBoundary(t, "main")
	if err != nil {
		t.Fatalf("create from main failed: %v", err)
	}
	if row == nil || row.Status != int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE) || row.Path == "" {
		t.Fatalf("row = %+v, want it ACTIVE with a path", row)
	}
}
