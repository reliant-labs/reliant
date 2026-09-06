// Copyright (c) 2025 Reliant Labs
package runtime

import "testing"

// joinNodePath must COMPOSE. The defect it replaces kept the outermost prefix
// and discarded everything between, which made a node two levels down
// indistinguishable from the same-named node one level down.
func TestJoinNodePath(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		nodeID string
		want   string
	}{
		{"top level has no prefix", "", "review", "review"},
		{"one level", "impl_loop", "review", "impl_loop.review"},
		{"two levels compose, they do not replace", "impl_loop.attempt", "review", "impl_loop.attempt.review"},
		{"three levels", "outer.stage.inner_loop", "work", "outer.stage.inner_loop.work"},
		{"empty node id yields the prefix", "impl_loop", "", "impl_loop"},
		{"both empty", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := joinNodePath(tc.prefix, tc.nodeID); got != tc.want {
				t.Errorf("joinNodePath(%q, %q) = %q, want %q", tc.prefix, tc.nodeID, got, tc.want)
			}
		})
	}
}

// Composing repeatedly is how the executors build a path: each nesting boundary
// appends exactly one segment to what it was handed.
func TestJoinNodePath_RepeatedCompositionAccumulates(t *testing.T) {
	path := joinNodePath("", "impl_loop")
	path = joinNodePath(path, "attempt")
	path = joinNodePath(path, "inner_loop")
	path = joinNodePath(path, "review")

	const want = "impl_loop.attempt.inner_loop.review"
	if path != want {
		t.Errorf("accumulated path = %q, want %q", path, want)
	}
}
