package kclrender

import (
	"errors"
	"strings"
	"testing"
)

// The regression: control-plane pinned forge v0.1.43, the installed binary
// was v0.1.42, and `forge generate` failed with KCL's own schema error
// pointing at `deploy/kcl/dev/main.k:2933` — a field that exists only in
// v0.1.43. forge's KCL schema module is embedded in the binary, so the older
// binary genuinely did not have that field; but the message blamed the
// user's KCL and never mentioned that two forge versions were in play. An
// agent read the line number as the fault and lost real time to it.
//
// These tests pin the two halves of the fix at the render seam: the skew is
// stated BEFORE the render is attempted, and if a render fails anyway the
// skew leads the diagnosis rather than trailing KCL's line number.

// TestSkewPreflightRefusesABinaryBehindThePin — the refusal is the point.
// A render with a schema older than the project's declared schema cannot
// succeed for any project that uses a field added since, and it fails in a
// way that misattributes the cause. Better to refuse up front and name it.
func TestSkewPreflightRefusesABinaryBehindThePin(t *testing.T) {
	err := skewPreflight("v0.1.43", "v0.1.42")
	if err == nil {
		t.Fatal("preflight allowed a render with a binary older than the project's pin")
	}
	msg := err.Error()

	for _, want := range []string{
		"v0.1.43", "v0.1.42",
		// The cause: the schema ships in the binary.
		"embedded",
		// The runbook shape forge errors follow.
		"Fix:",
		// The literal, runnable command.
		"go install github.com/reliant-labs/forge/cmd/forge@v0.1.43",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("skew refusal missing %q\n--- message ---\n%s", want, msg)
		}
	}

	// It must name the override, or a user with a genuine reason to proceed
	// has a wall instead of an escape hatch.
	if !strings.Contains(msg, "FORGE_ALLOW_VERSION_SKEW=1") {
		t.Errorf("skew refusal does not name the override:\n%s", msg)
	}
}

// TestSkewPreflightAllowsEveryNonSkewPairing. A false positive here refuses
// every render on a healthy install, which is far worse than the bug being
// fixed — forge builds itself from a working tree, so dev stamps are the
// common local case.
func TestSkewPreflightAllowsEveryNonSkewPairing(t *testing.T) {
	cases := []struct{ name, pinned, binary string }{
		{"versions equal", "v0.1.43", "v0.1.43"},
		{"binary ahead of pin", "v0.1.40", "v0.1.43"},
		{"no pin declared", "", "v0.1.43"},
		{"dev binary", "v0.1.43", "(devel)"},
		{"dirty binary", "v0.1.43", "v0.1.42+dirty"},
		{"pseudo-version binary", "v0.1.43", "v0.1.42-0.20260916085636-c01e07ec6ef2"},
		{"first-user sentinel pin", "0.0.0", "v0.1.43"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := skewPreflight(c.pinned, c.binary); err != nil {
				t.Errorf("skewPreflight(%q, %q) refused a healthy pairing: %v",
					c.pinned, c.binary, err)
			}
		})
	}
}

// TestRenderFailureLeadsWithTheSkew covers the belt-and-braces half. The
// preflight is silent for a pin it cannot compare, and a render can still
// fail for a reason the skew explains. When it does, the skew must come
// FIRST: the thing that cost time was reading KCL's line number as the
// fault, and a diagnosis appended after it gets read second, if at all.
func TestRenderFailureLeadsWithTheSkew(t *testing.T) {
	kclErr := errors.New("kpm run deploy/kcl/dev: " +
		"deploy/kcl/dev/main.k:2933: Cannot add member 'runtime_type' to schema 'Workload'")

	got := annotateRenderErr(kclErr, "v0.1.43", "v0.1.42")
	if got == nil {
		t.Fatal("annotateRenderErr dropped the render error")
	}
	msg := got.Error()

	// The skew must precede KCL's own text.
	skewAt := strings.Index(msg, "v0.1.42")
	kclAt := strings.Index(msg, "main.k:2933")
	if skewAt < 0 || kclAt < 0 {
		t.Fatalf("annotated error lost a half of the story:\n%s", msg)
	}
	if skewAt > kclAt {
		t.Errorf("KCL's line number precedes the version skew; the skew is the "+
			"leading diagnosis:\n%s", msg)
	}

	// The original error must remain unwrappable — callers match on it.
	if !errors.Is(got, kclErr) {
		t.Errorf("annotateRenderErr broke errors.Is on the underlying render error")
	}
}

// TestAnnotateRenderErrIsTransparentWithoutSkew — when there is no skew to
// report, the render error must pass through byte-identical. An annotation
// layer that reworded every failure would make every other render bug harder
// to read, which is the opposite of the intent.
func TestAnnotateRenderErrIsTransparentWithoutSkew(t *testing.T) {
	inner := errors.New("kpm run deploy/kcl/dev: some unrelated KCL failure")
	for _, c := range []struct{ pinned, binary string }{
		{"v0.1.43", "v0.1.43"},
		{"v0.1.40", "v0.1.43"},
		{"", "v0.1.43"},
		{"v0.1.43", "(devel)"},
	} {
		got := annotateRenderErr(inner, c.pinned, c.binary)
		if got.Error() != inner.Error() {
			t.Errorf("annotateRenderErr(%q, %q) rewrote an unrelated failure:\n%s",
				c.pinned, c.binary, got.Error())
		}
	}
	if annotateRenderErr(nil, "v0.1.43", "v0.1.42") != nil {
		t.Error("annotateRenderErr invented an error from a successful render")
	}
}
