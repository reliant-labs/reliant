package cli

import (
	"testing"
	"time"
)

// freeAfter returns a probe reporting each port busy for the first n calls to
// that port, then free — modelling a descendant that closes its listener
// shortly after the teardown signal.
func freeAfter(n int) (func(int) bool, *int) {
	calls := 0
	return func(int) bool {
		calls++
		return calls <= n
	}, &calls
}

// The regression: a port that frees shortly after teardown must NOT be
// reported as a conflict.
func TestAwaitPortReleaseClearsSlowListener(t *testing.T) {
	probe, _ := freeAfter(2)
	conflicts := []portConflict{{name: "admin-server", port: 8090}}
	slept := 0
	got := awaitPortRelease(conflicts, probe, time.Second, 10*time.Millisecond,
		func(time.Duration) { slept++ })
	if len(got) != 0 {
		t.Fatalf("expected the port to clear within the grace, got %v", got)
	}
	if slept == 0 {
		t.Error("expected the loop to wait between probes")
	}
}

// A port held for real must still be reported once the grace expires.
func TestAwaitPortReleaseKeepsGenuineConflict(t *testing.T) {
	alwaysBusy := func(int) bool { return true }
	conflicts := []portConflict{{name: "admin-server", port: 8090}}
	got := awaitPortRelease(conflicts, alwaysBusy, 30*time.Millisecond, 10*time.Millisecond,
		func(time.Duration) {})
	if len(got) != 1 || got[0].port != 8090 {
		t.Fatalf("a genuinely held port must survive the grace, got %v", got)
	}
}

// An already-free port returns immediately without sleeping: the happy path
// must not pay the grace.
func TestAwaitPortReleaseFastPath(t *testing.T) {
	slept := 0
	got := awaitPortRelease([]portConflict{{name: "a", port: 1}},
		func(int) bool { return false }, time.Minute, time.Second,
		func(time.Duration) { slept++ })
	if len(got) != 0 {
		t.Fatalf("expected no conflicts, got %v", got)
	}
	if slept != 0 {
		t.Errorf("must not sleep when the port is already free, slept %d times", slept)
	}
}

// The set NARROWS: ports that free drop out while the genuinely held one
// remains, so the final report names only what is actually still bound.
func TestAwaitPortReleaseNarrowsSet(t *testing.T) {
	// 8090 frees on the second round; 5173 never does.
	round := map[int]int{}
	probe := func(port int) bool {
		round[port]++
		if port == 8090 {
			return round[port] <= 1
		}
		return true
	}
	conflicts := []portConflict{{name: "admin-server", port: 8090}, {name: "web", port: 5173}}
	got := awaitPortRelease(conflicts, probe, 50*time.Millisecond, 10*time.Millisecond,
		func(time.Duration) {})
	if len(got) != 1 || got[0].port != 5173 {
		t.Fatalf("expected only the genuinely held port, got %v", got)
	}
	// Once 8090 dropped out it must not be probed again — each probe is a
	// real dial timeout in production.
	if round[8090] != 2 {
		t.Errorf("freed port should stop being probed, got %d probes", round[8090])
	}
}

// A zero grace still probes once, so the function is never a no-op.
func TestAwaitPortReleaseZeroGraceProbesOnce(t *testing.T) {
	calls := 0
	got := awaitPortRelease([]portConflict{{name: "a", port: 1}},
		func(int) bool { calls++; return true }, 0, 10*time.Millisecond,
		func(time.Duration) { t.Error("must not sleep with a zero grace") })
	if calls != 1 {
		t.Errorf("expected exactly one probe, got %d", calls)
	}
	if len(got) != 1 {
		t.Errorf("expected the conflict preserved, got %v", got)
	}
}
