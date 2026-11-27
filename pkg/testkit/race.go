package testkit

import (
	"strings"
	"testing"
)

// RaceEnabled reports whether this binary was built with the race detector
// (go test -race, which scaffolded task test uses by default). Use it to run
// a smaller workload under -race without skipping the test entirely.
func RaceEnabled() bool { return raceEnabled }

// SkipUnderRace skips t when the binary was built with -race. why must name
// what makes race detection unnecessary for this test, even on a non-race run.
// Use it for work such as a large, single-goroutine simulation, not a test of
// concurrent code. Unlike [Slow], it does not skip tests under -short alone.
func SkipUnderRace(t testing.TB, why string) {
	t.Helper()
	if strings.TrimSpace(why) == "" {
		t.Fatalf("testkit.SkipUnderRace: why is empty; explain why this test does not need -race")
	}
	if raceEnabled {
		t.Skip("skipped under -race: " + why + "; runs without -race (go test, task test:short)")
	}
}
