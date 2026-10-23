package diagnostics

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestLogEmitter_EmitWritesCanonicalEvent asserts the stable
// event-name attribute lands on every per-diagnostic log line, plus
// the documented attribute set (kind, symbol, file, line). Operators
// grep for the event name; dashboards alert on its count.
func TestLogEmitter_EmitWritesCanonicalEvent(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := NewLogEmitter(logger)

	e.Emit(Diagnostic{
		Kind:     KindStubImpl,
		Symbol:   "botconfig.LoadFromYAML",
		File:     "internal/botconfig/config.go",
		Line:     18,
		Message:  "x is a stub",
		Severity: SeverityWarn,
	})

	out := buf.String()
	if !strings.Contains(out, "event="+EventName) {
		t.Errorf("missing canonical event attribute %q in output:\n%s", EventName, out)
	}
	if !strings.Contains(out, "kind=stub-impl") {
		t.Errorf("missing kind attribute in output:\n%s", out)
	}
	if !strings.Contains(out, "symbol=botconfig.LoadFromYAML") {
		t.Errorf("missing symbol attribute in output:\n%s", out)
	}
	if !strings.Contains(out, "line=18") {
		t.Errorf("missing line attribute in output:\n%s", out)
	}
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("expected WARN level, got:\n%s", out)
	}
}

// TestLogEmitter_EmitOmitsEmptyComponentDepName asserts that a
// KindStubImpl diagnostic (Component / DepName empty) does NOT emit
// those attributes with empty values — empty `component=` /
// `dep_name=` would pollute log search.
func TestLogEmitter_EmitOmitsEmptyComponentDepName(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	e := NewLogEmitter(logger)

	e.Emit(Diagnostic{
		Kind:    KindStubImpl,
		Symbol:  "x.Y",
		File:    "f",
		Line:    1,
		Message: "x",
	})

	out := buf.String()
	if strings.Contains(out, "component=") {
		t.Errorf("expected no `component=` attribute for stub-impl, got:\n%s", out)
	}
	if strings.Contains(out, "dep_name=") {
		t.Errorf("expected no `dep_name=` attribute for stub-impl, got:\n%s", out)
	}
}

// TestLogEmitter_EmitIncludesComponentDepNameForNilDep asserts the
// dual to the previous test: KindNilDep diagnostics DO emit
// component and dep_name attributes.
func TestLogEmitter_EmitIncludesComponentDepNameForNilDep(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	e := NewLogEmitter(logger)

	e.Emit(Diagnostic{
		Kind:      KindNilDep,
		Symbol:    "wireX.Dep",
		File:      "f",
		Line:      1,
		Component: "wireX",
		DepName:   "Dep",
		Message:   "x",
	})

	out := buf.String()
	if !strings.Contains(out, "component=wireX") {
		t.Errorf("missing component attribute for nil-dep, got:\n%s", out)
	}
	if !strings.Contains(out, "dep_name=Dep") {
		t.Errorf("missing dep_name attribute for nil-dep, got:\n%s", out)
	}
}

// TestLogEmitter_SummaryEmptyIsNoOp asserts the documented "no output
// for zero diagnostics" behavior. A clean project should not produce
// a warn line at every boot.
func TestLogEmitter_SummaryEmptyIsNoOp(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	e := NewLogEmitter(logger)

	e.Summary(nil)
	e.Summary([]Diagnostic{})

	if out := buf.String(); out != "" {
		t.Errorf("expected empty output for empty Summary, got:\n%s", out)
	}
}

// TestLogEmitter_SummaryWritesCountAndItems asserts the summary
// shape: stable event name, count attribute, items attribute. The
// items list is the operator's single-line answer to "what's
// unwired?".
func TestLogEmitter_SummaryWritesCountAndItems(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	e := NewLogEmitter(logger)

	e.Summary([]Diagnostic{
		{Kind: KindStubImpl, Symbol: "a.A"},
		{Kind: KindStubImpl, Symbol: "b.B"},
	})

	out := buf.String()
	if !strings.Contains(out, "event="+SummaryEventName) {
		t.Errorf("missing summary event %q in output:\n%s", SummaryEventName, out)
	}
	if !strings.Contains(out, "count=2") {
		t.Errorf("missing count=2 in output:\n%s", out)
	}
	if !strings.Contains(out, "a.A") || !strings.Contains(out, "b.B") {
		t.Errorf("missing item symbols in output:\n%s", out)
	}
}

// TestLogEmitter_NilLoggerFallsBackToDefault asserts that a zero-value
// LogEmitter (no Logger field set) does not crash — it falls through
// to slog.Default(). Saves callers a nil-check at the wire point.
func TestLogEmitter_NilLoggerFallsBackToDefault(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("LogEmitter with nil Logger panicked: %v", r)
		}
	}()
	e := LogEmitter{}
	e.Emit(Diagnostic{Symbol: "x", Message: "x"})
	e.Summary([]Diagnostic{{Symbol: "x"}})
}

// TestNopEmitter asserts the obvious: NopEmitter drops everything
// without crashing. Cheap insurance for callers that wire it as a
// no-op default.
func TestNopEmitter(t *testing.T) {
	var e Emitter = NopEmitter{}
	e.Emit(Diagnostic{})
	e.Summary([]Diagnostic{{}, {}})
}

// TestMultiEmitter_FansOut asserts that Emit and Summary calls reach
// every base emitter exactly once per call. Order across base
// emitters is the construction order — important for the strict +
// log composition (strict must see Summary last, but its Emit can run
// in any order).
func TestMultiEmitter_FansOut(t *testing.T) {
	a := &countingEmitter{}
	b := &countingEmitter{}
	m := NewMultiEmitter(a, b)

	m.Emit(Diagnostic{Symbol: "x"})
	m.Emit(Diagnostic{Symbol: "y"})
	m.Summary([]Diagnostic{{Symbol: "x"}, {Symbol: "y"}})

	for _, e := range []*countingEmitter{a, b} {
		if e.emitCalls != 2 {
			t.Errorf("emitCalls = %d, want 2", e.emitCalls)
		}
		if e.summaryCalls != 1 {
			t.Errorf("summaryCalls = %d, want 1", e.summaryCalls)
		}
		if e.lastSummarySize != 2 {
			t.Errorf("lastSummarySize = %d, want 2", e.lastSummarySize)
		}
	}
}
