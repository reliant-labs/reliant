// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// Fixtures shared by the workflow/run lane's tests (P5b L2). Each test seeds
// rows it spells out and asserts the explicit rows or values it expects back.

// l2Base is a fixed instant, so created_at orderings are total.
var l2Base = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

// seedChat is a committed chat owned by the seed user.
func seedChat(tb testing.TB, env *storetest.Env, id string) {
	tb.Helper()
	if err := store.CreateChat(context.Background(), env.ORM, &store.Chat{
		ID: id, Title: id, ProjectID: storetest.SeedProjectID, UserID: storetest.SeedUserID,
		CreatedAt: l2Base, UpdatedAt: l2Base, LastActive: l2Base,
	}); err != nil {
		tb.Fatalf("seed chat %s: %v", id, err)
	}
}

// newWorkflow is a workflow fixture whose thread is its own id.
func newWorkflow(id, chatID string, parentID *string, status core.WorkflowStatus, createdAt time.Time) *core.Workflow {
	return &core.Workflow{
		ID: id, ParentID: parentID, ChatID: chatID, WorkflowName: "builtin://agent",
		Thread: id, Status: status, CreatedAt: createdAt,
	}
}

func ptr[T any](v T) *T { return &v }

func mustExec(tb testing.TB, env *storetest.Env, q string, args ...any) {
	tb.Helper()
	if _, err := env.SQL.Exec(q, args...); err != nil {
		tb.Fatalf("%s: %v", q, err)
	}
}

// requireSame fails unless got equals want. Instants compare with time.Equal:
// timestamptz scans into a different location than a literal carries.
func requireSame(tb testing.TB, what string, got, want any) {
	tb.Helper()
	if diff := cmp.Diff(want, got, cmp.Comparer(func(a, b time.Time) bool { return a.Equal(b) })); diff != "" {
		tb.Fatalf("%s: got differs from want (-want +got):\n%s", what, diff)
	}
}

// requireRecent fails unless at is within a minute of now: the instant a
// NOW() the database stamped must be.
func requireRecent(tb testing.TB, what string, at time.Time) {
	tb.Helper()
	if d := time.Since(at); d > time.Minute || d < -time.Minute {
		tb.Fatalf("%s = %v, want a stamp of now (%v)", what, at, time.Now())
	}
}

func workflowIDs(ws []*core.Workflow) []string {
	ids := make([]string, len(ws))
	for i, w := range ws {
		ids[i] = w.ID
	}
	return ids
}
