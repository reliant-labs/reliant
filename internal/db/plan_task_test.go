// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db/core"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

// createTestPlan inserts a plan into the test DB and returns it.
func createTestPlan(t *testing.T, repo *Repo, chatID string) *Plan {
	t.Helper()
	plan := &Plan{
		ID:        uuid.New().String(),
		ThreadID:  chatID,
		Title:     "Test Plan",
		Status:    int32(reliantv1.PlanStatus_PLAN_STATUS_PENDING),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := repo.CreatePlan(context.Background(), plan); err != nil {
		t.Fatalf("failed to create plan: %v", err)
	}
	return plan
}

// createTestChat inserts a chat into the test DB and returns its ID.
func createTestChat(t *testing.T, repo *Repo) string {
	t.Helper()
	chatID := uuid.New().String()
	chat := &Chat{
		ID:         chatID,
		Title:      "Test Chat",
		ProjectID:  "test-project",
		UserID:     "test-user",
		State:      ChatStateIdle,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		LastActive: time.Now(),
	}
	if err := repo.CreateChat(context.Background(), chat); err != nil {
		t.Fatalf("failed to create chat: %v", err)
	}
	return chatID
}

func TestPlanCRUD(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := createTestChat(t, repo)

	t.Run("create and get plan", func(t *testing.T) {
		plan := createTestPlan(t, repo, chatID)

		got, err := repo.GetPlan(ctx, plan.ID)
		if err != nil {
			t.Fatalf("GetPlan: %v", err)
		}
		if got.Title != "Test Plan" {
			t.Errorf("expected title 'Test Plan', got %q", got.Title)
		}
		if got.Status != int32(reliantv1.PlanStatus_PLAN_STATUS_PENDING) {
			t.Errorf("expected status 'pending', got %q", got.Status)
		}
	})

	t.Run("create plan with complexity", func(t *testing.T) {
		complexity := int32(reliantv1.PlanComplexity_PLAN_COMPLEXITY_COMPLEX)
		plan := &Plan{
			ID:         uuid.New().String(),
			ThreadID:   chatID,
			Title:      "Complex Plan",
			Status:     int32(reliantv1.PlanStatus_PLAN_STATUS_PENDING),
			Complexity: &complexity,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}
		if err := repo.CreatePlan(ctx, plan); err != nil {
			t.Fatalf("CreatePlan: %v", err)
		}

		got, err := repo.GetPlan(ctx, plan.ID)
		if err != nil {
			t.Fatalf("GetPlan: %v", err)
		}
		if got.Complexity == nil || *got.Complexity != int32(reliantv1.PlanComplexity_PLAN_COMPLEXITY_COMPLEX) {
			t.Errorf("expected complexity complex, got %v", got.Complexity)
		}
	})

	t.Run("create plan with project_id", func(t *testing.T) {
		projectID := "test-project"
		plan := &Plan{
			ID:        uuid.New().String(),
			ThreadID:  chatID,
			Title:     "Project Plan",
			Status:    int32(reliantv1.TaskStatus_TASK_STATUS_PENDING),
			ProjectID: &projectID,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := repo.CreatePlan(ctx, plan); err != nil {
			t.Fatalf("CreatePlan: %v", err)
		}

		got, err := repo.GetPlan(ctx, plan.ID)
		if err != nil {
			t.Fatalf("GetPlan: %v", err)
		}
		if got.ProjectID == nil || *got.ProjectID != "test-project" {
			t.Errorf("expected project_id 'test-project', got %v", got.ProjectID)
		}

		// Test ListPlansByProject
		plans, err := repo.ListPlansByProject(ctx, "test-project")
		if err != nil {
			t.Fatalf("ListPlansByProject: %v", err)
		}
		found := false
		for _, p := range plans {
			if p.ID == plan.ID {
				found = true
			}
		}
		if !found {
			t.Error("expected plan in ListPlansByProject results")
		}
	})

	t.Run("update plan preserves fields", func(t *testing.T) {
		complexity := int32(reliantv1.PlanComplexity_PLAN_COMPLEXITY_MODERATE)
		plan := &Plan{
			ID:         uuid.New().String(),
			ThreadID:   chatID,
			Title:      "Original Title",
			Status:     int32(reliantv1.PlanStatus_PLAN_STATUS_PENDING),
			Complexity: &complexity,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}
		if err := repo.CreatePlan(ctx, plan); err != nil {
			t.Fatalf("CreatePlan: %v", err)
		}

		// Update via UpdatePlan
		desc := "Updated description"
		plan.Title = "Updated Title"
		plan.Description = &desc
		if err := repo.UpdatePlan(ctx, plan); err != nil {
			t.Fatalf("UpdatePlan: %v", err)
		}

		got, err := repo.GetPlan(ctx, plan.ID)
		if err != nil {
			t.Fatalf("GetPlan: %v", err)
		}
		if got.Title != "Updated Title" {
			t.Errorf("expected title 'Updated Title', got %q", got.Title)
		}
		if got.Description == nil || *got.Description != "Updated description" {
			t.Errorf("expected description 'Updated description', got %v", got.Description)
		}
		if got.Complexity == nil || *got.Complexity != int32(reliantv1.PlanComplexity_PLAN_COMPLEXITY_MODERATE) {
			t.Errorf("expected complexity 'moderate' preserved, got %v", got.Complexity)
		}
	})

	t.Run("update plan status preserves other fields", func(t *testing.T) {
		complexity := int32(reliantv1.PlanComplexity_PLAN_COMPLEXITY_SIMPLE)
		desc := "My description"
		plan := &Plan{
			ID:          uuid.New().String(),
			ThreadID:    chatID,
			Title:       "Status Test",
			Description: &desc,
			Status:      int32(reliantv1.PlanStatus_PLAN_STATUS_PENDING),
			Complexity:  &complexity,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}
		if err := repo.CreatePlan(ctx, plan); err != nil {
			t.Fatalf("CreatePlan: %v", err)
		}

		now := time.Now()
		if err := repo.UpdatePlanStatus(ctx, plan.ID, int32(reliantv1.PlanStatus_PLAN_STATUS_COMPLETED), &now); err != nil {
			t.Fatalf("UpdatePlanStatus: %v", err)
		}

		got, err := repo.GetPlan(ctx, plan.ID)
		if err != nil {
			t.Fatalf("GetPlan: %v", err)
		}
		if got.Status != int32(reliantv1.PlanStatus_PLAN_STATUS_COMPLETED) {
			t.Errorf("expected status completed, got %d", got.Status)
		}
		if got.Title != "Status Test" {
			t.Errorf("expected title preserved, got %q", got.Title)
		}
		if got.Description == nil || *got.Description != "My description" {
			t.Errorf("expected description preserved, got %v", got.Description)
		}
		if got.Complexity == nil || *got.Complexity != int32(reliantv1.PlanComplexity_PLAN_COMPLEXITY_SIMPLE) {
			t.Errorf("expected complexity preserved, got %v", got.Complexity)
		}
		if got.CompletedAt == nil {
			t.Error("expected completed_at to be set")
		}
	})

	t.Run("list plans by thread", func(t *testing.T) {
		newChatID := createTestChat(t, repo)
		createTestPlan(t, repo, newChatID)
		createTestPlan(t, repo, newChatID)

		plans, err := repo.ListPlansByThread(ctx, newChatID)
		if err != nil {
			t.Fatalf("ListPlansByThread: %v", err)
		}
		if len(plans) != 2 {
			t.Errorf("expected 2 plans, got %d", len(plans))
		}
	})

	t.Run("delete plan", func(t *testing.T) {
		plan := createTestPlan(t, repo, chatID)

		if err := repo.DeletePlan(ctx, plan.ID); err != nil {
			t.Fatalf("DeletePlan: %v", err)
		}

		_, err := repo.GetPlan(ctx, plan.ID)
		if err == nil {
			t.Error("expected error when getting deleted plan")
		}
	})
}

func TestTaskCRUD(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := createTestChat(t, repo)
	plan := createTestPlan(t, repo, chatID)

	t.Run("create and get task", func(t *testing.T) {
		task := &Task{
			ID:        uuid.New().String(),
			PlanID:    plan.ID,
			Title:     "Task 1",
			Status:    int32(reliantv1.TaskStatus_TASK_STATUS_PENDING),
			Position:  0,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := repo.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}

		got, err := repo.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if got.Title != "Task 1" {
			t.Errorf("expected title 'Task 1', got %q", got.Title)
		}
	})

	t.Run("create task with metadata and assignee", func(t *testing.T) {
		metadata := `{"preferred_agent":"researcher","priority":"high"}`
		assignee := "agent-1"
		task := &Task{
			ID:        uuid.New().String(),
			PlanID:    plan.ID,
			Title:     "Task with metadata",
			Status:    int32(reliantv1.TaskStatus_TASK_STATUS_PENDING),
			Position:  1,
			Metadata:  &metadata,
			Assignee:  &assignee,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := repo.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}

		got, err := repo.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if got.Metadata == nil || *got.Metadata != metadata {
			t.Errorf("expected metadata %q, got %v", metadata, got.Metadata)
		}
		if got.Assignee == nil || *got.Assignee != "agent-1" {
			t.Errorf("expected assignee 'agent-1', got %v", got.Assignee)
		}
	})

	t.Run("create task with parent", func(t *testing.T) {
		parent := &Task{
			ID:        uuid.New().String(),
			PlanID:    plan.ID,
			Title:     "Parent Task",
			Status:    int32(reliantv1.TaskStatus_TASK_STATUS_PENDING),
			Position:  2,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := repo.CreateTask(ctx, parent); err != nil {
			t.Fatalf("CreateTask parent: %v", err)
		}

		child := &Task{
			ID:           uuid.New().String(),
			PlanID:       plan.ID,
			ParentTaskID: &parent.ID,
			Title:        "Child Task",
			Status:       int32(reliantv1.TaskStatus_TASK_STATUS_PENDING),
			Position:     0,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}
		if err := repo.CreateTask(ctx, child); err != nil {
			t.Fatalf("CreateTask child: %v", err)
		}

		got, err := repo.GetTask(ctx, child.ID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if got.ParentTaskID == nil || *got.ParentTaskID != parent.ID {
			t.Errorf("expected parent_task_id %q, got %v", parent.ID, got.ParentTaskID)
		}
	})

	t.Run("update task preserves fields", func(t *testing.T) {
		metadata := `{"notes":"important"}`
		assignee := "agent-2"
		task := &Task{
			ID:        uuid.New().String(),
			PlanID:    plan.ID,
			Title:     "Original",
			Status:    int32(reliantv1.TaskStatus_TASK_STATUS_PENDING),
			Position:  3,
			Metadata:  &metadata,
			Assignee:  &assignee,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := repo.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}

		desc := "Updated desc"
		task.Title = "Updated"
		task.Description = &desc
		task.Status = int32(reliantv1.TaskStatus_TASK_STATUS_IN_PROGRESS)
		if err := repo.UpdateTask(ctx, task); err != nil {
			t.Fatalf("UpdateTask: %v", err)
		}

		got, err := repo.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if got.Title != "Updated" {
			t.Errorf("expected title 'Updated', got %q", got.Title)
		}
		if got.Description == nil || *got.Description != "Updated desc" {
			t.Errorf("expected description 'Updated desc', got %v", got.Description)
		}
		if got.Metadata == nil || *got.Metadata != metadata {
			t.Errorf("expected metadata preserved, got %v", got.Metadata)
		}
		if got.Assignee == nil || *got.Assignee != "agent-2" {
			t.Errorf("expected assignee preserved, got %v", got.Assignee)
		}
	})

	t.Run("update task status preserves metadata", func(t *testing.T) {
		metadata := `{"priority":"high"}`
		task := &Task{
			ID:        uuid.New().String(),
			PlanID:    plan.ID,
			Title:     "Status test",
			Status:    int32(reliantv1.TaskStatus_TASK_STATUS_PENDING),
			Position:  4,
			Metadata:  &metadata,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := repo.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}

		if err := repo.UpdateTaskStatus(ctx, task.ID, int32(reliantv1.TaskStatus_TASK_STATUS_COMPLETED), nil); err != nil {
			t.Fatalf("UpdateTaskStatus: %v", err)
		}

		got, err := repo.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if got.Status != int32(reliantv1.TaskStatus_TASK_STATUS_COMPLETED) {
			t.Errorf("expected status completed, got %d", got.Status)
		}
		if got.Title != "Status test" {
			t.Errorf("expected title preserved, got %q", got.Title)
		}
		if got.Metadata == nil || *got.Metadata != metadata {
			t.Errorf("expected metadata preserved, got %v", got.Metadata)
		}
		if got.CompletedAt == nil {
			t.Error("expected completed_at to be set")
		}
	})

	t.Run("list tasks by plan", func(t *testing.T) {
		newPlan := createTestPlan(t, repo, chatID)
		for i := 0; i < 3; i++ {
			task := &Task{
				ID:        uuid.New().String(),
				PlanID:    newPlan.ID,
				Title:     "List Task",
				Status:    int32(reliantv1.TaskStatus_TASK_STATUS_PENDING),
				Position:  i,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			if err := repo.CreateTask(ctx, task); err != nil {
				t.Fatalf("CreateTask: %v", err)
			}
		}

		tasks, err := repo.ListTasksByPlan(ctx, newPlan.ID)
		if err != nil {
			t.Fatalf("ListTasksByPlan: %v", err)
		}
		if len(tasks) != 3 {
			t.Errorf("expected 3 tasks, got %d", len(tasks))
		}
	})

	t.Run("all valid task statuses", func(t *testing.T) {
		statuses := []int32{
			int32(reliantv1.TaskStatus_TASK_STATUS_PENDING),
			int32(reliantv1.TaskStatus_TASK_STATUS_IN_PROGRESS),
			int32(reliantv1.TaskStatus_TASK_STATUS_COMPLETED),
			int32(reliantv1.TaskStatus_TASK_STATUS_FAILED),
			int32(reliantv1.TaskStatus_TASK_STATUS_BLOCKED),
			int32(reliantv1.TaskStatus_TASK_STATUS_CANCELLED),
			int32(reliantv1.TaskStatus_TASK_STATUS_SKIPPED),
		}
		for _, status := range statuses {
			task := &Task{
				ID:        uuid.New().String(),
				PlanID:    plan.ID,
				Title:     fmt.Sprintf("Status %d", status),
				Status:    status,
				Position:  0,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			if err := repo.CreateTask(ctx, task); err != nil {
				t.Errorf("CreateTask with status %d: %v", status, err)
			}
		}
	})

	t.Run("delete task", func(t *testing.T) {
		task := &Task{
			ID:        uuid.New().String(),
			PlanID:    plan.ID,
			Title:     "Delete me",
			Status:    int32(reliantv1.TaskStatus_TASK_STATUS_PENDING),
			Position:  0,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := repo.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}

		if err := repo.DeleteTask(ctx, task.ID); err != nil {
			t.Fatalf("DeleteTask: %v", err)
		}

		_, err := repo.GetTask(ctx, task.ID)
		if err == nil {
			t.Error("expected error when getting deleted task")
		}
	})
}

func TestTaskDependencies(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := createTestChat(t, repo)
	plan := createTestPlan(t, repo, chatID)

	makeTask := func(title string, pos int) *Task {
		t.Helper()
		task := &Task{
			ID:        uuid.New().String(),
			PlanID:    plan.ID,
			Title:     title,
			Status:    int32(reliantv1.TaskStatus_TASK_STATUS_PENDING),
			Position:  pos,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := repo.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask %q: %v", title, err)
		}
		return task
	}

	t.Run("create and get dependency", func(t *testing.T) {
		taskA := makeTask("A", 0)
		taskB := makeTask("B", 1)

		dep := &TaskDependency{
			ID:             uuid.New().String(),
			FromTaskID:     taskA.ID,
			ToTaskID:       taskB.ID,
			DependencyType: core.DependencyTypeBlocks,
			CreatedAt:      time.Now(),
		}
		if err := repo.CreateTaskDependency(ctx, dep); err != nil {
			t.Fatalf("CreateTaskDependency: %v", err)
		}

		got, err := repo.GetTaskDependency(ctx, dep.ID)
		if err != nil {
			t.Fatalf("GetTaskDependency: %v", err)
		}
		if got.FromTaskID != taskA.ID {
			t.Errorf("expected from_task_id %q, got %q", taskA.ID, got.FromTaskID)
		}
		if got.ToTaskID != taskB.ID {
			t.Errorf("expected to_task_id %q, got %q", taskB.ID, got.ToTaskID)
		}
		if got.DependencyType != core.DependencyTypeBlocks {
			t.Errorf("expected type 'blocks', got %q", got.DependencyType)
		}
	})

	t.Run("all dependency types", func(t *testing.T) {
		types := []int32{core.DependencyTypeBlocks, core.DependencyTypeRelated, core.DependencyTypeParallelWith}
		for _, depType := range types {
			a := makeTask(fmt.Sprintf("From-%d", depType), 0)
			b := makeTask(fmt.Sprintf("To-%d", depType), 1)
			dep := &TaskDependency{
				ID:             uuid.New().String(),
				FromTaskID:     a.ID,
				ToTaskID:       b.ID,
				DependencyType: depType,
				CreatedAt:      time.Now(),
			}
			if err := repo.CreateTaskDependency(ctx, dep); err != nil {
				t.Errorf("CreateTaskDependency type %q: %v", depType, err)
			}
		}
	})

	t.Run("self dependency rejected", func(t *testing.T) {
		taskA := makeTask("Self", 0)
		dep := &TaskDependency{
			ID:             uuid.New().String(),
			FromTaskID:     taskA.ID,
			ToTaskID:       taskA.ID,
			DependencyType: core.DependencyTypeBlocks,
			CreatedAt:      time.Now(),
		}
		if err := repo.CreateTaskDependency(ctx, dep); err == nil {
			t.Error("expected error for self dependency")
		}
	})

	t.Run("list dependencies by task", func(t *testing.T) {
		taskA := makeTask("ListA", 0)
		taskB := makeTask("ListB", 1)
		taskC := makeTask("ListC", 2)

		// A blocks B, A related to C
		for _, d := range []*TaskDependency{
			{ID: uuid.New().String(), FromTaskID: taskA.ID, ToTaskID: taskB.ID, DependencyType: core.DependencyTypeBlocks, CreatedAt: time.Now()},
			{ID: uuid.New().String(), FromTaskID: taskA.ID, ToTaskID: taskC.ID, DependencyType: core.DependencyTypeRelated, CreatedAt: time.Now()},
		} {
			if err := repo.CreateTaskDependency(ctx, d); err != nil {
				t.Fatalf("CreateTaskDependency: %v", err)
			}
		}

		// TaskA should appear in deps for A, B, and C
		depsA, err := repo.ListTaskDependenciesByTask(ctx, taskA.ID)
		if err != nil {
			t.Fatalf("ListTaskDependenciesByTask A: %v", err)
		}
		if len(depsA) != 2 {
			t.Errorf("expected 2 deps for taskA, got %d", len(depsA))
		}

		depsB, err := repo.ListTaskDependenciesByTask(ctx, taskB.ID)
		if err != nil {
			t.Fatalf("ListTaskDependenciesByTask B: %v", err)
		}
		if len(depsB) != 1 {
			t.Errorf("expected 1 dep for taskB, got %d", len(depsB))
		}
	})

	t.Run("list blockers for task", func(t *testing.T) {
		taskA := makeTask("BlockerA", 0)
		taskB := makeTask("BlockerB", 1)
		taskC := makeTask("Blocked", 2)

		// A blocks C, B blocks C
		for _, d := range []*TaskDependency{
			{ID: uuid.New().String(), FromTaskID: taskA.ID, ToTaskID: taskC.ID, DependencyType: core.DependencyTypeBlocks, CreatedAt: time.Now()},
			{ID: uuid.New().String(), FromTaskID: taskB.ID, ToTaskID: taskC.ID, DependencyType: core.DependencyTypeBlocks, CreatedAt: time.Now()},
		} {
			if err := repo.CreateTaskDependency(ctx, d); err != nil {
				t.Fatalf("CreateTaskDependency: %v", err)
			}
		}

		blockers, err := repo.ListBlockersForTask(ctx, taskC.ID)
		if err != nil {
			t.Fatalf("ListBlockersForTask: %v", err)
		}
		if len(blockers) != 2 {
			t.Errorf("expected 2 blockers, got %d", len(blockers))
		}
	})

	t.Run("list dependencies by plan", func(t *testing.T) {
		newPlan := createTestPlan(t, repo, chatID)
		a := &Task{ID: uuid.New().String(), PlanID: newPlan.ID, Title: "PlanDepA", Status: int32(reliantv1.TaskStatus_TASK_STATUS_PENDING), Position: 0, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		b := &Task{ID: uuid.New().String(), PlanID: newPlan.ID, Title: "PlanDepB", Status: int32(reliantv1.TaskStatus_TASK_STATUS_PENDING), Position: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		c := &Task{ID: uuid.New().String(), PlanID: newPlan.ID, Title: "PlanDepC", Status: int32(reliantv1.TaskStatus_TASK_STATUS_PENDING), Position: 2, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		for _, task := range []*Task{a, b, c} {
			if err := repo.CreateTask(ctx, task); err != nil {
				t.Fatalf("CreateTask: %v", err)
			}
		}

		deps := []*TaskDependency{
			{ID: uuid.New().String(), FromTaskID: a.ID, ToTaskID: b.ID, DependencyType: core.DependencyTypeBlocks, CreatedAt: time.Now()},
			{ID: uuid.New().String(), FromTaskID: b.ID, ToTaskID: c.ID, DependencyType: core.DependencyTypeBlocks, CreatedAt: time.Now()},
			{ID: uuid.New().String(), FromTaskID: a.ID, ToTaskID: c.ID, DependencyType: core.DependencyTypeRelated, CreatedAt: time.Now()},
		}
		for _, d := range deps {
			if err := repo.CreateTaskDependency(ctx, d); err != nil {
				t.Fatalf("CreateTaskDependency: %v", err)
			}
		}

		planDeps, err := repo.ListDependenciesByPlan(ctx, newPlan.ID)
		if err != nil {
			t.Fatalf("ListDependenciesByPlan: %v", err)
		}
		if len(planDeps) != 3 {
			t.Errorf("expected 3 deps, got %d", len(planDeps))
		}
	})

	t.Run("delete dependency by id", func(t *testing.T) {
		taskA := makeTask("DelA", 0)
		taskB := makeTask("DelB", 1)

		dep := &TaskDependency{
			ID:             uuid.New().String(),
			FromTaskID:     taskA.ID,
			ToTaskID:       taskB.ID,
			DependencyType: core.DependencyTypeBlocks,
			CreatedAt:      time.Now(),
		}
		if err := repo.CreateTaskDependency(ctx, dep); err != nil {
			t.Fatalf("CreateTaskDependency: %v", err)
		}

		if err := repo.DeleteTaskDependency(ctx, dep.ID); err != nil {
			t.Fatalf("DeleteTaskDependency: %v", err)
		}

		_, err := repo.GetTaskDependency(ctx, dep.ID)
		if err == nil {
			t.Error("expected error when getting deleted dependency")
		}
	})

	t.Run("delete dependency by pair", func(t *testing.T) {
		taskA := makeTask("PairA", 0)
		taskB := makeTask("PairB", 1)

		dep := &TaskDependency{
			ID:             uuid.New().String(),
			FromTaskID:     taskA.ID,
			ToTaskID:       taskB.ID,
			DependencyType: core.DependencyTypeRelated,
			CreatedAt:      time.Now(),
		}
		if err := repo.CreateTaskDependency(ctx, dep); err != nil {
			t.Fatalf("CreateTaskDependency: %v", err)
		}

		if err := repo.DeleteTaskDependencyByPair(ctx, taskA.ID, taskB.ID, core.DependencyTypeRelated); err != nil {
			t.Fatalf("DeleteTaskDependencyByPair: %v", err)
		}

		_, err := repo.GetTaskDependency(ctx, dep.ID)
		if err == nil {
			t.Error("expected error when getting deleted dependency")
		}
	})

	t.Run("unique constraint on from-to-type triple", func(t *testing.T) {
		taskA := makeTask("UniqueA", 0)
		taskB := makeTask("UniqueB", 1)

		dep1 := &TaskDependency{
			ID:             uuid.New().String(),
			FromTaskID:     taskA.ID,
			ToTaskID:       taskB.ID,
			DependencyType: core.DependencyTypeBlocks,
			CreatedAt:      time.Now(),
		}
		if err := repo.CreateTaskDependency(ctx, dep1); err != nil {
			t.Fatalf("first CreateTaskDependency: %v", err)
		}

		dep2 := &TaskDependency{
			ID:             uuid.New().String(),
			FromTaskID:     taskA.ID,
			ToTaskID:       taskB.ID,
			DependencyType: core.DependencyTypeBlocks,
			CreatedAt:      time.Now(),
		}
		if err := repo.CreateTaskDependency(ctx, dep2); err == nil {
			t.Error("expected unique constraint violation for duplicate from-to-type")
		}

		// Same pair with different type should succeed
		dep3 := &TaskDependency{
			ID:             uuid.New().String(),
			FromTaskID:     taskA.ID,
			ToTaskID:       taskB.ID,
			DependencyType: core.DependencyTypeRelated,
			CreatedAt:      time.Now(),
		}
		if err := repo.CreateTaskDependency(ctx, dep3); err != nil {
			t.Errorf("different type should be allowed: %v", err)
		}
	})

	t.Run("cascade delete tasks removes dependencies", func(t *testing.T) {
		newPlan := createTestPlan(t, repo, chatID)
		a := &Task{ID: uuid.New().String(), PlanID: newPlan.ID, Title: "CascA", Status: int32(reliantv1.TaskStatus_TASK_STATUS_PENDING), Position: 0, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		b := &Task{ID: uuid.New().String(), PlanID: newPlan.ID, Title: "CascB", Status: int32(reliantv1.TaskStatus_TASK_STATUS_PENDING), Position: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		for _, task := range []*Task{a, b} {
			if err := repo.CreateTask(ctx, task); err != nil {
				t.Fatalf("CreateTask: %v", err)
			}
		}

		dep := &TaskDependency{
			ID: uuid.New().String(), FromTaskID: a.ID, ToTaskID: b.ID,
			DependencyType: core.DependencyTypeBlocks, CreatedAt: time.Now(),
		}
		if err := repo.CreateTaskDependency(ctx, dep); err != nil {
			t.Fatalf("CreateTaskDependency: %v", err)
		}

		// Delete task A — should cascade to the dependency
		if err := repo.DeleteTask(ctx, a.ID); err != nil {
			t.Fatalf("DeleteTask: %v", err)
		}

		_, err := repo.GetTaskDependency(ctx, dep.ID)
		if err == nil {
			t.Error("expected dependency to be cascade-deleted with task")
		}
	})
}

func TestReadyTaskComputation(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := createTestChat(t, repo)
	plan := createTestPlan(t, repo, chatID)

	makeTask := func(title string, pos int, status int32) *Task {
		t.Helper()
		task := &Task{
			ID:        uuid.New().String(),
			PlanID:    plan.ID,
			Title:     title,
			Status:    status,
			Position:  pos,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := repo.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask %q: %v", title, err)
		}
		return task
	}

	addBlock := func(from, to *Task) {
		t.Helper()
		dep := &TaskDependency{
			ID: uuid.New().String(), FromTaskID: from.ID, ToTaskID: to.ID,
			DependencyType: core.DependencyTypeBlocks, CreatedAt: time.Now(),
		}
		if err := repo.CreateTaskDependency(ctx, dep); err != nil {
			t.Fatalf("CreateTaskDependency: %v", err)
		}
	}

	// computeReady replicates the ready-task logic from ListReadyTasksTool
	computeReady := func(planID string) []*Task {
		t.Helper()
		allTasks, err := repo.ListTasksByPlan(ctx, planID)
		if err != nil {
			t.Fatalf("ListTasksByPlan: %v", err)
		}
		allDeps, err := repo.ListDependenciesByPlan(ctx, planID)
		if err != nil {
			t.Fatalf("ListDependenciesByPlan: %v", err)
		}

		taskStatus := make(map[string]int32)
		for _, task := range allTasks {
			taskStatus[task.ID] = task.Status
		}

		blockers := make(map[string][]string)
		for _, dep := range allDeps {
			if dep.DependencyType == core.DependencyTypeBlocks {
				blockers[dep.ToTaskID] = append(blockers[dep.ToTaskID], dep.FromTaskID)
			}
		}

		var ready []*Task
		for _, task := range allTasks {
			if task.Status != int32(TaskStatusPending) {
				continue
			}
			allComplete := true
			for _, bid := range blockers[task.ID] {
				if s, ok := taskStatus[bid]; ok && s != int32(TaskStatusCompleted) {
					allComplete = false
					break
				}
			}
			if allComplete {
				ready = append(ready, task)
			}
		}
		return ready
	}

	t.Run("no dependencies means all pending are ready", func(t *testing.T) {
		newPlan := createTestPlan(t, repo, chatID)
		for i := 0; i < 3; i++ {
			task := &Task{
				ID: uuid.New().String(), PlanID: newPlan.ID, Title: "Free",
				Status: int32(reliantv1.TaskStatus_TASK_STATUS_PENDING), Position: i, CreatedAt: time.Now(), UpdatedAt: time.Now(),
			}
			if err := repo.CreateTask(ctx, task); err != nil {
				t.Fatalf("CreateTask: %v", err)
			}
		}

		ready := computeReady(newPlan.ID)
		if len(ready) != 3 {
			t.Errorf("expected 3 ready tasks, got %d", len(ready))
		}
	})

	t.Run("blocked task not ready", func(t *testing.T) {
		// A (pending) -> blocks -> B (pending)
		// Only A should be ready
		taskA := makeTask("ReadyA", 0, int32(reliantv1.TaskStatus_TASK_STATUS_PENDING))
		taskB := makeTask("BlockedB", 1, int32(reliantv1.TaskStatus_TASK_STATUS_PENDING))
		addBlock(taskA, taskB)

		ready := computeReady(plan.ID)
		readyIDs := map[string]bool{}
		for _, r := range ready {
			readyIDs[r.ID] = true
		}

		if !readyIDs[taskA.ID] {
			t.Error("taskA should be ready")
		}
		if readyIDs[taskB.ID] {
			t.Error("taskB should NOT be ready (blocked by A)")
		}
	})

	t.Run("completed blocker unblocks task", func(t *testing.T) {
		newPlan := createTestPlan(t, repo, chatID)
		a := &Task{ID: uuid.New().String(), PlanID: newPlan.ID, Title: "Done", Status: int32(reliantv1.TaskStatus_TASK_STATUS_COMPLETED), Position: 0, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		b := &Task{ID: uuid.New().String(), PlanID: newPlan.ID, Title: "NowReady", Status: int32(reliantv1.TaskStatus_TASK_STATUS_PENDING), Position: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		for _, task := range []*Task{a, b} {
			if err := repo.CreateTask(ctx, task); err != nil {
				t.Fatalf("CreateTask: %v", err)
			}
		}
		dep := &TaskDependency{
			ID: uuid.New().String(), FromTaskID: a.ID, ToTaskID: b.ID,
			DependencyType: core.DependencyTypeBlocks, CreatedAt: time.Now(),
		}
		if err := repo.CreateTaskDependency(ctx, dep); err != nil {
			t.Fatalf("CreateTaskDependency: %v", err)
		}

		ready := computeReady(newPlan.ID)
		if len(ready) != 1 || ready[0].ID != b.ID {
			t.Errorf("expected only task B to be ready, got %d tasks", len(ready))
		}
	})

	t.Run("chain: A->B->C only A ready initially", func(t *testing.T) {
		newPlan := createTestPlan(t, repo, chatID)
		a := &Task{ID: uuid.New().String(), PlanID: newPlan.ID, Title: "Chain A", Status: int32(reliantv1.TaskStatus_TASK_STATUS_PENDING), Position: 0, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		b := &Task{ID: uuid.New().String(), PlanID: newPlan.ID, Title: "Chain B", Status: int32(reliantv1.TaskStatus_TASK_STATUS_PENDING), Position: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		c := &Task{ID: uuid.New().String(), PlanID: newPlan.ID, Title: "Chain C", Status: int32(reliantv1.TaskStatus_TASK_STATUS_PENDING), Position: 2, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		for _, task := range []*Task{a, b, c} {
			if err := repo.CreateTask(ctx, task); err != nil {
				t.Fatalf("CreateTask: %v", err)
			}
		}
		for _, d := range []*TaskDependency{
			{ID: uuid.New().String(), FromTaskID: a.ID, ToTaskID: b.ID, DependencyType: core.DependencyTypeBlocks, CreatedAt: time.Now()},
			{ID: uuid.New().String(), FromTaskID: b.ID, ToTaskID: c.ID, DependencyType: core.DependencyTypeBlocks, CreatedAt: time.Now()},
		} {
			if err := repo.CreateTaskDependency(ctx, d); err != nil {
				t.Fatalf("CreateTaskDependency: %v", err)
			}
		}

		ready := computeReady(newPlan.ID)
		if len(ready) != 1 || ready[0].ID != a.ID {
			t.Errorf("expected only chain A ready, got %d", len(ready))
		}

		// Complete A -> B becomes ready
		if err := repo.UpdateTaskStatus(ctx, a.ID, int32(reliantv1.TaskStatus_TASK_STATUS_COMPLETED), nil); err != nil {
			t.Fatalf("UpdateTaskStatus: %v", err)
		}
		ready = computeReady(newPlan.ID)
		if len(ready) != 1 || ready[0].ID != b.ID {
			t.Errorf("expected only chain B ready after A completed, got %d", len(ready))
		}

		// Complete B -> C becomes ready
		if err := repo.UpdateTaskStatus(ctx, b.ID, int32(reliantv1.TaskStatus_TASK_STATUS_COMPLETED), nil); err != nil {
			t.Fatalf("UpdateTaskStatus: %v", err)
		}
		ready = computeReady(newPlan.ID)
		if len(ready) != 1 || ready[0].ID != c.ID {
			t.Errorf("expected only chain C ready after B completed, got %d", len(ready))
		}
	})

	t.Run("diamond: A->{B,C}->D", func(t *testing.T) {
		newPlan := createTestPlan(t, repo, chatID)
		a := &Task{ID: uuid.New().String(), PlanID: newPlan.ID, Title: "Diamond A", Status: int32(reliantv1.TaskStatus_TASK_STATUS_PENDING), Position: 0, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		b := &Task{ID: uuid.New().String(), PlanID: newPlan.ID, Title: "Diamond B", Status: int32(reliantv1.TaskStatus_TASK_STATUS_PENDING), Position: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		c := &Task{ID: uuid.New().String(), PlanID: newPlan.ID, Title: "Diamond C", Status: int32(reliantv1.TaskStatus_TASK_STATUS_PENDING), Position: 2, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		d := &Task{ID: uuid.New().String(), PlanID: newPlan.ID, Title: "Diamond D", Status: int32(reliantv1.TaskStatus_TASK_STATUS_PENDING), Position: 3, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		for _, task := range []*Task{a, b, c, d} {
			if err := repo.CreateTask(ctx, task); err != nil {
				t.Fatalf("CreateTask: %v", err)
			}
		}

		// A blocks B, A blocks C, B blocks D, C blocks D
		for _, dep := range []*TaskDependency{
			{ID: uuid.New().String(), FromTaskID: a.ID, ToTaskID: b.ID, DependencyType: core.DependencyTypeBlocks, CreatedAt: time.Now()},
			{ID: uuid.New().String(), FromTaskID: a.ID, ToTaskID: c.ID, DependencyType: core.DependencyTypeBlocks, CreatedAt: time.Now()},
			{ID: uuid.New().String(), FromTaskID: b.ID, ToTaskID: d.ID, DependencyType: core.DependencyTypeBlocks, CreatedAt: time.Now()},
			{ID: uuid.New().String(), FromTaskID: c.ID, ToTaskID: d.ID, DependencyType: core.DependencyTypeBlocks, CreatedAt: time.Now()},
		} {
			if err := repo.CreateTaskDependency(ctx, dep); err != nil {
				t.Fatalf("CreateTaskDependency: %v", err)
			}
		}

		// Only A ready initially
		ready := computeReady(newPlan.ID)
		if len(ready) != 1 || ready[0].ID != a.ID {
			t.Errorf("expected only A ready, got %d", len(ready))
		}

		// Complete A -> B and C ready, D still blocked
		if err := repo.UpdateTaskStatus(ctx, a.ID, int32(reliantv1.TaskStatus_TASK_STATUS_COMPLETED), nil); err != nil {
			t.Fatalf("UpdateTaskStatus: %v", err)
		}
		ready = computeReady(newPlan.ID)
		readyIDs := map[string]bool{}
		for _, r := range ready {
			readyIDs[r.ID] = true
		}
		if len(ready) != 2 || !readyIDs[b.ID] || !readyIDs[c.ID] {
			t.Errorf("expected B and C ready, got %v", readyIDs)
		}

		// Complete B only -> D still blocked (C not done)
		if err := repo.UpdateTaskStatus(ctx, b.ID, int32(reliantv1.TaskStatus_TASK_STATUS_COMPLETED), nil); err != nil {
			t.Fatalf("UpdateTaskStatus: %v", err)
		}
		ready = computeReady(newPlan.ID)
		readyIDs = map[string]bool{}
		for _, r := range ready {
			readyIDs[r.ID] = true
		}
		if readyIDs[d.ID] {
			t.Error("D should NOT be ready until both B and C complete")
		}
		if !readyIDs[c.ID] {
			t.Error("C should still be ready")
		}

		// Complete C -> D now ready
		if err := repo.UpdateTaskStatus(ctx, c.ID, int32(reliantv1.TaskStatus_TASK_STATUS_COMPLETED), nil); err != nil {
			t.Fatalf("UpdateTaskStatus: %v", err)
		}
		ready = computeReady(newPlan.ID)
		if len(ready) != 1 || ready[0].ID != d.ID {
			t.Errorf("expected only D ready after B+C complete, got %d", len(ready))
		}
	})

	t.Run("related dependencies dont block", func(t *testing.T) {
		newPlan := createTestPlan(t, repo, chatID)
		a := &Task{ID: uuid.New().String(), PlanID: newPlan.ID, Title: "Related A", Status: int32(reliantv1.TaskStatus_TASK_STATUS_PENDING), Position: 0, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		b := &Task{ID: uuid.New().String(), PlanID: newPlan.ID, Title: "Related B", Status: int32(reliantv1.TaskStatus_TASK_STATUS_PENDING), Position: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		for _, task := range []*Task{a, b} {
			if err := repo.CreateTask(ctx, task); err != nil {
				t.Fatalf("CreateTask: %v", err)
			}
		}

		dep := &TaskDependency{
			ID: uuid.New().String(), FromTaskID: a.ID, ToTaskID: b.ID,
			DependencyType: core.DependencyTypeRelated, CreatedAt: time.Now(),
		}
		if err := repo.CreateTaskDependency(ctx, dep); err != nil {
			t.Fatalf("CreateTaskDependency: %v", err)
		}

		// Both should be ready since 'related' doesn't block
		ready := computeReady(newPlan.ID)
		if len(ready) != 2 {
			t.Errorf("expected 2 ready tasks (related doesn't block), got %d", len(ready))
		}
	})

	t.Run("parallel_with dependencies dont block", func(t *testing.T) {
		newPlan := createTestPlan(t, repo, chatID)
		a := &Task{ID: uuid.New().String(), PlanID: newPlan.ID, Title: "Par A", Status: int32(reliantv1.TaskStatus_TASK_STATUS_PENDING), Position: 0, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		b := &Task{ID: uuid.New().String(), PlanID: newPlan.ID, Title: "Par B", Status: int32(reliantv1.TaskStatus_TASK_STATUS_PENDING), Position: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		for _, task := range []*Task{a, b} {
			if err := repo.CreateTask(ctx, task); err != nil {
				t.Fatalf("CreateTask: %v", err)
			}
		}

		dep := &TaskDependency{
			ID: uuid.New().String(), FromTaskID: a.ID, ToTaskID: b.ID,
			DependencyType: core.DependencyTypeParallelWith, CreatedAt: time.Now(),
		}
		if err := repo.CreateTaskDependency(ctx, dep); err != nil {
			t.Fatalf("CreateTaskDependency: %v", err)
		}

		ready := computeReady(newPlan.ID)
		if len(ready) != 2 {
			t.Errorf("expected 2 ready tasks (parallel_with doesn't block), got %d", len(ready))
		}
	})
}

func TestGetTaskByPosition(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := createTestChat(t, repo)
	plan := createTestPlan(t, repo, chatID)

	var taskIDs []string
	for i := 0; i < 3; i++ {
		task := &Task{
			ID: uuid.New().String(), PlanID: plan.ID, Title: "Pos Task",
			Status: int32(reliantv1.TaskStatus_TASK_STATUS_PENDING), Position: i, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}
		if err := repo.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		taskIDs = append(taskIDs, task.ID)
	}

	got, err := repo.GetTaskByPosition(ctx, plan.ID, 1)
	if err != nil {
		t.Fatalf("GetTaskByPosition: %v", err)
	}
	if got.ID != taskIDs[1] {
		t.Errorf("expected task at position 1, got %q", got.ID)
	}

	_, err = repo.GetTaskByPosition(ctx, plan.ID, 99)
	if err == nil {
		t.Error("expected error for out-of-range position")
	}
}

func TestTaskStats(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := createTestChat(t, repo)
	plan := createTestPlan(t, repo, chatID)

	statuses := map[int32]int{
		int32(reliantv1.TaskStatus_TASK_STATUS_PENDING):     3,
		int32(reliantv1.TaskStatus_TASK_STATUS_IN_PROGRESS): 2,
		int32(reliantv1.TaskStatus_TASK_STATUS_COMPLETED):   4,
		int32(reliantv1.TaskStatus_TASK_STATUS_FAILED):      1,
	}

	pos := 0
	for status, count := range statuses {
		for i := 0; i < count; i++ {
			task := &Task{
				ID: uuid.New().String(), PlanID: plan.ID, Title: "Stat Task",
				Status: status, Position: pos, CreatedAt: time.Now(), UpdatedAt: time.Now(),
			}
			if err := repo.CreateTask(ctx, task); err != nil {
				t.Fatalf("CreateTask: %v", err)
			}
			pos++
		}
	}

	stats, err := repo.GetTaskStatsByPlan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("GetTaskStatsByPlan: %v", err)
	}

	if stats.Total != 10 {
		t.Errorf("expected 10 total, got %d", stats.Total)
	}
	if stats.Pending != 3 {
		t.Errorf("expected 3 pending, got %d", stats.Pending)
	}
	if stats.InProgress != 2 {
		t.Errorf("expected 2 in_progress, got %d", stats.InProgress)
	}
	if stats.Completed != 4 {
		t.Errorf("expected 4 completed, got %d", stats.Completed)
	}
	if stats.Failed != 1 {
		t.Errorf("expected 1 failed, got %d", stats.Failed)
	}
}
