// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// TestCancelScopeCancelsNewestFirst pins the order the determinism rests on:
// explicit, newest first, so a grandchild is always cancelled before the child
// whose SDK children-map would otherwise cancel it in random order.
func TestCancelScopeCancelsNewestFirst(t *testing.T) {
	var order []int
	s := &cancelScope{}
	for i := range 4 {
		s.entries = append(s.entries, scopedCancel{cancel: func() { order = append(order, i) }})
	}
	s.cancelChildren()
	assert.Equal(t, []int{3, 2, 1, 0}, order)
	assert.Empty(t, s.entries, "a cancelled scope must not hold on to its children")
}

type cancelScopeResult struct {
	Live          int
	AllCancelled  bool
	ScopeCanceled bool
}

// cancelScopeWorkflow builds children, a grandchild and an already-cancelled
// child under one scope, then cancels the scope.
func cancelScopeWorkflow(ctx workflow.Context) (cancelScopeResult, error) {
	scopeCtx, cancelAll := newCancelScope(ctx)
	scope := scopeCtx.Value(cancelScopeKey{}).(*cancelScope)

	a, _ := withScopedCancel(scopeCtx)
	b, _ := withScopedCancel(scopeCtx)
	grandchild, _ := withScopedCancel(a)
	c, cancelC := withScopedCancel(scopeCtx)
	cancelC()                          // an interrupt already cancelled c
	d, _ := withScopedCancel(scopeCtx) // adding d prunes the cancelled c

	live := len(scope.entries)
	cancelAll()

	all := true
	for _, x := range []workflow.Context{a, b, grandchild, c, d} {
		if x.Err() == nil {
			all = false
		}
	}
	return cancelScopeResult{Live: live, AllCancelled: all, ScopeCanceled: scopeCtx.Err() != nil}, nil
}

func TestCancelScopeCancelsEveryDescendantAndPrunesCancelledChildren(t *testing.T) {
	var ts temporaltest.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.ExecuteWorkflow(cancelScopeWorkflow)
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var res cancelScopeResult
	require.NoError(t, env.GetWorkflowResult(&res))
	// a, b, grandchild, d — c was cancelled before d was added.
	assert.Equal(t, 4, res.Live)
	assert.True(t, res.AllCancelled)
	assert.True(t, res.ScopeCanceled)
}

// withScopedCancel outside any scope is plain workflow.WithCancel: the timer
// contexts in approval/question flows may run under no pause coordinator.
func TestWithScopedCancelOutsideAScope(t *testing.T) {
	var ts temporaltest.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.ExecuteWorkflow(func(ctx workflow.Context) (bool, error) {
		child, cancel := withScopedCancel(ctx)
		cancel()
		return child.Err() != nil, nil
	})
	require.NoError(t, env.GetWorkflowError())
	var cancelled bool
	require.NoError(t, env.GetWorkflowResult(&cancelled))
	assert.True(t, cancelled)
}
