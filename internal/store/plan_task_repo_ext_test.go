// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// seedChatThread writes a chat and one thread of it through the generated
// stores, for the plan queries that reach a chat through threads.
func seedChatThread(t *testing.T, env *storetest.Env, chatID, threadID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.CreateChat(ctx, env.ORM, &store.Chat{
		ID: chatID, Title: chatID, ProjectID: storetest.SeedProjectID, UserID: storetest.SeedUserID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	if err := store.CreateThread(ctx, env.ORM, &store.Thread{ID: threadID, ChatID: chatID, Origin: "main", CreatedAt: now}); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
}

func i32(v int32) *int32 { return &v }

func strp(s string) *string { return &s }

// sameInstant compares a time read back from Postgres (microseconds) with
// one stamped in Go (nanoseconds).
func sameInstant(a, b time.Time) bool {
	d := a.Sub(b)
	return d > -time.Microsecond && d < time.Microsecond
}

// TestPlanQueries ports plans.sql: every plan read and write through the
// store, interleaved with the Repository on the same database.
func TestPlanQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedChatThread(t, env, "chat-plans", "thread-plans")
	seedChatThread(t, env, "chat-other", "thread-other")
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)

	// Written by the store, read by the Repository.
	older := &core.Plan{
		ID: "plan-older", ThreadID: "thread-plans", Title: "older", Description: strp("first"),
		Status: int32(reliantv1.PlanStatus_PLAN_STATUS_PENDING), Complexity: i32(int32(reliantv1.PlanComplexity_PLAN_COMPLEXITY_SIMPLE)),
		ProjectID: strp(storetest.SeedProjectID), CreatedAt: base,
	}
	if err := store.InsertPlan(ctx, env.ORM, older); err != nil {
		t.Fatalf("InsertPlan: %v", err)
	}
	if older.UpdatedAt.IsZero() || !older.CreatedAt.Equal(base) {
		t.Fatalf("InsertPlan stamps = {Created %v, Updated %v}, want {%v, set}", older.CreatedAt, older.UpdatedAt, base)
	}
	twin, err := env.Repo.GetPlan(ctx, "plan-older")
	if err != nil {
		t.Fatalf("Repo.GetPlan: %v", err)
	}
	if twin.Title != "older" || *twin.Description != "first" || twin.Status != older.Status || *twin.Complexity != *older.Complexity || *twin.ProjectID != storetest.SeedProjectID {
		t.Fatalf("Repo.GetPlan = %+v, want the store's write", twin)
	}

	// Written by the Repository, read by the store. The zero status is
	// written as is, never the column DEFAULT (1).
	newer := &core.Plan{ID: "plan-newer", ThreadID: "thread-plans", Title: "newer", Status: 0, CreatedAt: base.Add(time.Minute), UpdatedAt: base.Add(time.Minute)}
	if err := env.Repo.CreatePlan(ctx, newer); err != nil {
		t.Fatalf("Repo.CreatePlan: %v", err)
	}
	got, err := store.GetPlan(ctx, env.ORM, "plan-newer")
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Title != "newer" || got.Status != 0 || got.Complexity != nil || got.ProjectID != nil || got.Description != nil {
		t.Fatalf("GetPlan = %+v, want the Repository's write with status 0 and nil optionals", got)
	}
	if _, err := store.GetPlan(ctx, env.ORM, "plan-missing"); err == nil || err.Error() != "plan not found: plan-missing" {
		t.Fatalf("GetPlan(missing) err = %v, want \"plan not found: plan-missing\"", err)
	}
	other := &core.Plan{ID: "plan-other", ThreadID: "thread-other", Title: "other", Status: 1, CreatedAt: base}
	if err := store.InsertPlan(ctx, env.ORM, other); err != nil {
		t.Fatalf("InsertPlan(other): %v", err)
	}

	wantIDs := func(t *testing.T, what string, plans []*core.Plan, want ...string) {
		t.Helper()
		if len(plans) != len(want) {
			t.Fatalf("%s = %d plans, want %v", what, len(plans), want)
		}
		for i, p := range plans {
			if p.ID != want[i] {
				t.Fatalf("%s[%d] = %s, want %v", what, i, p.ID, want)
			}
		}
	}

	byThread, err := store.ListPlansByThread(ctx, env.ORM, "thread-plans")
	if err != nil {
		t.Fatalf("ListPlansByThread: %v", err)
	}
	wantIDs(t, "ListPlansByThread", byThread, "plan-newer", "plan-older")
	newest, err := store.GetPlanByThreadID(ctx, env.ORM, "thread-plans")
	if err != nil || newest.ID != "plan-newer" {
		t.Fatalf("GetPlanByThreadID = %v, %v; want plan-newer", newest, err)
	}
	if _, err := store.GetPlanByThreadID(ctx, env.ORM, "thread-none"); !errors.Is(err, core.ErrPlanNotFound) || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetPlanByThreadID(none) err = %v, want core.ErrPlanNotFound (sql.ErrNoRows)", err)
	}
	byChat, err := store.ListPlansByChatID(ctx, env.ORM, "chat-plans")
	if err != nil {
		t.Fatalf("ListPlansByChatID: %v", err)
	}
	wantIDs(t, "ListPlansByChatID", byChat, "plan-newer", "plan-older")
	byProject, err := store.ListPlansByProject(ctx, env.ORM, storetest.SeedProjectID)
	if err != nil {
		t.Fatalf("ListPlansByProject: %v", err)
	}
	wantIDs(t, "ListPlansByProject", byProject, "plan-older")

	// The sqlc twins list the same plans in the same order.
	twinThread, err := env.Repo.ListPlansByThread(ctx, "thread-plans")
	if err != nil {
		t.Fatalf("Repo.ListPlansByThread: %v", err)
	}
	wantIDs(t, "Repo.ListPlansByThread", twinThread, "plan-newer", "plan-older")
	twinChat, err := env.Repo.ListPlansByChatID(ctx, "chat-plans")
	if err != nil {
		t.Fatalf("Repo.ListPlansByChatID: %v", err)
	}
	wantIDs(t, "Repo.ListPlansByChatID", twinChat, "plan-newer", "plan-older")

	// ReplacePlan rewrites only the mutable columns.
	done := base.Add(2 * time.Hour)
	replace := &core.Plan{ID: "plan-older", ThreadID: "ignored", Title: "older v2", Status: int32(reliantv1.PlanStatus_PLAN_STATUS_COMPLETED), CompletedAt: &done}
	if err := store.ReplacePlan(ctx, env.ORM, replace); err != nil {
		t.Fatalf("ReplacePlan: %v", err)
	}
	twin, err = env.Repo.GetPlan(ctx, "plan-older")
	if err != nil {
		t.Fatalf("Repo.GetPlan after ReplacePlan: %v", err)
	}
	if twin.Title != "older v2" || twin.Description != nil || twin.Complexity != nil || twin.ThreadID != "thread-plans" ||
		*twin.ProjectID != storetest.SeedProjectID || !twin.CompletedAt.Equal(done) || !sameInstant(twin.UpdatedAt, replace.UpdatedAt) || !twin.CreatedAt.Equal(base) {
		t.Fatalf("after ReplacePlan Repo.GetPlan = %+v, want title/description/status/complexity/completed_at replaced, thread/project/created_at kept", twin)
	}
	if err := store.ReplacePlan(ctx, env.ORM, &core.Plan{ID: "plan-missing", Title: "x"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("ReplacePlan(missing) err = %v, want sql.ErrNoRows", err)
	}

	if err := store.UpdatePlanStatus(ctx, env.ORM, "plan-newer", int32(reliantv1.PlanStatus_PLAN_STATUS_IN_PROGRESS), nil); err != nil {
		t.Fatalf("UpdatePlanStatus: %v", err)
	}
	twin, err = env.Repo.GetPlan(ctx, "plan-newer")
	if err != nil {
		t.Fatalf("Repo.GetPlan after UpdatePlanStatus: %v", err)
	}
	if twin.Status != int32(reliantv1.PlanStatus_PLAN_STATUS_IN_PROGRESS) || twin.Title != "newer" {
		t.Fatalf("after UpdatePlanStatus Repo.GetPlan = %+v, want in-progress and the title kept", twin)
	}

	if err := store.RemovePlan(ctx, env.ORM, "plan-newer"); err != nil {
		t.Fatalf("RemovePlan: %v", err)
	}
	if err := store.RemovePlan(ctx, env.ORM, "plan-newer"); err != nil {
		t.Fatalf("RemovePlan(again) = %v, want nil: a missing plan is not an error", err)
	}
	if _, err := env.Repo.GetPlan(ctx, "plan-newer"); err == nil {
		t.Fatalf("Repo.GetPlan after RemovePlan: want not found")
	}
}

// TestTaskQueries ports tasks.sql, interleaved with the Repository.
func TestTaskQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedChatThread(t, env, "chat-tasks", "thread-tasks")
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	if err := store.InsertPlan(ctx, env.ORM, &core.Plan{ID: "plan-t", ThreadID: "thread-tasks", Title: "p", Status: 1, CreatedAt: base}); err != nil {
		t.Fatalf("InsertPlan: %v", err)
	}

	second := &core.Task{
		ID: "task-2", PlanID: "plan-t", Title: "second", Description: strp("d"), Status: int32(reliantv1.TaskStatus_TASK_STATUS_PENDING),
		Position: 1, Metadata: strp(`{"priority":"high"}`), Assignee: strp("agent"), CreatedAt: base,
	}
	if err := store.InsertTask(ctx, env.ORM, second); err != nil {
		t.Fatalf("InsertTask: %v", err)
	}
	first := &core.Task{ID: "task-1", PlanID: "plan-t", ParentTaskID: strp("task-2"), Title: "first", Status: 0, Position: 0, CreatedAt: base, UpdatedAt: base}
	if err := env.Repo.CreateTask(ctx, first); err != nil {
		t.Fatalf("Repo.CreateTask: %v", err)
	}

	got, err := store.GetTask(ctx, env.ORM, "task-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Title != "first" || got.Status != 0 || *got.ParentTaskID != "task-2" || got.Metadata != nil {
		t.Fatalf("GetTask = %+v, want the Repository's write with status 0", got)
	}
	twin, err := env.Repo.GetTask(ctx, "task-2")
	if err != nil {
		t.Fatalf("Repo.GetTask: %v", err)
	}
	if twin.Title != "second" || twin.Position != 1 || *twin.Metadata != `{"priority":"high"}` || *twin.Assignee != "agent" || *twin.Description != "d" {
		t.Fatalf("Repo.GetTask = %+v, want the store's write", twin)
	}
	if _, err := store.GetTask(ctx, env.ORM, "task-missing"); err == nil || err.Error() != "task not found: task-missing" {
		t.Fatalf("GetTask(missing) err = %v, want \"task not found: task-missing\"", err)
	}

	listed, err := store.ListTasksByPlan(ctx, env.ORM, "plan-t")
	if err != nil {
		t.Fatalf("ListTasksByPlan: %v", err)
	}
	twinListed, err := env.Repo.ListTasksByPlan(ctx, "plan-t")
	if err != nil {
		t.Fatalf("Repo.ListTasksByPlan: %v", err)
	}
	if len(listed) != 2 || listed[0].ID != "task-1" || listed[1].ID != "task-2" || len(twinListed) != 2 || twinListed[0].ID != "task-1" {
		t.Fatalf("ListTasksByPlan = %v / twin %v, want [task-1 task-2] by position", listed, twinListed)
	}

	done := base.Add(time.Hour)
	replace := &core.Task{ID: "task-2", PlanID: "ignored", Title: "second v2", Status: int32(reliantv1.TaskStatus_TASK_STATUS_COMPLETED), Position: 5, CompletedAt: &done}
	if err := store.ReplaceTask(ctx, env.ORM, replace); err != nil {
		t.Fatalf("ReplaceTask: %v", err)
	}
	twin, err = env.Repo.GetTask(ctx, "task-2")
	if err != nil {
		t.Fatalf("Repo.GetTask after ReplaceTask: %v", err)
	}
	if twin.Title != "second v2" || twin.Position != 5 || twin.Metadata != nil || twin.Assignee != nil || twin.Description != nil ||
		twin.PlanID != "plan-t" || !twin.CompletedAt.Equal(done) || !sameInstant(twin.UpdatedAt, replace.UpdatedAt) {
		t.Fatalf("after ReplaceTask Repo.GetTask = %+v, want the mutable columns replaced and plan_id kept", twin)
	}

	if err := store.UpdateTaskStatus(ctx, env.ORM, "task-1", int32(reliantv1.TaskStatus_TASK_STATUS_BLOCKED), nil); err != nil {
		t.Fatalf("UpdateTaskStatus: %v", err)
	}
	twin, err = env.Repo.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("Repo.GetTask after UpdateTaskStatus: %v", err)
	}
	if twin.Status != int32(reliantv1.TaskStatus_TASK_STATUS_BLOCKED) || twin.Title != "first" || *twin.ParentTaskID != "task-2" {
		t.Fatalf("after UpdateTaskStatus Repo.GetTask = %+v, want blocked with the rest kept", twin)
	}
	if err := store.UpdateTaskStatus(ctx, env.ORM, "task-missing", 1, nil); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("UpdateTaskStatus(missing) err = %v, want sql.ErrNoRows", err)
	}

	if err := store.RemoveTask(ctx, env.ORM, "task-1"); err != nil {
		t.Fatalf("RemoveTask: %v", err)
	}
	if err := store.RemoveTask(ctx, env.ORM, "task-1"); err != nil {
		t.Fatalf("RemoveTask(again) = %v, want nil", err)
	}
	if _, err := env.Repo.GetTask(ctx, "task-1"); err == nil {
		t.Fatalf("Repo.GetTask after RemoveTask: want not found")
	}
}

// TestTaskDependencyQueries ports task_dependencies.sql, interleaved with the
// Repository.
func TestTaskDependencyQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedChatThread(t, env, "chat-deps", "thread-deps")
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	for _, p := range []string{"plan-a", "plan-b"} {
		if err := store.InsertPlan(ctx, env.ORM, &core.Plan{ID: p, ThreadID: "thread-deps", Title: p, Status: 1, CreatedAt: base}); err != nil {
			t.Fatalf("InsertPlan(%s): %v", p, err)
		}
	}
	for i, id := range []string{"a1", "a2", "a3"} {
		if err := store.InsertTask(ctx, env.ORM, &core.Task{ID: id, PlanID: "plan-a", Title: id, Status: 1, Position: i}); err != nil {
			t.Fatalf("InsertTask(%s): %v", id, err)
		}
	}
	if err := store.InsertTask(ctx, env.ORM, &core.Task{ID: "b1", PlanID: "plan-b", Title: "b1", Status: 1}); err != nil {
		t.Fatalf("InsertTask(b1): %v", err)
	}

	blocks := int32(reliantv1.DependencyType_DEPENDENCY_TYPE_BLOCKS)
	related := int32(reliantv1.DependencyType_DEPENDENCY_TYPE_RELATED)
	// a1 -blocks-> a2 (store, explicit created_at), a3 -related-> a2
	// (Repository), a2 -blocks-> b1 (store, created_at left to its DEFAULT).
	d1 := &core.TaskDependency{ID: "d1", FromTaskID: "a1", ToTaskID: "a2", DependencyType: blocks, CreatedAt: base}
	if err := store.InsertTaskDependency(ctx, env.ORM, d1); err != nil {
		t.Fatalf("InsertTaskDependency: %v", err)
	}
	if err := env.Repo.CreateTaskDependency(ctx, &core.TaskDependency{ID: "d2", FromTaskID: "a3", ToTaskID: "a2", DependencyType: related, CreatedAt: base.Add(time.Minute)}); err != nil {
		t.Fatalf("Repo.CreateTaskDependency: %v", err)
	}
	d3 := &core.TaskDependency{ID: "d3", FromTaskID: "a2", ToTaskID: "b1", DependencyType: blocks}
	if err := store.InsertTaskDependency(ctx, env.ORM, d3); err != nil {
		t.Fatalf("InsertTaskDependency(default created_at): %v", err)
	}
	if !d3.CreatedAt.After(base.Add(time.Minute)) {
		t.Fatalf("InsertTaskDependency created_at = %v, want the column DEFAULT now()", d3.CreatedAt)
	}
	if err := store.InsertTaskDependency(ctx, env.ORM, &core.TaskDependency{ID: "d-dup", FromTaskID: "a1", ToTaskID: "a2", DependencyType: blocks}); err == nil {
		t.Fatalf("InsertTaskDependency(duplicate edge): want the unique violation")
	}

	got, err := store.GetTaskDependency(ctx, env.ORM, "d2")
	if err != nil || got.FromTaskID != "a3" || got.DependencyType != related {
		t.Fatalf("GetTaskDependency(d2) = %+v, %v; want the Repository's a3->a2 related edge", got, err)
	}
	twin, err := env.Repo.GetTaskDependency(ctx, "d1")
	if err != nil || twin.ToTaskID != "a2" || !twin.CreatedAt.Equal(base) {
		t.Fatalf("Repo.GetTaskDependency(d1) = %+v, %v; want the store's edge", twin, err)
	}
	if _, err := store.GetTaskDependency(ctx, env.ORM, "nope"); err == nil || err.Error() != "task dependency not found: nope" {
		t.Fatalf("GetTaskDependency(missing) err = %v", err)
	}

	ids := func(deps []*core.TaskDependency) []string {
		out := make([]string, len(deps))
		for i, d := range deps {
			out[i] = d.ID
		}
		return out
	}
	same := func(t *testing.T, what string, got, twin []*core.TaskDependency, want ...string) {
		t.Helper()
		g, w := ids(got), ids(twin)
		if len(g) != len(want) || len(w) != len(want) {
			t.Fatalf("%s = %v, twin %v, want %v", what, g, w, want)
		}
		for i := range want {
			if g[i] != want[i] || w[i] != want[i] {
				t.Fatalf("%s = %v, twin %v, want %v", what, g, w, want)
			}
		}
	}

	byTask, err := store.ListTaskDependenciesByTask(ctx, env.ORM, "a2")
	if err != nil {
		t.Fatalf("ListTaskDependenciesByTask: %v", err)
	}
	twinByTask, err := env.Repo.ListTaskDependenciesByTask(ctx, "a2")
	if err != nil {
		t.Fatalf("Repo.ListTaskDependenciesByTask: %v", err)
	}
	same(t, "ListTaskDependenciesByTask(a2)", byTask, twinByTask, "d1", "d2", "d3")

	blockers, err := store.ListBlockersForTask(ctx, env.ORM, "a2")
	if err != nil {
		t.Fatalf("ListBlockersForTask: %v", err)
	}
	twinBlockers, err := env.Repo.ListBlockersForTask(ctx, "a2")
	if err != nil {
		t.Fatalf("Repo.ListBlockersForTask: %v", err)
	}
	same(t, "ListBlockersForTask(a2)", blockers, twinBlockers, "d1")

	byPlan, err := store.ListDependenciesByPlan(ctx, env.ORM, "plan-a")
	if err != nil {
		t.Fatalf("ListDependenciesByPlan: %v", err)
	}
	twinByPlan, err := env.Repo.ListDependenciesByPlan(ctx, "plan-a")
	if err != nil {
		t.Fatalf("Repo.ListDependenciesByPlan: %v", err)
	}
	same(t, "ListDependenciesByPlan(plan-a)", byPlan, twinByPlan, "d1", "d2", "d3")

	// By pair: only the matching type goes.
	if err := store.DeleteTaskDependencyByPair(ctx, env.ORM, "a3", "a2", blocks); err != nil {
		t.Fatalf("DeleteTaskDependencyByPair(wrong type): %v", err)
	}
	if _, err := env.Repo.GetTaskDependency(ctx, "d2"); err != nil {
		t.Fatalf("d2 gone after a by-pair delete of another type: %v", err)
	}
	if err := store.DeleteTaskDependencyByPair(ctx, env.ORM, "a3", "a2", related); err != nil {
		t.Fatalf("DeleteTaskDependencyByPair: %v", err)
	}
	if _, err := env.Repo.GetTaskDependency(ctx, "d2"); err == nil {
		t.Fatalf("d2 still there after DeleteTaskDependencyByPair")
	}

	if err := store.RemoveTaskDependency(ctx, env.ORM, "d3"); err != nil {
		t.Fatalf("RemoveTaskDependency: %v", err)
	}
	if err := store.RemoveTaskDependency(ctx, env.ORM, "d3"); err != nil {
		t.Fatalf("RemoveTaskDependency(again) = %v, want nil", err)
	}
	// Deleting a task cascades to its edges.
	if err := store.RemoveTask(ctx, env.ORM, "a1"); err != nil {
		t.Fatalf("RemoveTask(a1): %v", err)
	}
	left, err := store.ListTaskDependenciesByTask(ctx, env.ORM, "a2")
	if err != nil || len(left) != 0 {
		t.Fatalf("edges of a2 after deleting d2, d3 and task a1 = %v, %v; want none", ids(left), err)
	}
}
