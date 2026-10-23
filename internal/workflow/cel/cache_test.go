// Copyright (c) 2025 Reliant Labs
package wfcel

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewEnvIsBuiltOncePerConfig pins the environment cache: the same config
// (namespaces in the same order, same feature flags) gets the same *cel.Env,
// and a different one does not.
func TestNewEnvIsBuiltOncePerConfig(t *testing.T) {
	t.Parallel()
	a, err := NewEnv(DefaultCELEnvConfig())
	require.NoError(t, err)
	b, err := NewEnv(DefaultCELEnvConfig())
	require.NoError(t, err)
	assert.Same(t, a, b, "an identical config must reuse the cached environment")

	other, err := NewEnv(SaveMessageCELEnvConfig())
	require.NoError(t, err)
	assert.NotSame(t, a, other, "a different namespace set is a different environment")

	noFuncs := DefaultCELEnvConfig()
	noFuncs.IncludeCustomFunctions = false
	bare, err := NewEnv(noFuncs)
	require.NoError(t, err)
	assert.NotSame(t, a, bare, "the feature flags are part of the key")
	_, issues := bare.Compile("coalesce(inputs.a, 1)")
	assert.Error(t, issues.Err(), "an environment without custom functions must not see them")
}

// TestCompileCachesProgramsAndRefusesBadExpressions pins the program cache:
// a hit is the same compiled program, and a compile error comes back as
// issues every time (not cached as a success).
func TestCompileCachesProgramsAndRefusesBadExpressions(t *testing.T) {
	t.Parallel()
	cfg := DefaultCELEnvConfig()
	first, issues, err := Compile(cfg, "inputs.n + 1")
	require.NoError(t, err)
	require.Nil(t, issues)
	second, _, err := Compile(cfg, "inputs.n + 1")
	require.NoError(t, err)
	assert.Same(t, first, second)

	for range 2 {
		compiled, issues, err := Compile(cfg, "inputs.n +")
		require.NoError(t, err)
		assert.Nil(t, compiled)
		require.NotNil(t, issues)
		assert.Error(t, issues.Err())
	}
}

// TestCompiledProgramsAreSafeForConcurrentUse evaluates shared cached
// programs from many goroutines with different activations. Run under -race
// it proves the cached Env/Program carry no per-evaluation state; without it
// it still proves each goroutine sees its own activation's answer.
func TestCompiledProgramsAreSafeForConcurrentUse(t *testing.T) {
	t.Parallel()
	exprs := []string{
		"inputs.n * 2",
		"size(inputs.list) + inputs.n",
		"coalesce(inputs.?missing.orValue(null), inputs.n)",
		"inputs.list.map(x, x + inputs.n).size()",
	}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for g := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 50 {
				n := int64(g*1000 + i)
				ctx := &NodeResolutionContext{Inputs: map[string]interface{}{"n": n, "list": []interface{}{int64(1), int64(2)}}}
				expr := exprs[(g+i)%len(exprs)]
				got, err := EvaluateValue(expr, ctx)
				if err != nil {
					errs <- fmt.Errorf("%s: %w", expr, err)
					return
				}
				want := map[string]int64{
					exprs[0]: n * 2,
					exprs[1]: 2 + n,
					exprs[2]: n,
					exprs[3]: 2,
				}[expr]
				if got != want {
					errs <- fmt.Errorf("%s with n=%d: got %v, want %d", expr, n, got, want)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestLRUIsBounded pins the eviction order: the least recently used entry
// goes first, and a get refreshes recency.
func TestLRUIsBounded(t *testing.T) {
	t.Parallel()
	c := newLRU[int](2)
	c.add("a", 1)
	c.add("b", 2)
	_, ok := c.get("a") // a is now the most recent
	require.True(t, ok)
	c.add("c", 3) // evicts b
	assert.Equal(t, 2, c.len())
	_, ok = c.get("b")
	assert.False(t, ok, "the least recently used entry is evicted")
	v, ok := c.get("a")
	assert.True(t, ok)
	assert.Equal(t, 1, v)
	c.add("a", 10) // replace in place
	v, _ = c.get("a")
	assert.Equal(t, 10, v)
	assert.Equal(t, 2, c.len())
}

// BenchmarkEvaluateValue measures one wfcel evaluation (edge conditions,
// declared outputs and node config all come through here).
func BenchmarkEvaluateValue(b *testing.B) {
	ctx := &NodeResolutionContext{Inputs: map[string]interface{}{"n": int64(3), "list": []interface{}{int64(1), int64(2)}}}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := EvaluateValue("size(inputs.list) + inputs.n", ctx); err != nil {
			b.Fatal(err)
		}
	}
}
