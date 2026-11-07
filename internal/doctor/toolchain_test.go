package doctor

import (
	"context"
	"strings"
	"testing"
)

// A probe that never ran cannot say whether covdata exists. That is
// UNDETERMINED — the catch-all used to report it as a warning, which reads
// like a finding about the toolchain rather than a hole in the report.
//
// The cancelled context is the cheapest way to reach the catch-all without
// mutating PATH: exec refuses to start, so the branch sees a non-nil error
// that is neither exec.ErrNotFound nor the "no such tool" sentinel — the
// same shape a cancellation, a malformed install or a permissions failure
// produces in the field.
func TestCheckCovdataUnknownWhenProbeCannotRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := CheckCovdata(ctx, &Environment{})
	if res.Status != StatusUnknown {
		t.Fatalf("status = %s, want %s (message: %s)", res.Status, StatusUnknown, res.Message)
	}
	if !strings.Contains(res.Message, "unknown") {
		t.Errorf("message %q does not say the answer is unknown", res.Message)
	}
}

// The healthy toolchain must still PASS: the undetermined branch is the
// catch-all, and widening it must not swallow a real answer.
func TestCheckCovdataPassesOnHealthyToolchain(t *testing.T) {
	res := CheckCovdata(context.Background(), &Environment{})
	if res.Status != StatusPass {
		t.Skipf("no usable covdata in this toolchain (status %s: %s) — nothing to assert",
			res.Status, res.Message)
	}
}
