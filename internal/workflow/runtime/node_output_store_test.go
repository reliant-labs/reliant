// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNodeOutputStore_Basic(t *testing.T) {
	t.Parallel()
	store := NewNodeOutputStore("test-workflow")

	// Initially empty
	assert.Equal(t, 0, store.Count())
	assert.False(t, store.Has("step1"))

	// Set and get
	store.Set("step1", map[string]interface{}{"key": "value"})
	assert.True(t, store.Has("step1"))
	assert.Equal(t, 1, store.Count())

	output, exists := store.Get("step1")
	assert.True(t, exists)
	assert.NotNil(t, output)

	// Non-existent step
	_, exists = store.Get("nonexistent")
	assert.False(t, exists)
}

func TestNodeOutputStore_NilOutput(t *testing.T) {
	t.Parallel()
	store := NewNodeOutputStore("test-workflow")

	// Setting nil output should still mark step as having output
	store.Set("step1", nil)
	assert.True(t, store.Has("step1"))

	output, exists := store.Get("step1")
	assert.True(t, exists)
	assert.Nil(t, output)
}

func TestNodeOutputStore_FromExistingMap(t *testing.T) {
	t.Parallel()
	existingData := map[string]interface{}{
		"step1": map[string]interface{}{"result": "ok"},
		"step2": "simple string",
	}

	store := NewNodeOutputStoreFrom(existingData, "test-workflow")

	assert.Equal(t, 2, store.Count())
	assert.True(t, store.Has("step1"))
	assert.True(t, store.Has("step2"))

	// Modifying store should modify original map (no copy)
	store.Set("step3", "new")
	assert.Contains(t, existingData, "step3")
}

func TestNodeOutputStore_FromNilMap(t *testing.T) {
	t.Parallel()
	store := NewNodeOutputStoreFrom(nil, "test-workflow")
	assert.NotNil(t, store)
	assert.Equal(t, 0, store.Count())
}

func TestNodeOutputStore_Keys(t *testing.T) {
	t.Parallel()
	store := NewNodeOutputStore("test-workflow")
	store.Set("step_a", "a")
	store.Set("step_b", "b")
	store.Set("step_c", "c")

	keys := store.Keys()
	assert.Len(t, keys, 3)
	assert.Contains(t, keys, "step_a")
	assert.Contains(t, keys, "step_b")
	assert.Contains(t, keys, "step_c")
}

func TestNodeOutputStore_AsMap(t *testing.T) {
	t.Parallel()
	store := NewNodeOutputStore("test-workflow")
	store.Set("step1", "value1")
	store.Set("step2", "value2")

	m := store.AsMap()
	assert.Len(t, m, 2)
	assert.Equal(t, "value1", m["step1"])
	assert.Equal(t, "value2", m["step2"])

	// AsMap returns the same underlying map
	m["step3"] = "value3"
	assert.True(t, store.Has("step3"))
}

func TestNodeOutputStore_Clone(t *testing.T) {
	t.Parallel()
	store := NewNodeOutputStore("test-workflow")
	store.Set("step1", map[string]interface{}{"key": "value"})

	clone := store.Clone()

	// Clone has same data
	assert.True(t, clone.Has("step1"))

	// Modifying clone doesn't affect original
	clone.Set("step2", "new")
	assert.False(t, store.Has("step2"))
	assert.True(t, clone.Has("step2"))
}

func TestNodeOutputStore_Merge(t *testing.T) {
	t.Parallel()
	store1 := NewNodeOutputStore("test-workflow")
	store1.Set("step1", "value1")

	store2 := NewNodeOutputStore("other-workflow")
	store2.Set("step2", "value2")
	store2.Set("step1", "overwritten") // Overwrites step1

	store1.Merge(store2)

	assert.Equal(t, 2, store1.Count())
	output1, _ := store1.Get("step1")
	assert.Equal(t, "overwritten", output1)
	assert.True(t, store1.Has("step2"))
}

func TestNodeOutputStore_MergeNil(t *testing.T) {
	t.Parallel()
	store := NewNodeOutputStore("test-workflow")
	store.Set("step1", "value1")

	// Should not panic
	store.Merge(nil)
	assert.Equal(t, 1, store.Count())
}

func TestNodeOutputStore_MergeMap(t *testing.T) {
	t.Parallel()
	store := NewNodeOutputStore("test-workflow")
	store.Set("step1", "value1")

	store.MergeMap(map[string]interface{}{
		"step2": "value2",
		"step3": "value3",
	})

	assert.Equal(t, 3, store.Count())
}

func TestNodeOutputStore_Clear(t *testing.T) {
	t.Parallel()
	store := NewNodeOutputStore("test-workflow")
	store.Set("step1", "value1")
	store.Set("step2", "value2")

	store.Clear()

	assert.Equal(t, 0, store.Count())
	assert.False(t, store.Has("step1"))
}

func TestNodeOutputStore_GetMap(t *testing.T) {
	t.Parallel()
	store := NewNodeOutputStore("test-workflow")

	// Map output
	store.Set("step1", map[string]interface{}{"result": "ok"})
	m := store.GetMap("step1")
	require.NotNil(t, m)
	assert.Equal(t, "ok", m["result"])

	// Non-map output
	store.Set("step2", "string value")
	assert.Nil(t, store.GetMap("step2"))

	// Non-existent step
	assert.Nil(t, store.GetMap("nonexistent"))
}

func TestNodeOutputStore_GetString(t *testing.T) {
	t.Parallel()
	store := NewNodeOutputStore("test-workflow")
	store.Set("step1", map[string]interface{}{
		"message": "hello",
		"count":   42,
	})

	// String field
	assert.Equal(t, "hello", store.GetString("step1", "message"))

	// Non-string field
	assert.Equal(t, "", store.GetString("step1", "count"))

	// Non-existent field
	assert.Equal(t, "", store.GetString("step1", "nonexistent"))

	// Non-existent step
	assert.Equal(t, "", store.GetString("nonexistent", "message"))
}

func TestNodeOutputStore_GetBool(t *testing.T) {
	t.Parallel()
	store := NewNodeOutputStore("test-workflow")
	store.Set("step1", map[string]interface{}{
		"success": true,
		"failed":  false,
		"count":   42,
	})

	assert.True(t, store.GetBool("step1", "success"))
	assert.False(t, store.GetBool("step1", "failed"))
	assert.False(t, store.GetBool("step1", "count"))
	assert.False(t, store.GetBool("step1", "nonexistent"))
	assert.False(t, store.GetBool("nonexistent", "success"))
}

func TestNodeOutputStore_GetInt(t *testing.T) {
	t.Parallel()
	store := NewNodeOutputStore("test-workflow")
	store.Set("step1", map[string]interface{}{
		"count":     42,
		"count64":   int64(100),
		"countF":    float64(3.14),
		"notNumber": "string",
	})

	assert.Equal(t, 42, store.GetInt("step1", "count"))
	assert.Equal(t, 100, store.GetInt("step1", "count64"))
	assert.Equal(t, 3, store.GetInt("step1", "countF"))
	assert.Equal(t, 0, store.GetInt("step1", "notNumber"))
	assert.Equal(t, 0, store.GetInt("step1", "nonexistent"))
	assert.Equal(t, 0, store.GetInt("nonexistent", "count"))
}

func TestNodeOutputStore_EnableDebug(t *testing.T) {
	t.Parallel()
	store := NewNodeOutputStore("test-workflow").EnableDebug()

	// Just verify it doesn't panic
	store.Set("step1", "value")
	store.Get("step1")
	assert.True(t, store.Has("step1"))
}
