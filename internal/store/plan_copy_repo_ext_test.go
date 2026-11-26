// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// TestCopyChatPlansToThread holds copyChatPlansToThreadSQL to what BranchChat
// copied when it copied row by row: every plan of every thread of the chat,
// fresh ids, statuses reset, the task tree mapped onto the copies (a parent in
// another plan dropped), project, metadata, assignee and completion left
// behind, and no other chat's plans.
func TestCopyChatPlansToThread(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	seedChatThread(t, env, "chat-src", "thread-src")
	if err := store.CreateThread(ctx, env.ORM, &store.Thread{ID: "thread-src-spawn", ChatID: "chat-src", Origin: "spawn", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	seedChatThread(t, env, "chat-other", "thread-other")
	seedChatThread(t, env, "chat-branch", "thread-branch")
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	done := int32(reliantv1.PlanStatus_PLAN_STATUS_COMPLETED)
	taskDone := int32(reliantv1.TaskStatus_TASK_STATUS_COMPLETED)

	plans := []*core.Plan{
		{ID: "p-old", ThreadID: "thread-src", Title: "old plan", Description: strp("first"), Status: done,
			Complexity: i32(int32(reliantv1.PlanComplexity_PLAN_COMPLEXITY_SIMPLE)), ProjectID: strp(storetest.SeedProjectID), CreatedAt: base},
		{ID: "p-new", ThreadID: "thread-src", Title: "new plan", Status: done, CreatedAt: base.Add(time.Minute)},
		{ID: "p-spawn", ThreadID: "thread-src-spawn", Title: "spawn plan", Status: done, CreatedAt: base.Add(30 * time.Second)},
		{ID: "p-other", ThreadID: "thread-other", Title: "other chat's plan", Status: done, CreatedAt: base},
	}
	for _, p := range plans {
		if err := store.InsertPlan(ctx, env.ORM, p); err != nil {
			t.Fatalf("InsertPlan(%s): %v", p.ID, err)
		}
	}
	completed := base.Add(2 * time.Minute)
	tasks := []*core.Task{
		{ID: "t-root", PlanID: "p-old", Title: "root", Description: strp("the root"), Status: taskDone, Position: 0,
			Metadata: strp(`{"k":"v"}`), Assignee: strp("agent-1"), CompletedAt: &completed},
		{ID: "t-child", PlanID: "p-old", ParentTaskID: strp("t-root"), Title: "child", Status: taskDone, Position: 1},
		{ID: "t-grandchild", PlanID: "p-old", ParentTaskID: strp("t-child"), Title: "grandchild", Status: taskDone, Position: 2},
		{ID: "t-cross", PlanID: "p-new", ParentTaskID: strp("t-root"), Title: "parent in another plan", Status: taskDone, Position: 0},
		{ID: "t-spawn", PlanID: "p-spawn", Title: "spawn task", Status: taskDone, Position: 5},
		{ID: "t-other", PlanID: "p-other", Title: "other chat's task", Status: taskDone, Position: 0},
	}
	for _, task := range tasks {
		if err := store.InsertTask(ctx, env.ORM, task); err != nil {
			t.Fatalf("InsertTask(%s): %v", task.ID, err)
		}
	}

	pending := int32(reliantv1.PlanStatus_PLAN_STATUS_PENDING)
	taskPending := int32(reliantv1.TaskStatus_TASK_STATUS_PENDING)
	nPlans, nTasks, err := store.CopyChatPlansToThread(ctx, env.ORM, "chat-src", "thread-branch", pending, taskPending)
	if err != nil {
		t.Fatalf("CopyChatPlansToThread: %v", err)
	}
	if nPlans != 3 || nTasks != 5 {
		t.Fatalf("copied %d plans, %d tasks; want 3, 5", nPlans, nTasks)
	}

	copies, err := store.ListPlansByThread(ctx, env.ORM, "thread-branch")
	if err != nil {
		t.Fatalf("ListPlansByThread: %v", err)
	}
	// Newest first, in the source's order: the newest source plan's copy is
	// the thread's newest plan.
	var titles []string
	byTitle := map[string]*core.Plan{}
	for _, p := range copies {
		titles = append(titles, p.Title)
		byTitle[p.Title] = p
		for _, src := range plans {
			if p.ID == src.ID {
				t.Fatalf("copy %q kept its source's id", p.Title)
			}
		}
		if p.Status != pending || p.ProjectID != nil || p.CompletedAt != nil {
			t.Fatalf("copy %q = {status %d, project %v, completed %v}, want pending with no project or completion", p.Title, p.Status, p.ProjectID, p.CompletedAt)
		}
	}
	if want := []string{"new plan", "spawn plan", "old plan"}; len(titles) != len(want) || titles[0] != want[0] || titles[1] != want[1] || titles[2] != want[2] {
		t.Fatalf("copied plans newest first = %v, want %v", titles, want)
	}
	old := byTitle["old plan"]
	if old.Description == nil || *old.Description != "first" || old.Complexity == nil || *old.Complexity != *plans[0].Complexity {
		t.Fatalf("old plan's copy = %+v, want its description and complexity", old)
	}

	copiedTasks := map[string]*core.Task{}
	for _, p := range copies {
		list, err := store.ListTasksByPlan(ctx, env.ORM, p.ID)
		if err != nil {
			t.Fatalf("ListTasksByPlan: %v", err)
		}
		for _, task := range list {
			copiedTasks[task.Title] = task
		}
	}
	if len(copiedTasks) != 5 {
		t.Fatalf("copied tasks = %d, want 5", len(copiedTasks))
	}
	root, child, grandchild := copiedTasks["root"], copiedTasks["child"], copiedTasks["grandchild"]
	if root.PlanID != old.ID || root.ParentTaskID != nil || root.ID == "t-root" {
		t.Fatalf("root's copy = %+v, want a new task of the old plan's copy with no parent", root)
	}
	if child.ParentTaskID == nil || *child.ParentTaskID != root.ID || grandchild.ParentTaskID == nil || *grandchild.ParentTaskID != child.ID {
		t.Fatalf("task tree not mapped onto the copies: child.parent=%v grandchild.parent=%v", child.ParentTaskID, grandchild.ParentTaskID)
	}
	if cross := copiedTasks["parent in another plan"]; cross.ParentTaskID != nil || cross.PlanID != byTitle["new plan"].ID {
		t.Fatalf("a parent in another plan = %+v, want dropped", cross)
	}
	if spawn := copiedTasks["spawn task"]; spawn.PlanID != byTitle["spawn plan"].ID || spawn.Position != 5 {
		t.Fatalf("spawn task's copy = %+v, want position 5 on the spawn plan's copy", spawn)
	}
	if root.Status != taskPending || root.Description == nil || *root.Description != "the root" ||
		root.Metadata != nil || root.Assignee != nil || root.CompletedAt != nil || root.Position != 0 {
		t.Fatalf("root's copy = %+v, want pending, its description and position, no metadata/assignee/completion", root)
	}

	// The source is untouched, and another chat's plans are not copied.
	for _, src := range []string{"thread-src", "thread-other"} {
		list, err := store.ListPlansByThread(ctx, env.ORM, src)
		if err != nil {
			t.Fatalf("ListPlansByThread(%s): %v", src, err)
		}
		for _, p := range list {
			if p.Status != done {
				t.Fatalf("source plan %s changed: status %d", p.ID, p.Status)
			}
		}
	}

	// A chat with no plans copies nothing.
	seedChatThread(t, env, "chat-empty", "thread-empty")
	nPlans, nTasks, err = store.CopyChatPlansToThread(ctx, env.ORM, "chat-empty", "thread-other", pending, taskPending)
	if err != nil || nPlans != 0 || nTasks != 0 {
		t.Fatalf("copy of a chat with no plans = (%d, %d, %v), want (0, 0, nil)", nPlans, nTasks, err)
	}
}
