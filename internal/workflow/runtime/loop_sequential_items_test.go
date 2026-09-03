package runtime

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildIterCtx_WithResolvedItems(t *testing.T) {
	t.Parallel()
	executor := &InlineLoopExecutor{
		iteration: 1,
		resolvedItems: []interface{}{
			map[string]interface{}{"name": "wave0"},
			map[string]interface{}{"name": "wave1"},
			map[string]interface{}{"name": "wave2"},
		},
		resolvedKeys: []string{"key0", "key1", "key2"},
	}

	ctx := executor.buildIterCtx()

	assert.Equal(t, 1, ctx["iteration"])
	assert.Equal(t, 1, ctx["index"])
	assert.Equal(t, map[string]interface{}{"name": "wave1"}, ctx["item"])
	assert.Equal(t, "key1", ctx["key"])
}

func TestBuildIterCtx_WithoutResolvedItems(t *testing.T) {
	t.Parallel()
	executor := &InlineLoopExecutor{
		iteration:     3,
		resolvedItems: nil,
		resolvedKeys:  nil,
	}

	ctx := executor.buildIterCtx()

	assert.Equal(t, 3, ctx["iteration"])
	assert.Equal(t, 3, ctx["index"])
	_, hasItem := ctx["item"]
	assert.False(t, hasItem, "item should not be present when resolvedItems is nil")
	_, hasKey := ctx["key"]
	assert.False(t, hasKey, "key should not be present when resolvedItems is nil")
}

func TestBuildIterCtx_BoundsCheck(t *testing.T) {
	t.Parallel()
	executor := &InlineLoopExecutor{
		iteration: 2,
		resolvedItems: []interface{}{
			"only-one-item",
		},
		resolvedKeys: []string{"k0"},
	}

	// iteration (2) >= len(resolvedItems) (1), should fallback to basic context
	ctx := executor.buildIterCtx()

	assert.Equal(t, 2, ctx["iteration"])
	assert.Equal(t, 2, ctx["index"])
	_, hasItem := ctx["item"]
	assert.False(t, hasItem, "item should not be present when iteration is out of bounds")
	_, hasKey := ctx["key"]
	assert.False(t, hasKey, "key should not be present when iteration is out of bounds")
}

func TestBuildIterCtx_FirstItem(t *testing.T) {
	t.Parallel()
	executor := &InlineLoopExecutor{
		iteration:     0,
		resolvedItems: []interface{}{"alpha", "beta"},
		resolvedKeys:  []string{"a", "b"},
	}

	ctx := executor.buildIterCtx()

	assert.Equal(t, 0, ctx["iteration"])
	assert.Equal(t, 0, ctx["index"])
	assert.Equal(t, "alpha", ctx["item"])
	assert.Equal(t, "a", ctx["key"])
}

func TestBuildIterCtx_MapValueUnwrap(t *testing.T) {
	t.Parallel()
	// resolveIterItem unwraps _map_value from map items
	executor := &InlineLoopExecutor{
		iteration: 0,
		resolvedItems: []interface{}{
			map[string]interface{}{"_map_value": "actual_value", "_map_key": "mk"},
		},
		resolvedKeys: []string{"mk"},
	}

	ctx := executor.buildIterCtx()

	// resolveIterItem should unwrap _map_value
	assert.Equal(t, "actual_value", ctx["item"])
	assert.Equal(t, "mk", ctx["key"])
}

func TestBuildIterContextModel_WithResolvedItems(t *testing.T) {
	t.Parallel()
	executor := &InlineLoopExecutor{
		iteration: 1,
		resolvedItems: []interface{}{
			map[string]interface{}{"task": "build"},
			map[string]interface{}{"task": "test"},
		},
		resolvedKeys: []string{"step1", "step2"},
	}

	ic := executor.buildIterContextModel()

	require.NotNil(t, ic)
	assert.Equal(t, 1, ic.Iteration)
	assert.Equal(t, 1, ic.Index)
	assert.Equal(t, map[string]interface{}{"task": "test"}, ic.Item)
	assert.Equal(t, "step2", ic.Key)
}

func TestBuildIterContextModel_WithoutResolvedItems(t *testing.T) {
	t.Parallel()
	executor := &InlineLoopExecutor{
		iteration:     5,
		resolvedItems: nil,
		resolvedKeys:  nil,
	}

	ic := executor.buildIterContextModel()

	require.NotNil(t, ic)
	assert.Equal(t, 5, ic.Iteration)
	assert.Equal(t, 5, ic.Index)
	assert.Nil(t, ic.Item, "Item should be nil when resolvedItems is nil")
	assert.Empty(t, ic.Key, "Key should be empty when resolvedItems is nil")
}

func TestBuildIterContextModel_BoundsCheck(t *testing.T) {
	t.Parallel()
	executor := &InlineLoopExecutor{
		iteration:     3,
		resolvedItems: []interface{}{"a", "b"},
		resolvedKeys:  []string{"k0", "k1"},
	}

	// iteration (3) >= len(resolvedItems) (2), should not populate item/key
	ic := executor.buildIterContextModel()

	require.NotNil(t, ic)
	assert.Equal(t, 3, ic.Iteration)
	assert.Equal(t, 3, ic.Index)
	assert.Nil(t, ic.Item, "Item should be nil when iteration is out of bounds")
	assert.Empty(t, ic.Key, "Key should be empty when iteration is out of bounds")
}

func TestBuildIterContextModel_MapValueUnwrap(t *testing.T) {
	t.Parallel()
	executor := &InlineLoopExecutor{
		iteration: 0,
		resolvedItems: []interface{}{
			map[string]interface{}{"_map_value": 42, "_map_key": "answer"},
		},
		resolvedKeys: []string{"answer"},
	}

	ic := executor.buildIterContextModel()

	// resolveIterItem should unwrap _map_value
	assert.Equal(t, 42, ic.Item)
	assert.Equal(t, "answer", ic.Key)
}

func TestSequentialLoopAutoStop(t *testing.T) {
	t.Parallel()
	// The auto-stop condition is: resolvedItems != nil && iteration >= len(resolvedItems)
	// We test the condition directly since full loop execution requires Temporal context.

	tests := []struct {
		name          string
		iteration     int
		resolvedItems []interface{}
		shouldStop    bool
	}{
		{
			name:          "stops when iteration equals item count",
			iteration:     3,
			resolvedItems: []interface{}{"a", "b", "c"},
			shouldStop:    true,
		},
		{
			name:          "stops when iteration exceeds item count",
			iteration:     5,
			resolvedItems: []interface{}{"a", "b"},
			shouldStop:    true,
		},
		{
			name:          "continues when items remain",
			iteration:     1,
			resolvedItems: []interface{}{"a", "b", "c"},
			shouldStop:    false,
		},
		{
			name:          "continues at first iteration",
			iteration:     0,
			resolvedItems: []interface{}{"a"},
			shouldStop:    false,
		},
		{
			name:          "no auto-stop when resolvedItems is nil",
			iteration:     100,
			resolvedItems: nil,
			shouldStop:    false,
		},
		{
			name:          "stops immediately for empty items list",
			iteration:     0,
			resolvedItems: []interface{}{},
			shouldStop:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			executor := &InlineLoopExecutor{
				iteration:     tc.iteration,
				resolvedItems: tc.resolvedItems,
			}

			// Replicate the auto-stop condition from executeSequentialLoop
			shouldStop := executor.resolvedItems != nil && executor.iteration >= len(executor.resolvedItems)
			assert.Equal(t, tc.shouldStop, shouldStop)
		})
	}
}

func TestBuildIterCtx_ConsistentWithModel(t *testing.T) {
	t.Parallel()
	// Verify that buildIterCtx and buildIterContextModel produce consistent data
	executor := &InlineLoopExecutor{
		iteration: 1,
		resolvedItems: []interface{}{
			"first",
			map[string]interface{}{"nested": true},
		},
		resolvedKeys: []string{"k0", "k1"},
	}

	ctxMap := executor.buildIterCtx()
	ctxModel := executor.buildIterContextModel()

	assert.Equal(t, ctxMap["iteration"], ctxModel.Iteration)
	assert.Equal(t, ctxMap["index"], ctxModel.Index)
	assert.Equal(t, ctxMap["item"], ctxModel.Item)
	assert.Equal(t, ctxMap["key"], ctxModel.Key)
}

// Verify the model package helpers used by buildIterCtx produce expected shapes.
func TestModelBuildIterContextHelpers(t *testing.T) {
	t.Parallel()
	t.Run("BuildIterContext basic", func(t *testing.T) {
		ctx := model.BuildIterContext(7)
		assert.Equal(t, 7, ctx["iteration"])
		assert.Equal(t, 7, ctx["index"])
		assert.Len(t, ctx, 2, "basic iter context should only have iteration and index")
	})

	t.Run("BuildParallelIterContext with item and key", func(t *testing.T) {
		item := map[string]interface{}{"goal": "ship it"}
		ctx := model.BuildParallelIterContext(2, item, "wave2")
		assert.Equal(t, 2, ctx["iteration"])
		assert.Equal(t, 2, ctx["index"])
		assert.Equal(t, item, ctx["item"])
		assert.Equal(t, "wave2", ctx["key"])
		assert.Len(t, ctx, 4)
	})
}
