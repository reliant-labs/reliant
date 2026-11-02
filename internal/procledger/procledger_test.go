// Copyright (c) 2026 Reliant Labs
package procledger

import "testing"

// A late Forget for a reaped process must not drop the entry of the process
// that was given its pid next.
func TestForgetOnlyTheProcessThatStartedThen(t *testing.T) {
	const pid = 4990001
	Record(Process{Pid: pid, Pgid: pid, Start: 2, Paths: []string{"/new"}})
	t.Cleanup(func() { Forget(pid, 2) })
	Forget(pid, 1)
	p, ok := Lookup(pid)
	if !ok || p.Start != 2 {
		t.Fatalf("a stale Forget dropped the live entry: %+v %v", p, ok)
	}
	p.Paths[0] = "/mutated"
	if q, _ := Lookup(pid); q.Paths[0] != "/new" {
		t.Error("Lookup must hand out a copy")
	}
	Forget(pid, 2)
	if _, ok := Lookup(pid); ok {
		t.Error("Forget with the matching start time must drop the entry")
	}
}
