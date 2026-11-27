package testkit

import (
	"runtime/debug"
	"strings"
	"testing"
)

// builtWithRace reads the race setting from the binary's own build info,
// which the toolchain records independently of the build-tag files behind
// RaceEnabled, so the two can be checked against each other.
func builtWithRace(t *testing.T) bool {
	t.Helper()
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("this test binary carries no build info")
	}
	for _, s := range info.Settings {
		if s.Key == "-race" {
			return s.Value == "true"
		}
	}
	return false
}

func TestRaceEnabledMatchesTheBuild(t *testing.T) {
	if got, want := RaceEnabled(), builtWithRace(t); got != want {
		t.Fatalf("RaceEnabled() = %v, but the binary's build info says -race=%v", got, want)
	}
}

func TestSkipUnderRaceSkipsOnlyARaceBuild(t *testing.T) {
	rec := &recordingTB{TB: t}
	SkipUnderRace(rec, "single-goroutine simulator")
	if rec.fatal != "" {
		t.Fatalf("SkipUnderRace failed a valid call: %s", rec.fatal)
	}
	if !builtWithRace(t) {
		if rec.skipped != "" {
			t.Fatalf("SkipUnderRace skipped a build without -race: %q", rec.skipped)
		}
		return
	}
	for _, want := range []string{"-race", "single-goroutine simulator", "task test:short"} {
		if !strings.Contains(rec.skipped, want) {
			t.Errorf("skip message %q does not mention %q", rec.skipped, want)
		}
	}
}

func TestSkipUnderRaceRequiresAReason(t *testing.T) {
	rec := &recordingTB{TB: t}
	SkipUnderRace(rec, "")
	if !strings.Contains(rec.fatal, "why is empty") {
		t.Fatalf("SkipUnderRace(\"\") = fatal %q, skipped %q; want a fatal naming the empty why", rec.fatal, rec.skipped)
	}
}
