package testdocker

import (
	"fmt"
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
}

func (r *recordingTB) Helper() {}

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
