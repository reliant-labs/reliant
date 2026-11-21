package diagnostics

import (
	"testing"
)

// TestRegistry_RegisterStub_RecordsKindStubImpl asserts the canonical
// stub-impl record shape: Kind, Symbol, File, Line set; Component and
// DepName left empty (stubs have no wiring component).
func TestRegistry_RegisterStub_RecordsKindStubImpl(t *testing.T) {
	r := NewRegistry()
	r.RegisterStub("botconfig.LoadFromYAML", "internal/botconfig/config.go", 18)

	if got := r.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1", got)
	}

	got := r.Boot(NopEmitter{})
	if len(got) != 1 {
		t.Fatalf("Boot returned %d diagnostics, want 1", len(got))
	}
	d := got[0]
	if d.Kind != KindStubImpl {
		t.Errorf("Kind = %q, want %q", d.Kind, KindStubImpl)
	}
	if d.Symbol != "botconfig.LoadFromYAML" {
		t.Errorf("Symbol = %q, want %q", d.Symbol, "botconfig.LoadFromYAML")
	}
	if d.File != "internal/botconfig/config.go" {
		t.Errorf("File = %q", d.File)
	}
	if d.Line != 18 {
		t.Errorf("Line = %d, want 18", d.Line)
	}
	if d.Component != "" || d.DepName != "" {
		t.Errorf("Component/DepName should be empty for stub-impl, got %q/%q", d.Component, d.DepName)
	}
	if d.Severity != SeverityWarn {
		t.Errorf("Severity = %q, want %q", d.Severity, SeverityWarn)
	}
}

// TestRegistry_RegisterNilDep_RecordsKindNilDep asserts the canonical
// nil-dep record shape: Kind, Component, DepName set; Symbol is the
// "component.depName" composite.
func TestRegistry_RegisterNilDep_RecordsKindNilDep(t *testing.T) {
	r := NewRegistry()
	r.RegisterNilDep("wireWorkerCalibratorRefitDeps", "PgUnsettled", "pkg/app/wire_gen.go", 128)

	got := r.Boot(NopEmitter{})
	if len(got) != 1 {
		t.Fatalf("Boot returned %d, want 1", len(got))
	}
	d := got[0]
	if d.Kind != KindNilDep {
		t.Errorf("Kind = %q, want %q", d.Kind, KindNilDep)
	}
	if d.Component != "wireWorkerCalibratorRefitDeps" {
		t.Errorf("Component = %q", d.Component)
	}
	if d.DepName != "PgUnsettled" {
		t.Errorf("DepName = %q", d.DepName)
	}
	wantSym := "wireWorkerCalibratorRefitDeps.PgUnsettled"
	if d.Symbol != wantSym {
		t.Errorf("Symbol = %q, want %q", d.Symbol, wantSym)
	}
}

// TestRegistry_RegisterEmpty_NoOp asserts the documented no-op
// behavior for empty symbol / component / depName. Codegen should
// never emit these, but a defensive no-op is cheaper than a log line
// that complains about an empty registration.
func TestRegistry_RegisterEmpty_NoOp(t *testing.T) {
	r := NewRegistry()
	r.RegisterStub("", "x", 1)
	r.RegisterNilDep("", "x", "y", 2)
	r.RegisterNilDep("x", "", "y", 3)
	if got := r.Len(); got != 0 {
		t.Errorf("Len() = %d after empty registrations, want 0", got)
	}
}

// TestRegistry_Boot_OrderIsStable asserts the sort: by Kind, then by
// Symbol. Stable ordering matters for log-search and CI snapshot
// tests that assert on summary text.
func TestRegistry_Boot_OrderIsStable(t *testing.T) {
	r := NewRegistry()
	// Insert intentionally out of order across both Kind and Symbol.
	r.RegisterNilDep("wireZ", "Dep", "f", 1)
	r.RegisterStub("zeta.Func", "f", 2)
	r.RegisterStub("alpha.Func", "f", 3)
	r.RegisterNilDep("wireA", "Dep", "f", 4)

	got := r.Boot(NopEmitter{})
	if len(got) != 4 {
		t.Fatalf("Boot returned %d, want 4", len(got))
	}
	// nil-dep < stub-impl lexicographically, so all nil-deps come
	// first.
	wantSyms := []string{
		"wireA.Dep",
		"wireZ.Dep",
		"alpha.Func",
		"zeta.Func",
	}
	for i, want := range wantSyms {
		if got[i].Symbol != want {
			t.Errorf("got[%d].Symbol = %q, want %q", i, got[i].Symbol, want)
		}
	}
}

// TestRegistry_Boot_NilEmitter_UsesNop asserts that Boot(nil) does
// not crash. The Registry.Boot contract documents NopEmitter
// fallback; this exercises it.
func TestRegistry_Boot_NilEmitter_UsesNop(t *testing.T) {
	r := NewRegistry()
	r.RegisterStub("x.Y", "f", 1)
	got := r.Boot(nil)
	if len(got) != 1 {
		t.Errorf("Boot(nil) returned %d, want 1", len(got))
	}
}

// TestRegistry_Reset_ClearsEntries asserts the test-only Reset
// helper. Production code does not call Reset; the test confirms it
// works for harnesses that run multiple Boot scenarios in one
// process.
func TestRegistry_Reset_ClearsEntries(t *testing.T) {
	r := NewRegistry()
	r.RegisterStub("x.Y", "f", 1)
	if r.Len() != 1 {
		t.Fatalf("pre-reset Len = %d", r.Len())
	}
	r.Reset()
	if r.Len() != 0 {
		t.Errorf("post-reset Len = %d, want 0", r.Len())
	}
}

// TestRegistry_BootCallsSummary asserts that Boot fires exactly one
// Summary call regardless of the number of Emit calls. The countingEmitter
// inlined here is the canonical pattern for asserting on Emitter
// call counts without dragging in a mock-gen dependency — matches
// the contractkit.Recorder convention (forge/pkg/contractkit) but
// stays vendor-free for this skeleton.
func TestRegistry_BootCallsSummary(t *testing.T) {
	r := NewRegistry()
	r.RegisterStub("x.Y", "f", 1)
	r.RegisterStub("x.Z", "f", 2)

	c := &countingEmitter{}
	r.Boot(c)

	if c.emitCalls != 2 {
		t.Errorf("emitCalls = %d, want 2", c.emitCalls)
	}
	if c.summaryCalls != 1 {
		t.Errorf("summaryCalls = %d, want 1", c.summaryCalls)
	}
	if c.lastSummarySize != 2 {
		t.Errorf("lastSummarySize = %d, want 2", c.lastSummarySize)
	}
}

// countingEmitter is a minimal Emitter that records call counts. Used
// across registry_test.go and strict_test.go.
type countingEmitter struct {
	emitCalls       int
	summaryCalls    int
	lastSummarySize int
}

func (c *countingEmitter) Emit(Diagnostic) { c.emitCalls++ }
func (c *countingEmitter) Summary(ds []Diagnostic) {
	c.summaryCalls++
	c.lastSummarySize = len(ds)
}
