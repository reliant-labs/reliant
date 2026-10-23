package diagnostics

import (
	"log/slog"
	"strings"
)

// emitter.go — runtime emit surface.
//
// Three implementations, and they are the whole set:
//
//   - LogEmitter writes structured slog lines. The emitter a caller
//     passes Registry.Boot to get warn-level output.
//   - NopEmitter drops everything. Used by tests that don't care
//     about diagnostic output, and as the default when a caller
//     passes nil to Registry.Boot.
//   - MultiEmitter fans out to N base emitters, for a caller that
//     wants diagnostics in two places at once.
//
// StrictEmitter (strict.go) wraps any of them to exit non-zero when a
// diagnostic was emitted.

// Emitter is the runtime sink for diagnostics.
//
// Implementations must be safe for concurrent calls (Bootstrap calls
// Boot serially, but composed emitters may fan out to goroutines).
// Boot calls Emit once per diagnostic, then Summary once with the
// full slice in stable order.
type Emitter interface {
	// Emit writes one diagnostic. Implementations should not retain
	// the diagnostic beyond the call — the Registry owns the
	// canonical copy.
	Emit(d Diagnostic)

	// Summary writes the roll-up. May be called with an empty slice,
	// in which case implementations should write a "clean" line (or
	// nothing) — never a misleading "0 entries" warning.
	Summary(ds []Diagnostic)
}

// EventName is the stable slog event-name attribute every LogEmitter
// log line carries. Operators grep this; dashboards count it. Do not
// rename without a deprecation cycle.
const EventName = "forge.scaffold.unwired"

// SummaryEventName is the stable slog event-name for the roll-up
// line. Distinct from EventName so dashboards can chart the summary
// count separately from per-diagnostic occurrences.
const SummaryEventName = "forge.scaffold.unwired.summary"

// LogEmitter writes structured warn-level slog lines.
//
// Logger may be nil — Emit falls back to slog.Default(), so projects
// that haven't wired a custom logger still get output. Most callers
// pass the per-process *slog.Logger from cmd/server.go.
type LogEmitter struct {
	Logger *slog.Logger
}

// NewLogEmitter returns a LogEmitter wrapping logger. Nil logger is
// fine; Emit will fall through to slog.Default().
func NewLogEmitter(logger *slog.Logger) LogEmitter {
	return LogEmitter{Logger: logger}
}

// Emit writes one diagnostic at warn level with the stable
// EventName attribute. Attribute keys (kind, symbol, file, line,
// component, dep_name) are stable across forge versions.
func (l LogEmitter) Emit(d Diagnostic) {
	logger := l.Logger
	if logger == nil {
		logger = slog.Default()
	}
	// Build the attribute slice manually so empty fields (Component,
	// DepName for KindStubImpl) don't land as `=""` in the output.
	attrs := []slog.Attr{
		slog.String("event", EventName),
		slog.String("kind", string(d.Kind)),
		slog.String("symbol", d.Symbol),
		slog.String("file", d.File),
		slog.Int("line", d.Line),
	}
	if d.Component != "" {
		attrs = append(attrs, slog.String("component", d.Component))
	}
	if d.DepName != "" {
		attrs = append(attrs, slog.String("dep_name", d.DepName))
	}
	logger.LogAttrs(nil, slog.LevelWarn, d.Message, attrs...)
}

// Summary writes the roll-up line. Empty slice → no output (Bootstrap
// callers don't want a "0 unwired scaffolds" warn at every boot of a
// clean project). Non-empty slice → one warn line with the count and
// a bracket-list of symbols, so a single log line answers "what's
// unwired?" without paging through the per-diagnostic lines.
func (l LogEmitter) Summary(ds []Diagnostic) {
	if len(ds) == 0 {
		return
	}
	logger := l.Logger
	if logger == nil {
		logger = slog.Default()
	}
	symbols := make([]string, 0, len(ds))
	for _, d := range ds {
		symbols = append(symbols, d.Symbol)
	}
	logger.LogAttrs(nil, slog.LevelWarn,
		"forge scaffold unwired summary",
		slog.String("event", SummaryEventName),
		slog.Int("count", len(ds)),
		slog.String("items", "["+strings.Join(symbols, ",")+"]"),
	)
}

// NopEmitter drops every Emit and Summary call. Used by tests that
// don't care about output, and as the fallback when a caller passes
// nil to Registry.Boot.
type NopEmitter struct{}

func (NopEmitter) Emit(Diagnostic)      {}
func (NopEmitter) Summary([]Diagnostic) {}

// MultiEmitter fans Emit and Summary calls out to every base emitter
// in order, so the same wiring gap can surface in two places at once
// (a caller supplies whatever second sink it wants — this package
// ships only the three implementations above).
//
// Construct with NewMultiEmitter so the zero value is unambiguous —
// an empty MultiEmitter is a NopEmitter equivalent, which is rarely
// what the caller wants.
type MultiEmitter struct {
	Emitters []Emitter
}

// NewMultiEmitter returns a MultiEmitter whose Emit/Summary calls fan
// out to each supplied emitter in order. Pass at least one emitter;
// an empty MultiEmitter is a NopEmitter equivalent.
func NewMultiEmitter(emitters ...Emitter) MultiEmitter {
	return MultiEmitter{Emitters: emitters}
}

// Emit fans the diagnostic out to every base emitter. Errors in any
// one emitter must not stop the others — implementations are expected
// to never panic, and we don't recover here.
func (m MultiEmitter) Emit(d Diagnostic) {
	for _, e := range m.Emitters {
		e.Emit(d)
	}
}

// Summary fans the roll-up out to every base emitter.
func (m MultiEmitter) Summary(ds []Diagnostic) {
	for _, e := range m.Emitters {
		e.Summary(ds)
	}
}
