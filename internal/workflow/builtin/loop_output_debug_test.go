package builtin_test

import (
	"testing"
)

func TestLoopOutputPropagation(t *testing.T) {
	// DISABLED: This test was for the old one-ring workflow structure that had
	// a one_ring_loop node with nested review loop. The workflow has been restructured
	// to a linear flow with only implement_loop, so this test no longer applies.
	//
	// The test was looking for a "one_ring_loop" node that has been renamed to
	// "implement_loop", and the workflow no longer has nested loops for review.
	t.Skip("Test disabled - workflow structure changed, one_ring_loop node no longer exists")
}
