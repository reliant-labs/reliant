package testdocker

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// recordingTB is the testing.TB Unavailable sees: it records whether the call
// failed or skipped, then ends the goroutine as a real Fatalf or Skip does
// (runtime.Goexit), so the policy is checked without a docker daemon. The embedded nil TB panics on any method
// Unavailable starts calling that this does not record.
type recordingTB struct {
	testing.TB
	failed, skipped bool
	msg             string
	logs            []string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Name() string { return "TestContainer" }

func (r *recordingTB) Logf(format string, args ...any) {
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.failed = true
	r.msg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func (r *recordingTB) Skip(args ...any) {
	r.skipped = true
	r.msg = fmt.Sprint(args...)
	runtime.Goexit()
}

// TestUnavailableFailsUnderCI pins the fail-versus-skip policy the main-only
// integration lane depends on: under CI a missing docker or image fails the
// test (a skip would turn the lane green while it ran nothing, forge#1870);
// on a developer machine it skips.
func TestUnavailableFailsUnderCI(t *testing.T) {
	for _, tc := range []struct {
		name, ci      string
		failed, skipd bool
	}{
		{"CI set", "true", true, false},
		{"CI unset", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CI", tc.ci)
			t.Setenv("RELIANT_GATE", "")
			r := &recordingTB{}
			done := make(chan struct{})
			go func() {
				defer close(done)
				Unavailable(r, "docker is not available: %s", "no daemon")
			}()
			<-done
			if r.failed != tc.failed || r.skipped != tc.skipd {
				t.Fatalf("Unavailable with CI=%q: failed=%t skipped=%t; want failed=%t skipped=%t", tc.ci, r.failed, r.skipped, tc.failed, tc.skipd)
			}
			if !strings.Contains(r.msg, "docker is not available: no daemon") {
				t.Errorf("Unavailable message %q does not carry the caller's reason", r.msg)
			}
		})
	}
}

// callUnavailable runs Unavailable on a recordingTB until it fails or skips.
func callUnavailable() *recordingTB {
	r := &recordingTB{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Unavailable(r, "docker is not available: %s", "no daemon")
	}()
	<-done
	return r
}

// TestUnavailableSkipsInAGate pins the Reliant gate's contract: a gate
// (RELIANT_GATE=1) has no docker engine, so a container test skips there even
// though CI is set (scripts/gate.sh sets it), with exactly the message the
// gate reports, and appends its name to the stage's skip log, one line per
// skip, which the gate counts into its verdict (reliant's
// gaterun.StageResult.SkippedDockerTests).
func TestUnavailableSkipsInAGate(t *testing.T) {
	skips := filepath.Join(t.TempDir(), "test.gate-run-1")
	t.Setenv("CI", "1")
	t.Setenv("RELIANT_GATE", "1")
	t.Setenv("RELIANT_GATE_DOCKER_SKIPS", skips)
	for range 2 {
		r := callUnavailable()
		if r.failed || !r.skipped {
			t.Fatalf("Unavailable in a gate: failed=%t skipped=%t; want a skip", r.failed, r.skipped)
		}
		if r.msg != "skip: docker unavailable (gate)" {
			t.Fatalf("Unavailable in a gate skipped with %q; want exactly %q", r.msg, "skip: docker unavailable (gate)")
		}
		if !strings.Contains(strings.Join(r.logs, "\n"), "docker is not available: no daemon") {
			t.Errorf("Unavailable in a gate logged %q, not the caller's reason", r.logs)
		}
	}
	got, err := os.ReadFile(skips)
	if err != nil {
		t.Fatalf("the skip log: %v", err)
	}
	if string(got) != "TestContainer\nTestContainer\n" {
		t.Fatalf("the skip log holds %q; want one line per skip", got)
	}
}

// TestUnavailableGateNeedsExactlyOne: only RELIANT_GATE=1 marks a gate (what
// reliant's gaterun.GateEnv sets); any other value keeps the CI failure.
func TestUnavailableGateNeedsExactlyOne(t *testing.T) {
	t.Setenv("CI", "1")
	t.Setenv("RELIANT_GATE", "true")
	t.Setenv("RELIANT_GATE_DOCKER_SKIPS", "")
	if r := callUnavailable(); !r.failed {
		t.Fatalf("Unavailable with RELIANT_GATE=true under CI: failed=%t skipped=%t; want a failure", r.failed, r.skipped)
	}
}

// TestUnavailableInAGateFailsAnUncountedSkip: a gate counts skips only through
// its skip log, so in a gate a skip that cannot be recorded (no log set, or a
// log that cannot be written) fails rather than going unrun uncounted.
func TestUnavailableInAGateFailsAnUncountedSkip(t *testing.T) {
	for _, tc := range []struct{ name, log string }{
		{"no skip log", ""},
		{"unwritable skip log", filepath.Join(t.TempDir(), "missing-dir", "test.gate-run-1")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CI", "")
			t.Setenv("RELIANT_GATE", "1")
			t.Setenv("RELIANT_GATE_DOCKER_SKIPS", tc.log)
			r := callUnavailable()
			if !r.failed || r.skipped {
				t.Fatalf("Unavailable in a gate with %s: failed=%t skipped=%t; want a failure", tc.name, r.failed, r.skipped)
			}
			if !strings.Contains(r.msg, "docker is not available: no daemon") {
				t.Errorf("Unavailable message %q does not carry the caller's reason", r.msg)
			}
		})
	}
}
