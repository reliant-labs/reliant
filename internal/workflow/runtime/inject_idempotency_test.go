// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The inject seed is the message that tells a freshly forked sub-agent what to
// do. It must be written exactly once per logical injection, no matter how many
// Temporal runs it takes to get through the workflow.
//
// It was not. SaveMessage's idempotency key was scoped to the Temporal RunID,
// and a resumed run reuses the workflow ID but gets a NEW RunID — so the seed
// computed a different key, missed the dedup, and landed twice. In production
// (chat 4d92f694) an implementer thread was told to start "Attempt 1 of 4"
// twice: the same bytes at seq 37 and again at seq 111, half an hour later,
// after the user paused and resumed. The agent restarted work it had already
// started.
//
// These tests pin the key that fixes it. The first is the regression; the rest
// are the other half of the contract — a key that dedupes too eagerly would
// silently swallow injections that genuinely differ, which is worse than the
// duplicate, because a sub-agent that is never told what to do just sits there.

// injectOpts builds the opts for one injection frame. Only the fields the key
// reads are set; the rest do not participate.
func injectOpts(workflowID, threadID, nodeID string, iteration *int64) ChildWorkflowInitOpts {
	return ChildWorkflowInitOpts{
		ChildWorkflowID: workflowID,
		ChildThreadID:   threadID,
		SpawnedByNodeID: nodeID,
		LoopIteration:   iteration,
	}
}

func iter(n int64) *int64 { return &n }

// THE REGRESSION. Resume reuses the workflow ID and mints a new RunID
// (chat_send.go starts a fresh execution against the existing workflowID), so
// the key must not vary with anything the run owns. Here the same logical frame
// is keyed twice, as the original run and its resume would.
func TestInjectKey_IsStableAcrossRuns(t *testing.T) {
	t.Parallel()
	frame := func() string {
		return injectIdempotencyKey(injectOpts("wf-root", "thread-impl", "implement", iter(0)))
	}

	first, second := frame(), frame()

	require.NotEmpty(t, first)
	assert.Equal(t, first, second,
		"the same injection frame must key identically across runs; if it does not, "+
			"a resumed run re-seeds the child and tells it to restart work in flight")

	// And the thing that broke it: no component may carry a run identity.
	// A RunID is exactly what used to be in here.
	assert.NotContains(t, first, "run",
		"the key must contain no run-scoped component — that is the whole bug")
}

// The negative half, and the one the brief called out as non-optional: a loop
// frame at iteration 0 is NOT the same frame as one with no loop at all.
// ForChild resolves them to different threads, so collapsing them here would
// suppress a legitimate injection.
func TestInjectKey_LoopPresenceIsDistinctFromIterationZero(t *testing.T) {
	t.Parallel()
	inLoop := injectIdempotencyKey(injectOpts("wf-root", "thread-a", "implement", iter(0)))
	noLoop := injectIdempotencyKey(injectOpts("wf-root", "thread-a", "implement", nil))

	assert.NotEqual(t, inLoop, noLoop,
		"a loop frame at iteration 0 and a non-loop frame are different frames; "+
			"keying them the same would drop one of the two injections")
}

// Each iteration of a retry loop is a genuinely new instruction ("Attempt 2 of
// 4"), so each must inject.
func TestInjectKey_EachIterationInjects(t *testing.T) {
	t.Parallel()
	seen := map[string]int{}
	for i := int64(0); i < 4; i++ {
		seen[injectIdempotencyKey(injectOpts("wf-root", "thread-a", "implement", iter(i)))]++
	}
	assert.Len(t, seen, 4,
		"every loop iteration must produce its own key, or attempts 2..4 are never "+
			"delivered and the agent waits forever on an instruction that was deduped away")
}

// Two different nodes seeding two different children must not share a key.
func TestInjectKey_DistinctNodesAndThreads(t *testing.T) {
	t.Parallel()
	base := injectIdempotencyKey(injectOpts("wf-root", "thread-a", "implement", iter(1)))

	assert.NotEqual(t, base,
		injectIdempotencyKey(injectOpts("wf-root", "thread-a", "review", iter(1))),
		"different graph nodes are different injections")
	assert.NotEqual(t, base,
		injectIdempotencyKey(injectOpts("wf-root", "thread-b", "implement", iter(1))),
		"different child threads are different injections")
	assert.NotEqual(t, base,
		injectIdempotencyKey(injectOpts("wf-branch", "thread-a", "implement", iter(1))),
		"a branched chat mints a new workflow ID and must not reuse its source's keys")
}

// WHY `memo` NEEDS NO SEPARATE KEY COMPONENT.
//
// The brief required memo in the key on the grounds that it is not derivable
// from the rest. That is true of (nodeID, iteration) alone — but the key also
// carries childThreadID, and the thread ID is DERIVED by ForChild from a string
// that includes the iteration only when memo is false. So memo is already in
// the key, transitively and exactly.
//
// This test is the load-bearing evidence for that claim, and the tripwire if it
// ever stops holding: if ForChild's derivation changes so that two frames
// differing only in memo can resolve the SAME thread, this fails and memo must
// become an explicit component of injectIdempotencyKey.
func TestInjectKey_MemoIsCapturedViaThreadIdentity(t *testing.T) {
	threadFor := func(iteration int, memo bool) string {
		root := NewExecutionContext("wf-root", "chat-1", "wf", "wf-root")
		root.Loop = &ExecLoopContext{Iteration: iteration}
		return root.ForChild("implement", model.ThreadModeFork, "agent", memo).Thread
	}

	// The premise: memo changes which thread the frame resolves to.
	require.NotEqual(t, threadFor(1, false), threadFor(1, true),
		"ForChild must fold memo into thread identity; if this ever stops being "+
			"true, injectIdempotencyKey needs memo as an explicit component")

	// Therefore the keys differ too, without the key naming memo at all.
	nonMemo := injectIdempotencyKey(injectOpts("wf-root", threadFor(1, false), "implement", iter(1)))
	memoized := injectIdempotencyKey(injectOpts("wf-root", threadFor(1, true), "implement", iter(1)))
	assert.NotEqual(t, nonMemo, memoized,
		"frames differing only in memo must key differently")

	// The other half of memo's contract: a memoized fork deliberately collapses
	// to ONE thread across iterations, so its injections must also collapse —
	// the same seed, not one per iteration.
	memoIter1 := threadFor(1, true)
	memoIter2 := threadFor(2, true)
	require.Equal(t, memoIter1, memoIter2, "memoized forks share one thread")
}

// A guard on the shape rather than the value: the key is built by
// concatenating components, so a component that is empty or that contains the
// separator could let two different frames render the same string.
func TestInjectKey_ComponentsCannotCollideThroughFormatting(t *testing.T) {
	t.Parallel()
	// A node ID containing the separator must not be able to impersonate a
	// different (thread, node) split.
	a := injectIdempotencyKey(injectOpts("wf", "thread|implement", "review", iter(0)))
	b := injectIdempotencyKey(injectOpts("wf", "thread", "implement|review", iter(0)))
	assert.NotEqual(t, a, b,
		"components must not be able to bleed across the separator")

	// The loop component is spelled, not numeric, so "no loop" cannot be
	// confused with any iteration number.
	assert.Contains(t, injectIdempotencyKey(injectOpts("wf", "t", "n", nil)), "noloop")
	assert.Contains(t, injectIdempotencyKey(injectOpts("wf", "t", "n", iter(3))), fmt.Sprintf("iter:%d", 3))
}
