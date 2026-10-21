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

// Fixtures shared by the workflow/run lane's tests (P5b L2). Each ported
// query's test writes through one layer and reads through the other, on one
// database (internal/db/SCHEMA.md rule 6): env.Repo is the twin.

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

// requireSame fails unless the port and its twin returned equal values.
// Instants compare with time.Equal: the two layers scan timestamptz into
// different locations.
func requireSame(tb testing.TB, what string, port, twin any) {
	tb.Helper()
	if diff := cmp.Diff(twin, port, cmp.Comparer(func(a, b time.Time) bool { return a.Equal(b) })); diff != "" {
		tb.Fatalf("%s: the port and its sqlc twin disagree (-twin +port):\n%s", what, diff)
	}
}

func workflowIDs(ws []*core.Workflow) []string {
	ids := make([]string, len(ws))
	for i, w := range ws {
		ids[i] = w.ID
	}
	return ids
}
