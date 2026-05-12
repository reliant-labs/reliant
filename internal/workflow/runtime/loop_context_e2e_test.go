// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

// =============================================================================
// E2E Test: Loop Context Recording - Step Executor & Database
// =============================================================================
//
// This test verifies:
// 1. The step executor correctly injects loop context into activity inputs
// 2. The database correctly stores and retrieves loop context
// 3. The extractActivityInputInfo function correctly extracts loop context
//
// For activity input struct field tests, see:
// internal/workflow/runtime/activities/handlers/loop_context_test.go
// =============================================================================

// TestStepExecutorLoopContextInjection verifies that StepExecutor injects loop context
func TestStepExecutorLoopContextInjection(t *testing.T) {
	t.Run("StepExecutor has loop context fields", func(t *testing.T) {
		// Verify the struct fields exist
		executor := &StepExecutor{
			loopNodeID:    "test_loop",
			loopIteration: 5,
		}

		assert.Equal(t, "test_loop", executor.loopNodeID)
		assert.Equal(t, 5, executor.loopIteration)

		t.Logf("✓ StepExecutor has loopNodeID=%s, loopIteration=%d fields",
			executor.loopNodeID, executor.loopIteration)
	})

	t.Run("buildRuntimeContext includes loop context", func(t *testing.T) {
		executor := &StepExecutor{
			workflowID:    "test-workflow",
			chatID:        "test-chat",
			loopNodeID:    "inject_test_loop",
			loopIteration: 3,
		}

		node := &reliantv1.Node{Id: "test-step", Type: "run"}
		rtx := executor.buildRuntimeContext(node)

		assert.Equal(t, "inject_test_loop", rtx.LoopNodeID)
		assert.Equal(t, 3, rtx.LoopIteration)
		assert.Equal(t, "test-workflow", rtx.WorkflowID)
		assert.Equal(t, "test-chat", rtx.ChatID)
		assert.Equal(t, "test-step", rtx.StepID)

		t.Logf("✓ buildRuntimeContext includes loop_node_id=%s, loop_iteration=%d",
			rtx.LoopNodeID, rtx.LoopIteration)
	})

	t.Run("buildRuntimeContext omits loop context when not in loop", func(t *testing.T) {
		executor := &StepExecutor{
			workflowID: "test-workflow",
			chatID:     "test-chat",
			loopNodeID: "", // Empty = not in a loop
		}

		node := &reliantv1.Node{Id: "test-step", Type: "run"}
		rtx := executor.buildRuntimeContext(node)

		assert.Empty(t, rtx.LoopNodeID, "Should not have loop_node_id when not in loop")
		assert.Equal(t, 0, rtx.LoopIteration, "Should have zero loop_iteration when not in loop")

		t.Logf("✓ buildRuntimeContext correctly omits loop context when not in loop")
	})

	t.Run("WithLoopContext returns executor for chaining", func(t *testing.T) {
		executor := &StepExecutor{}

		result := executor.WithLoopContext("chain_test", 7)

		// Verify it returns the same executor (for method chaining)
		assert.Same(t, executor, result)
		assert.Equal(t, "chain_test", executor.loopNodeID)
		assert.Equal(t, 7, executor.loopIteration)

		t.Logf("✓ WithLoopContext returns executor for chaining")
	})
}

// TestExtractActivityInputInfo verifies the extraction function used in activity wrapper
func TestExtractActivityInputInfo(t *testing.T) {
	t.Run("extracts loop context from map with float64 iteration", func(t *testing.T) {
		// JSON numbers deserialize as float64
		inputMap := map[string]interface{}{
			"workflow_id":    "extract-workflow",
			"chat_id":        "extract-chat",
			"step_id":        "extract-step",
			"loop_node_id":   "extract_loop",
			"loop_iteration": float64(4),
		}

		info := extractActivityInputInfo(inputMap)

		assert.Equal(t, "extract-step", info.StepID)
		assert.Equal(t, "extract-workflow", info.WorkflowID)
		assert.Equal(t, "extract_loop", info.LoopNodeID)
		assert.Equal(t, 4, info.LoopIteration)

		t.Logf("✓ extractActivityInputInfo extracted step_id=%s, loop_node_id=%s, loop_iteration=%d",
			info.StepID, info.LoopNodeID, info.LoopIteration)
	})

	t.Run("extracts loop context from map with int iteration", func(t *testing.T) {
		// Direct int assignment (not from JSON)
		inputMap := map[string]interface{}{
			"loop_node_id":   "int_loop",
			"loop_iteration": 5,
		}

		info := extractActivityInputInfo(inputMap)

		assert.Equal(t, "int_loop", info.LoopNodeID)
		assert.Equal(t, 5, info.LoopIteration)

		t.Logf("✓ extractActivityInputInfo handles int loop_iteration")
	})

	t.Run("extracts loop context from map with int64 iteration", func(t *testing.T) {
		inputMap := map[string]interface{}{
			"loop_node_id":   "int64_loop",
			"loop_iteration": int64(6),
		}

		info := extractActivityInputInfo(inputMap)

		assert.Equal(t, "int64_loop", info.LoopNodeID)
		assert.Equal(t, 6, info.LoopIteration)

		t.Logf("✓ extractActivityInputInfo handles int64 loop_iteration")
	})

	t.Run("handles missing loop context", func(t *testing.T) {
		inputMap := map[string]interface{}{
			"workflow_id": "no-loop-workflow",
			"chat_id":     "no-loop-chat",
			"step_id":     "no-loop-step",
		}

		info := extractActivityInputInfo(inputMap)

		assert.Equal(t, "", info.LoopNodeID)
		assert.Equal(t, -1, info.LoopIteration) // Default value

		t.Logf("✓ extractActivityInputInfo handles missing loop context correctly")
	})

	t.Run("handles iteration 0 correctly", func(t *testing.T) {
		// Iteration 0 is valid and should not be confused with "no loop"
		inputMap := map[string]interface{}{
			"loop_node_id":   "zero_iter_loop",
			"loop_iteration": float64(0),
		}

		info := extractActivityInputInfo(inputMap)

		assert.Equal(t, "zero_iter_loop", info.LoopNodeID)
		assert.Equal(t, 0, info.LoopIteration)

		t.Logf("✓ extractActivityInputInfo correctly handles iteration 0")
	})
}

// TestLoopContextDatabasePersistence verifies loop context is stored correctly in DB
func TestLoopContextDatabasePersistence(t *testing.T) {
	// Create test database
	repo := db.NewTestRepo(t)

	ctx := context.Background()

	// Create test project
	projectID := uuid.New().String()
	err := repo.CreateProject(ctx, &db.Project{
		ID:        projectID,
		Name:      "test-project",
		Path:      "/tmp/test",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create test chat
	chatID := uuid.New().String()
	workflowIDStr := chatID
	err = repo.CreateChat(ctx, &db.Chat{
		ID:         chatID,
		UserID:     "test-user",
		Title:      "Test Chat",
		ProjectID:  projectID,
		State:      db.ChatStateIdle,
		WorkflowID: &workflowIDStr,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		LastActive: time.Now(),
	})
	require.NoError(t, err)

	// Create workflow
	workflowID := chatID
	err = repo.CreateWorkflow(ctx, &db.Workflow{
		ID:           workflowID,
		ChatID:       chatID,
		WorkflowName: "test-workflow",
		Status:       db.WorkflowStatusRunning,
		CreatedAt:    time.Now(),
	})
	require.NoError(t, err)

	t.Run("step execution with loop context", func(t *testing.T) {
		stepID := "step-in-loop-" + uuid.New().String()[:8]
		stepExec := &db.StepExecution{
			ID:            uuid.New().String(),
			WorkflowID:    workflowID,
			StepID:        stepID,
			ActivityName:  "ExecuteRunStep",
			Success:       sql.NullBool{Bool: true, Valid: true},
			DurationMs:    sql.NullInt64{Int64: 100, Valid: true},
			LoopNodeID:    sql.NullString{String: "test_loop", Valid: true},
			LoopIteration: sql.NullInt64{Int64: 2, Valid: true},
			CreatedAt:     time.Now(),
		}

		err = repo.CreateStepExecution(ctx, stepExec)
		require.NoError(t, err)

		// Retrieve and verify
		retrieved, err := repo.GetStepExecution(ctx, stepExec.ID)
		require.NoError(t, err)

		assert.True(t, retrieved.LoopNodeID.Valid, "LoopNodeID should be valid")
		assert.Equal(t, "test_loop", retrieved.LoopNodeID.String)
		assert.True(t, retrieved.LoopIteration.Valid, "LoopIteration should be valid")
		assert.Equal(t, int64(2), retrieved.LoopIteration.Int64)

		t.Logf("✓ Step execution stored with loop_node_id=%s, loop_iteration=%d",
			retrieved.LoopNodeID.String, retrieved.LoopIteration.Int64)
	})

	t.Run("step execution without loop context", func(t *testing.T) {
		stepID := "step-no-loop-" + uuid.New().String()[:8]
		stepExec := &db.StepExecution{
			ID:           uuid.New().String(),
			WorkflowID:   workflowID,
			StepID:       stepID,
			ActivityName: "SaveMessage",
			Success:      sql.NullBool{Bool: true, Valid: true},
			DurationMs:   sql.NullInt64{Int64: 50, Valid: true},
			CreatedAt:    time.Now(),
		}

		err = repo.CreateStepExecution(ctx, stepExec)
		require.NoError(t, err)

		// Retrieve and verify
		retrieved, err := repo.GetStepExecution(ctx, stepExec.ID)
		require.NoError(t, err)

		assert.False(t, retrieved.LoopNodeID.Valid, "LoopNodeID should be NULL for non-loop step")
		assert.False(t, retrieved.LoopIteration.Valid, "LoopIteration should be NULL for non-loop step")

		t.Logf("✓ Non-loop step execution stored with NULL loop context")
	})

	t.Run("query steps by workflow includes loop context", func(t *testing.T) {
		// Create step executions with different loop iterations
		for i := 0; i < 3; i++ {
			stepExec := &db.StepExecution{
				ID:            uuid.New().String(),
				WorkflowID:    workflowID,
				StepID:        "loop-step",
				ActivityName:  "ExecuteRunStep",
				Success:       sql.NullBool{Bool: true, Valid: true},
				DurationMs:    sql.NullInt64{Int64: 100, Valid: true},
				LoopNodeID:    sql.NullString{String: "iteration_test_loop", Valid: true},
				LoopIteration: sql.NullInt64{Int64: int64(i), Valid: true},
				CreatedAt:     time.Now().Add(time.Duration(i) * time.Second),
			}
			err = repo.CreateStepExecution(ctx, stepExec)
			require.NoError(t, err)
		}

		// Query all steps for workflow
		steps, err := repo.GetStepExecutionsByWorkflow(ctx, workflowID)
		require.NoError(t, err)

		// Find the iteration test steps
		var iterationSteps []*db.StepExecution
		for _, step := range steps {
			if step.LoopNodeID.Valid && step.LoopNodeID.String == "iteration_test_loop" {
				iterationSteps = append(iterationSteps, step)
			}
		}

		assert.Len(t, iterationSteps, 3, "Should have 3 iteration steps")

		// Verify we can distinguish iterations
		iterations := make(map[int64]bool)
		for _, step := range iterationSteps {
			iterations[step.LoopIteration.Int64] = true
		}

		assert.True(t, iterations[0], "Should have iteration 0")
		assert.True(t, iterations[1], "Should have iteration 1")
		assert.True(t, iterations[2], "Should have iteration 2")

		t.Logf("✓ Query returned %d steps with iterations: %v", len(iterationSteps), iterations)
	})

	t.Run("iteration 0 stored correctly (not NULL)", func(t *testing.T) {
		// Regression: ensure iteration 0 is not confused with NULL
		stepExec := &db.StepExecution{
			ID:            uuid.New().String(),
			WorkflowID:    workflowID,
			StepID:        "zero-iter-step",
			ActivityName:  "ExecuteRunStep",
			Success:       sql.NullBool{Bool: true, Valid: true},
			LoopNodeID:    sql.NullString{String: "zero_iter_loop", Valid: true},
			LoopIteration: sql.NullInt64{Int64: 0, Valid: true},
			CreatedAt:     time.Now(),
		}

		err = repo.CreateStepExecution(ctx, stepExec)
		require.NoError(t, err)

		retrieved, err := repo.GetStepExecution(ctx, stepExec.ID)
		require.NoError(t, err)

		assert.True(t, retrieved.LoopIteration.Valid, "Iteration 0 should be Valid, not NULL")
		assert.Equal(t, int64(0), retrieved.LoopIteration.Int64)

		t.Logf("✓ Iteration 0 stored correctly as Valid=true, Int64=0")
	})
}

// =============================================================================
// Integration Test: Full Loop Execution Flow
// =============================================================================

func TestLoopContextIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// TODO: Add full Temporal workflow test once we have proper mocking infrastructure
	// The test would:
	// 1. Create a workflow with an inline loop
	// 2. Execute it using Temporal test environment
	// 3. Verify step_executions have correct loop_node_id and loop_iteration
	//
	// For now, the component tests above verify:
	// - Activity input structs have loop fields (handlers/loop_context_test.go)
	// - Step executor injects loop context (TestStepExecutorLoopContextInjection)
	// - Extraction function works (TestExtractActivityInputInfo)
	// - Database persists loop context (TestLoopContextDatabasePersistence)

	t.Log("Integration test placeholder - requires Temporal test environment setup")
}
