package diagnostics

import (
	"testing"
)

// TestStrictEmitter_NoDiagnosticsDoesNotExit asserts that a clean
// project (empty Summary slice) does NOT trigger osExit. Strict mode
// only fires when something is actually unwired; ticking up an exit
// on a clean boot would be hostile.
func TestStrictEmitter_NoDiagnosticsDoesNotExit(t *testing.T) {
	prev := osExit
	defer func() { osExit = prev }()

	exited := false
	osExit = func(int) { exited = true }

	s := NewStrictEmitter(NopEmitter{})
	s.Summary(nil)
	s.Summary([]Diagnostic{})

	if exited {
		t.Error("osExit was called with no diagnostics; expected clean exit")
	}
}

// TestStrictEmitter_NonEmptySummaryExits asserts the production-grade
// strict behavior: any diagnostic at Summary terminates the process.
// We swap osExit out so the test process survives, then assert on the
// captured exit code.
func TestStrictEmitter_NonEmptySummaryExits(t *testing.T) {
	prev := osExit
	defer func() { osExit = prev }()

	var captured int
	exited := false
	osExit = func(code int) {
		exited = true
		captured = code
	}

	s := NewStrictEmitter(NopEmitter{})
	s.Summary([]Diagnostic{{Symbol: "x.Y"}})

	if !exited {
		t.Fatal("osExit was not called despite a non-empty Summary")
	}
	if captured != 1 {
		t.Errorf("osExit code = %d, want 1", captured)
	}
}

// TestStrictEmitter_SummaryReachesBaseFirst asserts the documented
// "base sees Summary before exit" contract — operators must see the
// full list before the process dies.
func TestStrictEmitter_SummaryReachesBaseFirst(t *testing.T) {
	prev := osExit
	defer func() { osExit = prev }()

	base := &countingEmitter{}
	osExit = func(int) {
		// At the moment osExit runs, base.Summary must have already
		// been called.
		if base.summaryCalls != 1 {
			t.Errorf("base.Summary called %d times before exit, want 1", base.summaryCalls)
		}
	}

	s := NewStrictEmitter(base)
	s.Summary([]Diagnostic{{Symbol: "x"}})
}

// TestStrictEmitter_EmitDoesNotExit asserts that per-diagnostic Emit
// calls never terminate — the exit decision is deferred to Summary
// so the operator sees every line.
func TestStrictEmitter_EmitDoesNotExit(t *testing.T) {
	prev := osExit
	defer func() { osExit = prev }()

	exited := false
	osExit = func(int) { exited = true }

	s := NewStrictEmitter(NopEmitter{})
	s.Emit(Diagnostic{Symbol: "x"})

	if exited {
		t.Error("Emit triggered osExit; expected exit only at Summary")
	}
}

// TestNewStrictEmitter_NilBaseFallsBackToNop asserts the documented
// nil-base behavior: NewStrictEmitter(nil) returns a working emitter
// that doesn't crash, rather than panicking on the first Emit.
func TestNewStrictEmitter_NilBaseFallsBackToNop(t *testing.T) {
	prev := osExit
	defer func() { osExit = prev }()

	osExit = func(int) {}

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("StrictEmitter with nil base panicked: %v", r)
		}
	}()
	s := NewStrictEmitter(nil)
	s.Emit(Diagnostic{Symbol: "x"})
	s.Summary([]Diagnostic{{Symbol: "x"}})
}
