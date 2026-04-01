// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreatePlan_InlineDependencies(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)
	ctx := createTestContext(t, chatID)

	tool := &CreatePlanTool{repo: repo}

	t.Run("creates dependencies from position references", func(t *testing.T) {
		params := CreatePlanParams{
			Title:       "Test Plan with Deps",
			Description: "A plan to test inline dependencies",
			Complexity:  "moderate",
			Tasks: []TaskInput{
				{Title: "Task A"},
				{Title: "Task B", Dependencies: []TaskDependencyInput{
					{TaskPosition: 1, Type: "blocks"},
				}},
				{Title: "Task C", Dependencies: []TaskDependencyInput{
					{TaskPosition: 1, Type: "blocks"},
					{TaskPosition: 2, Type: "related"},
				}},
				{Title: "Task D", Dependencies: []TaskDependencyInput{
					{TaskPosition: 3, Type: "blocks"},
				}},
			},
		}

		resp, err := tool.Execute(ctx, params)
		require.NoError(t, err)

		text := resp.Content
		assert.Contains(t, text, "Plan created successfully!")
		assert.Contains(t, text, "Tasks (4 created)")
		assert.Contains(t, text, "Dependencies (4 created)")
		assert.Contains(t, text, "Task A blocks Task B")
		assert.Contains(t, text, "Task A blocks Task C")
		assert.Contains(t, text, "Task B related Task C")
		assert.Contains(t, text, "Task C blocks Task D")
		assert.NotContains(t, text, "Dependency warnings")

		// Verify dependencies were actually persisted
		plan, err := repo.GetPlanByThreadID(ctx.Context, chatID)
		require.NoError(t, err)

		deps, err := repo.ListDependenciesByPlan(ctx.Context, plan.ID)
		require.NoError(t, err)
		assert.Len(t, deps, 4)

		// Verify dependency types
		typeCount := map[int32]int{}
		for _, d := range deps {
			typeCount[d.DependencyType]++
		}
		assert.Equal(t, 3, typeCount[core.DependencyTypeBlocks])
		assert.Equal(t, 1, typeCount[core.DependencyTypeRelated])
	})
}

func TestCreatePlan_InlineDependencies_Validation(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	t.Run("out of range position", func(t *testing.T) {
		chatID := uuid.New().String()
		createTestChat(t, repo, chatID)
		ctx := createTestContext(t, chatID)
		tool := &CreatePlanTool{repo: repo}

		params := CreatePlanParams{
			Title:       "Out of Range Test",
			Description: "Testing out-of-range position",
			Tasks: []TaskInput{
				{Title: "Task A"},
				{Title: "Task B", Dependencies: []TaskDependencyInput{
					{TaskPosition: 99, Type: "blocks"},
				}},
			},
		}

		resp, err := tool.Execute(ctx, params)
		require.NoError(t, err)

		text := resp.Content
		assert.Contains(t, text, "Tasks (2 created)")
		assert.Contains(t, text, "Dependency warnings")
		assert.Contains(t, text, "task_position 99 is out of range")
	})

	t.Run("self reference", func(t *testing.T) {
		chatID := uuid.New().String()
		createTestChat(t, repo, chatID)
		ctx := createTestContext(t, chatID)
		tool := &CreatePlanTool{repo: repo}

		params := CreatePlanParams{
			Title:       "Self Ref Test",
			Description: "Testing self-reference",
			Tasks: []TaskInput{
				{Title: "Task A", Dependencies: []TaskDependencyInput{
					{TaskPosition: 1, Type: "blocks"},
				}},
			},
		}

		resp, err := tool.Execute(ctx, params)
		require.NoError(t, err)

		text := resp.Content
		assert.Contains(t, text, "Dependency warnings")
		assert.Contains(t, text, "cannot depend on itself")
	})

	t.Run("invalid dependency type", func(t *testing.T) {
		chatID := uuid.New().String()
		createTestChat(t, repo, chatID)
		ctx := createTestContext(t, chatID)
		tool := &CreatePlanTool{repo: repo}

		params := CreatePlanParams{
			Title:       "Invalid Type Test",
			Description: "Testing invalid type",
			Tasks: []TaskInput{
				{Title: "Task A"},
				{Title: "Task B", Dependencies: []TaskDependencyInput{
					{TaskPosition: 1, Type: "invalid_type"},
				}},
			},
		}

		resp, err := tool.Execute(ctx, params)
		require.NoError(t, err)

		text := resp.Content
		assert.Contains(t, text, "Dependency warnings")
		assert.Contains(t, text, "invalid dependency type")
	})

	t.Run("zero position", func(t *testing.T) {
		chatID := uuid.New().String()
		createTestChat(t, repo, chatID)
		ctx := createTestContext(t, chatID)
		tool := &CreatePlanTool{repo: repo}

		params := CreatePlanParams{
			Title:       "Zero Position Test",
			Description: "Testing zero position",
			Tasks: []TaskInput{
				{Title: "Task A"},
				{Title: "Task B", Dependencies: []TaskDependencyInput{
					{TaskPosition: 0, Type: "blocks"},
				}},
			},
		}

		resp, err := tool.Execute(ctx, params)
		require.NoError(t, err)

		text := resp.Content
		assert.Contains(t, text, "Dependency warnings")
		assert.Contains(t, text, "out of range")
	})
}

func TestCreatePlan_NoDependencies_BackwardsCompat(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)
	ctx := createTestContext(t, chatID)
	tool := &CreatePlanTool{repo: repo}

	params := CreatePlanParams{
		Title:       "Simple Plan",
		Description: "A plan with no dependencies",
		Complexity:  "simple",
		Tasks: []TaskInput{
			{Title: "Task A"},
			{Title: "Task B"},
			{Title: "Task C"},
		},
	}

	resp, err := tool.Execute(ctx, params)
	require.NoError(t, err)

	text := resp.Content
	assert.Contains(t, text, "Plan created successfully!")
	assert.Contains(t, text, "Tasks (3 created)")
	assert.NotContains(t, text, "Dependencies")
	assert.NotContains(t, text, "Dependency warnings")
}

func TestCreatePlan_TaskMetadata(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)
	ctx := createTestContext(t, chatID)
	tool := &CreatePlanTool{repo: repo}

	params := CreatePlanParams{
		Title:       "Plan with Metadata",
		Description: "Testing metadata passthrough",
		Complexity:  "moderate",
		Tasks: []TaskInput{
			{
				Title:       "Research task",
				Description: "Do some research",
				Metadata: &TaskMetadata{
					PreferredAgent: "research",
					Priority:       "high",
					Notes:          "Check existing patterns first",
				},
			},
			{
				Title: "Implement task",
				Metadata: &TaskMetadata{
					PreferredAgent: "implementation",
					ToolHints:      []string{"use_bash", "test_first"},
				},
			},
		},
	}

	resp, err := tool.Execute(ctx, params)
	require.NoError(t, err)

	text := resp.Content
	assert.Contains(t, text, "Plan created successfully!")
	assert.Contains(t, text, "Tasks (2 created)")

	// Verify metadata was stored
	plan, err := repo.GetPlanByThreadID(ctx.Context, chatID)
	require.NoError(t, err)

	tasks, err := repo.ListTasksByPlan(ctx.Context, plan.ID)
	require.NoError(t, err)
	require.Len(t, tasks, 2)

	// Check first task metadata
	require.NotNil(t, tasks[0].Metadata)
	var meta1 TaskMetadata
	err = json.Unmarshal([]byte(*tasks[0].Metadata), &meta1)
	require.NoError(t, err)
	assert.Equal(t, "research", meta1.PreferredAgent)
	assert.Equal(t, "high", meta1.Priority)
	assert.Equal(t, "Check existing patterns first", meta1.Notes)

	// Check second task metadata
	require.NotNil(t, tasks[1].Metadata)
	var meta2 TaskMetadata
	err = json.Unmarshal([]byte(*tasks[1].Metadata), &meta2)
	require.NoError(t, err)
	assert.Equal(t, "implementation", meta2.PreferredAgent)
	assert.Equal(t, []string{"use_bash", "test_first"}, meta2.ToolHints)
}

func TestCreatePlan_ParallelWithDependency(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)
	ctx := createTestContext(t, chatID)
	tool := &CreatePlanTool{repo: repo}

	params := CreatePlanParams{
		Title:       "Parallel Tasks Plan",
		Description: "Testing parallel_with dependency type",
		Tasks: []TaskInput{
			{Title: "Setup"},
			{Title: "Frontend work", Dependencies: []TaskDependencyInput{
				{TaskPosition: 1, Type: "blocks"},
			}},
			{Title: "Backend work", Dependencies: []TaskDependencyInput{
				{TaskPosition: 1, Type: "blocks"},
				{TaskPosition: 2, Type: "parallel_with"},
			}},
		},
	}

	resp, err := tool.Execute(ctx, params)
	require.NoError(t, err)

	text := resp.Content
	assert.Contains(t, text, "Dependencies (3 created)")
	assert.Contains(t, text, "Setup blocks Frontend work")
	assert.Contains(t, text, "Setup blocks Backend work")
	assert.Contains(t, text, "Frontend work parallel_with Backend work")

	// Verify in DB
	plan, err := repo.GetPlanByThreadID(ctx.Context, chatID)
	require.NoError(t, err)
	deps, err := repo.ListDependenciesByPlan(ctx.Context, plan.ID)
	require.NoError(t, err)
	assert.Len(t, deps, 3)

	// Check parallel_with exists
	hasParallel := false
	for _, d := range deps {
		if d.DependencyType == core.DependencyTypeParallelWith {
			hasParallel = true
		}
	}
	assert.True(t, hasParallel)
}

func TestCreatePlan_MixedValidAndInvalidDeps(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)
	ctx := createTestContext(t, chatID)
	tool := &CreatePlanTool{repo: repo}

	params := CreatePlanParams{
		Title:       "Mixed Deps Test",
		Description: "Some valid, some invalid",
		Tasks: []TaskInput{
			{Title: "Task A"},
			{Title: "Task B", Dependencies: []TaskDependencyInput{
				{TaskPosition: 1, Type: "blocks"},   // valid
				{TaskPosition: 50, Type: "blocks"},  // invalid: out of range
				{TaskPosition: 2, Type: "blocks"},   // invalid: self-reference
				{TaskPosition: 1, Type: "bad_type"}, // invalid: bad type
			}},
		},
	}

	resp, err := tool.Execute(ctx, params)
	require.NoError(t, err)

	text := resp.Content
	// Should have 1 valid dependency
	assert.Contains(t, text, "Dependencies (1 created)")
	assert.Contains(t, text, "Task A blocks Task B")

	// Should have 3 warnings
	assert.Contains(t, text, "Dependency warnings (3)")
	assert.True(t, strings.Contains(text, "out of range") || strings.Contains(text, "task_position 50"))
	assert.Contains(t, text, "cannot depend on itself")
	assert.Contains(t, text, "invalid dependency type")
}
