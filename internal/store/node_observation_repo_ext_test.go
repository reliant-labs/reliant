// Copyright (c) 2026 Reliant Labs
package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

func observation(id, chat, workflow, run, step string, attempt int32) *core.NodeObservation {
	return &core.NodeObservation{
		ID: id, ChatID: chat, WorkflowID: workflow, RunID: run, StepID: step, NodePath: step,
		Attempt: attempt, Source: core.NodeObservationSourceWorkflow,
		Attributes: `{"trigger":"schedule"}`,
		Events:     `[{"name":"decision","attributes":{"value":"no_cut"}}]`,
		Errors:     `[]`,
		CreatedAt:  retentionAged,
	}
}

func countObservations(t *testing.T, env *storetest.Env, where string, args ...interface{}) int {
	t.Helper()
	var n int
	if err := env.SQL.QueryRow(`SELECT count(*) FROM node_observations WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A node attempt records once: an activity retry, or a replayed recording,
// converges on the row already there instead of failing or writing a second.
// Another attempt, another loop iteration and another source are other rows.
func TestInsertNodeObservation_OneRowPerNodeAttempt(t *testing.T) {
	t.Parallel()
	env, ctx := storetest.New(t), context.Background()
	seedChat(t, env, "chat-obs")

	first := observation("o1", "chat-obs", "wf-obs", "run-1", "plan", 1)
	if got, err := store.InsertNodeObservation(ctx, env.ORM, first, 10); err != nil || got != core.NodeObservationStored {
		t.Fatalf("first write = %v, %v; want stored", got, err)
	}
	again := observation("o2", "chat-obs", "wf-obs", "run-1", "plan", 1)
	if got, err := store.InsertNodeObservation(ctx, env.ORM, again, 10); err != nil || got != core.NodeObservationDuplicate {
		t.Fatalf("same attempt again = %v, %v; want a duplicate", got, err)
	}
	retry := observation("o3", "chat-obs", "wf-obs", "run-1", "plan", 2)
	looped := observation("o4", "chat-obs", "wf-obs", "run-1", "plan", 1)
	loop, iteration := "sweep", int64(0)
	looped.LoopNodeID, looped.LoopIteration = &loop, &iteration
	model := observation("o5", "chat-obs", "wf-obs", "run-1", "plan", 1)
	model.Source = core.NodeObservationSourceModel
	for _, o := range []*core.NodeObservation{retry, looped, model} {
		if got, err := store.InsertNodeObservation(ctx, env.ORM, o, 10); err != nil || got != core.NodeObservationStored {
			t.Fatalf("%s = %v, %v; want stored", o.ID, got, err)
		}
	}
	if n := countObservations(t, env, `workflow_id = 'wf-obs'`); n != 4 {
		t.Fatalf("%d rows, want 4", n)
	}

	rows, err := store.ListWorkflowNodeObservations(ctx, env.ORM, "wf-obs")
	if err != nil {
		t.Fatal(err)
	}
	got := rows[0]
	if got.ID != "o1" || got.Attempt != 1 || got.Source != "workflow" || got.RunID != "run-1" ||
		got.LoopNodeID != nil || !got.CreatedAt.Equal(retentionAged) {
		t.Fatalf("row read back = %+v", got)
	}
	requireJSON(t, got.Attributes, `{"trigger":"schedule"}`)
	requireJSON(t, got.Events, `[{"name":"decision","attributes":{"value":"no_cut"}}]`)
}

// A run stores at most its cap of observations; a run is counted by its id,
// or by its workflow when the activity named no run.
func TestInsertNodeObservation_PerRunCap(t *testing.T) {
	t.Parallel()
	env, ctx := storetest.New(t), context.Background()
	seedChat(t, env, "chat-cap")
	for _, run := range []string{"run-cap", ""} {
		wf := "wf-cap-" + run
		for i := 0; i < 3; i++ {
			got, err := store.InsertNodeObservation(ctx, env.ORM, observation(fmt.Sprintf("%s-%d", wf, i), "chat-cap", wf, run, fmt.Sprintf("n%d", i), 1), 2)
			if err != nil {
				t.Fatal(err)
			}
			want := core.NodeObservationStored
			if i == 2 {
				want = core.NodeObservationCapped
			}
			if got != want {
				t.Fatalf("run %q write %d = %v, want %v", run, i, got, want)
			}
		}
		if n := countObservations(t, env, `workflow_id = $1`, wf); n != 2 {
			t.Fatalf("run %q holds %d rows, want its cap of 2", run, n)
		}
	}
}

// A chat DeleteUserProject purged refuses a late write, as every run-scoped
// table without a foreign key does (chat_write_admission).
func TestInsertNodeObservation_RefusedForAPurgedChat(t *testing.T) {
	t.Parallel()
	env, ctx := storetest.New(t), context.Background()
	mustExec(t, env, `INSERT INTO purged_chats (chat_id) VALUES ('chat-gone')`)
	if _, err := store.InsertNodeObservation(ctx, env.ORM, observation("o-gone", "chat-gone", "wf-gone", "", "plan", 1), 10); err == nil {
		t.Fatal("an observation of a purged chat was stored")
	}
}

// The observation prune deletes what the workflows of a root run's tree that
// themselves completed or were cancelled before the cutoff recorded, nothing
// of a workflow still running, failed or recently finished; it revisits a
// deferred root, and keeps its own cursor apart from the step prune's.
func TestPruneNodeObservations(t *testing.T) {
	t.Parallel()
	env, ctx := storetest.New(t), context.Background()
	seedChat(t, env, "chat-obs-prune")
	old, recent := retentionAged, retentionRecent
	runs := []stepRun{
		{"wf-o-done", nil, core.Completed(), &old},
		{"wf-o-done-child", ptr("wf-o-done"), core.Completed(), &old},
		{"wf-o-live-child", ptr("wf-o-done"), core.Active(), nil},
		{"wf-o-cancelled", nil, core.Cancelled(), &old},
		{"wf-o-failed", nil, core.Failed(), &old},
		{"wf-o-recent", nil, core.Completed(), &recent},
	}
	seedStepRuns(t, env, "chat-obs-prune", runs)
	for _, r := range runs {
		for i := 0; i < 3; i++ {
			o := observation(fmt.Sprintf("%s-o%d", r.id, i), "chat-obs-prune", r.id, "wf-o-root", fmt.Sprintf("n%d", i), 1)
			if _, err := store.InsertNodeObservation(ctx, env.ORM, o, 100); err != nil {
				t.Fatal(err)
			}
		}
	}

	res, err := env.Repo.PruneNodeObservations(ctx, retentionCutoff, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Runs != 2 || res.Deferred != 1 || res.Deleted != 9 {
		t.Fatalf("result = %+v, want 2 root runs (1 deferred) and 9 rows", res)
	}
	for _, r := range runs {
		want := 3
		switch r.id {
		case "wf-o-done", "wf-o-done-child", "wf-o-cancelled":
			want = 0
		}
		if n := countObservations(t, env, `workflow_id = $1`, r.id); n != want {
			t.Errorf("%s holds %d observations, want %d", r.id, n, want)
		}
	}
	if n := countObservations(t, env, `true`); n != 9 {
		t.Fatalf("%d observations left, want 9", n)
	}
	if _, id, err := store.ReadLedgerRetentionCursor(ctx, env.ORM, "node_observations"); err != nil || id != "wf-o-done" {
		t.Fatalf("observation cursor = %q, %v; want wf-o-done", id, err)
	}
	if _, id, err := store.ReadLedgerRetentionCursor(ctx, env.ORM, "step_executions"); err != nil || id != "" {
		t.Fatalf("step cursor = %q, %v; the observation prune must not move it", id, err)
	}
	if steps := retainedStepIDs(t, env); len(steps) != len(runs)*len(runSteps("x")) {
		t.Fatalf("the observation prune deleted step rows: %d left", len(steps))
	}

	// The live child finishes; only the deferral brings its root back.
	mustExec(t, env, `UPDATE workflows SET state = 3, stop_reason = 1, completed_at = $1 WHERE id = 'wf-o-live-child'`, old)
	if res, err = env.Repo.PruneNodeObservations(ctx, retentionCutoff, 0); err != nil || res.Deleted != 3 || res.Deferred != 0 {
		t.Fatalf("revisit = %+v, %v; want the live child's 3 rows and nothing deferred", res, err)
	}
	if res, err := env.Repo.PruneNodeObservations(ctx, retentionCutoff, 0); err != nil || res != (db.StepRetentionResult{}) {
		t.Fatalf("idle pass = %+v, %v; want nothing", res, err)
	}
}

func requireJSON(t *testing.T, got, want string) {
	t.Helper()
	var g, w interface{}
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("%s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("got %s, want %s", got, want)
	}
}
