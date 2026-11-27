//go:build !linux

package worktreereclaim

import "testing"

// requireOpenat2 is Linux-only: elsewhere the safe inspection refuses by
// design, and skipWithoutSafeInspection skips on that refusal.
func requireOpenat2(testing.TB) {}
