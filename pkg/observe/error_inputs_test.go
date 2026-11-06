package observe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// error_inputs_test.go — an ERROR record carries what an issue is grouped by:
// its call site, the kind of failure, and for a panic the frame that panicked.

const (
	keyFunc = "code.function.name"
	keyFile = "code.file.path"
	keyLine = "code.line.number"
)

type typedErr struct{ msg string }

func (e *typedErr) Error() string { return e.msg }

type multiErr struct{ errs []error }

func (e multiErr) Error() string   { return "multi" }
func (e multiErr) Unwrap() []error { return e.errs }

func callerLogsError(l *slog.Logger, args ...any) { l.Error("boom", args...) }

func TestErrorInputsAttributes(t *testing.T) {
	clearOTelEnv(t)
	pgLike := &typedErr{"duplicate key"}

	tests := []struct {
		name  string
		level slog.Level
		args  []any
		want  map[string]any // key → value; nil value means "absent"
	}{
		{
			name: "error with typed cause", level: slog.LevelError,
			args: []any{"err", fmt.Errorf("insert: %w", pgLike)},
			want: map[string]any{LogKeyExceptionType: "*observe.typedErr", LogKeyExceptionMessage: "duplicate key"},
		},
		{
			name: "any key holds the error", level: slog.LevelError,
			args: []any{"other", "x", "failure", errors.New("plain")},
			want: map[string]any{LogKeyExceptionType: "*errors.errorString", LogKeyExceptionMessage: "plain"},
		},
		{
			name: "first error attribute wins", level: slog.LevelError,
			args: []any{"a", pgLike, "b", errors.New("second")},
			want: map[string]any{LogKeyExceptionType: "*observe.typedErr"},
		},
		{
			name: "joined errors take the first branch", level: slog.LevelError,
			args: []any{"err", fmt.Errorf("w: %w", errors.Join(pgLike, errors.New("other")))},
			want: map[string]any{LogKeyExceptionType: "*observe.typedErr", LogKeyExceptionMessage: "duplicate key"},
		},
		{
			name: "custom multi-error takes the first branch", level: slog.LevelError,
			args: []any{"err", multiErr{[]error{pgLike, errors.New("b")}}},
			want: map[string]any{LogKeyExceptionType: "*observe.typedErr"},
		},
		{
			name: "svcerr cause is the failure, not the client message", level: slog.LevelError,
			args: []any{"err", svcerr.WithCause(errors.New(svcerr.InternalMessage), pgLike)},
			want: map[string]any{LogKeyExceptionType: "*observe.typedErr", LogKeyExceptionMessage: "duplicate key"},
		},
		{
			name: "no error attribute: source only", level: slog.LevelError,
			args: []any{"k", "v"},
			want: map[string]any{LogKeyExceptionType: nil, LogKeyExceptionMessage: nil},
		},
		{
			name: "caller's exception.type is kept", level: slog.LevelError,
			args: []any{"err", pgLike, LogKeyExceptionType, "custom.Kind"},
			want: map[string]any{LogKeyExceptionType: "custom.Kind", LogKeyExceptionMessage: nil},
		},
		{
			name: "warn carries none", level: slog.LevelWarn,
			args: []any{"err", pgLike},
			want: map[string]any{keyFunc: nil, keyFile: nil, keyLine: nil, LogKeyExceptionType: nil, LogKeyExceptionMessage: nil},
		},
		{
			name: "info carries none", level: slog.LevelInfo,
			args: []any{"err", pgLike},
			want: map[string]any{keyFunc: nil, LogKeyExceptionType: nil},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := traceJSONLogger(&buf, Config{})
			logger.Log(context.Background(), tc.level, "msg", tc.args...)

			line := decodeLines(t, &buf)[0]
			for k, want := range tc.want {
				got, present := line[k]
				if want == nil {
					if present {
						t.Errorf("%s = %v, want absent", k, got)
					}
					continue
				}
				if got != want {
					t.Errorf("%s = %v, want %v", k, got, want)
				}
			}
			if tc.level >= slog.LevelError {
				for _, k := range []string{keyFunc, keyFile, keyLine} {
					if _, ok := line[k]; !ok {
						t.Errorf("ERROR record is missing %s: %v", k, line)
					}
				}
			}
		})
	}
}

func TestErrorInputsCallSiteIsTheLoggingFunction(t *testing.T) {
	var buf bytes.Buffer
	logger := traceJSONLogger(&buf, Config{})
	callerLogsError(logger, "err", errors.New("x"))

	line := decodeLines(t, &buf)[0]
	if got := line[keyFunc]; got != "github.com/reliant-labs/forge/pkg/observe.callerLogsError" {
		t.Errorf("%s = %v", keyFunc, got)
	}
	if got, _ := line[keyFile].(string); !strings.HasSuffix(got, "error_inputs_test.go") {
		t.Errorf("%s = %v", keyFile, got)
	}
	if got, _ := line[keyLine].(float64); got == 0 {
		t.Errorf("%s = %v", keyLine, got)
	}
}

// wrappedAdapterLog mimics go.temporal.io/sdk/log's slog adapter: the logger
// builds the record itself with a PC captured by runtime.Callers and calls
// Handler().Handle directly, so no frame of the slog.Logger methods is on the
// stack. The call site is the function that called the adapter.
func wrappedAdapterLog(ctx context.Context, l *slog.Logger, msg string, attrs ...slog.Attr) {
	var pcs [1]uintptr
	runtime.Callers(2, pcs[:]) // skip Callers and wrappedAdapterLog
	r := slog.NewRecord(time.Now(), slog.LevelError, msg, pcs[0])
	r.AddAttrs(attrs...)
	_ = l.Handler().Handle(ctx, r)
}

func temporalActivityBody(ctx context.Context, l *slog.Logger) {
	wrappedAdapterLog(ctx, l, "Activity error.", slog.Any("Error", errors.New("workflow blew up")))
}

func TestErrorInputsTemporalAdapterNamesTheCaller(t *testing.T) {
	clearOTelEnv(t)
	var buf bytes.Buffer
	// The full production stack: the error-class handler outside the log
	// handler, as serverkit builds it. Neither may become the call site.
	logger := slog.New(NewErrorClassHandler(NewLogHandler(slog.NewJSONHandler(&buf, nil), Config{})))
	temporalActivityBody(context.Background(), logger)

	line := decodeLines(t, &buf)[0]
	const want = "github.com/reliant-labs/forge/pkg/observe.temporalActivityBody"
	if got := line[keyFunc]; got != want {
		t.Errorf("%s = %v, want %s", keyFunc, got, want)
	}
	if got, _ := line[keyFunc].(string); strings.Contains(got, "Handle") {
		t.Errorf("call site is a handler frame: %s", got)
	}
	if line[LogKeyExceptionType] != "*errors.errorString" {
		t.Errorf("exception.type = %v", line[LogKeyExceptionType])
	}
}

func TestErrorInputsRecordWithoutPCHasNoSource(t *testing.T) {
	var buf bytes.Buffer
	logger := traceJSONLogger(&buf, Config{})
	r := slog.NewRecord(time.Now(), slog.LevelError, "no pc", 0)
	r.AddAttrs(slog.Any("err", errors.New("x")))
	_ = logger.Handler().Handle(context.Background(), r)

	line := decodeLines(t, &buf)[0]
	if _, ok := line[keyFunc]; ok {
		t.Errorf("a record with no PC must not invent a call site: %v", line)
	}
	if line[LogKeyExceptionType] == nil {
		t.Errorf("exception.type must not depend on the PC: %v", line)
	}
}

func TestExceptionMessageIsBoundedTo1KiB(t *testing.T) {
	tests := []struct {
		name string
		msg  string
	}{
		{"ascii", strings.Repeat("a", 5000)},
		{"multibyte straddling the cut", strings.Repeat("日", 2000)},
		{"exactly the bound", strings.Repeat("b", MaxExceptionMessage)},
		{"under the bound", "short"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := traceJSONLogger(&buf, Config{})
			logger.Error("boom", "err", errors.New(tc.msg))

			got, _ := decodeLines(t, &buf)[0][LogKeyExceptionMessage].(string)
			if len(got) > MaxExceptionMessage {
				t.Errorf("len = %d, want <= %d", len(got), MaxExceptionMessage)
			}
			if !utf8.ValidString(got) {
				t.Errorf("message cut mid-rune: %q", got[len(got)-8:])
			}
			if len(tc.msg) <= MaxExceptionMessage {
				if got != tc.msg {
					t.Errorf("message changed under the bound: %q", got)
				}
				return
			}
			if !strings.HasSuffix(got, ellipsis) {
				t.Errorf("a cut message must say so: %q", got[len(got)-8:])
			}
		})
	}
}

func TestInnermostError(t *testing.T) {
	root := &typedErr{"root"}
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"bare", root, root},
		{"wrapped", fmt.Errorf("a: %w", fmt.Errorf("b: %w", root)), root},
		{"join first", errors.Join(root, errors.New("x")), root},
		{"wrapped join first", fmt.Errorf("a: %w", errors.Join(root, errors.New("x"))), root},
		{"empty join stays", errors.Join(), nil},
		{"multi with nil first stays", multiErr{[]error{nil, root}}, multiErr{[]error{nil, root}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil {
				return
			}
			got := innermostError(tc.err)
			if tc.name == "multi with nil first stays" {
				if _, ok := got.(multiErr); !ok {
					t.Errorf("got %T", got)
				}
				return
			}
			if got != tc.want {
				t.Errorf("got %v (%T), want %v", got, got, tc.want)
			}
		})
	}
}

func TestErrorGroup(t *testing.T) {
	var buf bytes.Buffer
	logger := traceJSONLogger(&buf, Config{})
	logger.ErrorContext(context.Background(), "reconcile drift", ErrorGroup(context.Background(), "billing-reconcile-drift"))

	if got := decodeLines(t, &buf)[0][LogKeyErrorGroup]; got != "billing-reconcile-drift" {
		t.Errorf("error.group = %v", got)
	}
}

//go:noinline
func panickingFrame() { panic("kaboom") }

//go:noinline
func nilDerefFrame() {
	var m map[string]int
	m["x"] = 1
}

//go:noinline
func indexFrame(i int) int { return []int{1}[i] }

func recoverAndLog(logger *slog.Logger, f func()) {
	defer func() {
		if rec := recover(); rec != nil {
			LogPanic(context.Background(), logger, "panic recovered", rec, slog.String("path", "/x"))
		}
	}()
	f()
}

func TestLogPanicNamesThePanickingFrame(t *testing.T) {
	const pkg = "github.com/reliant-labs/forge/pkg/observe."
	tests := []struct {
		name     string
		f        func()
		wantFunc string
		wantType string
	}{
		{"panic call", panickingFrame, pkg + "panickingFrame", "string"},
		{"runtime error: nil map write", nilDerefFrame, pkg + "nilDerefFrame", "runtime.plainError"},
		{"runtime error: index", func() { indexFrame(5) }, pkg + "TestLogPanicNamesThePanickingFrame.func", "runtime.boundsError"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			recoverAndLog(traceJSONLogger(&buf, Config{}), tc.f)

			line := decodeLines(t, &buf)[0]
			got, _ := line[keyFunc].(string)
			if tc.name == "runtime error: index" {
				// indexFrame may be inlined into the closure: either names user code.
				if !strings.HasPrefix(got, pkg+"indexFrame") && !strings.HasPrefix(got, tc.wantFunc) {
					t.Errorf("%s = %q", keyFunc, got)
				}
			} else if got != tc.wantFunc {
				t.Errorf("%s = %q, want %q", keyFunc, got, tc.wantFunc)
			}
			if strings.Contains(got, "recoverAndLog") || strings.Contains(got, "LogPanic") || strings.HasPrefix(got, "runtime.") {
				t.Errorf("call site is the recover site or runtime machinery: %s", got)
			}
			if line[LogKeyExceptionType] != tc.wantType {
				t.Errorf("exception.type = %v, want %v", line[LogKeyExceptionType], tc.wantType)
			}
			stack, _ := line[LogKeyExceptionStacktrace].(string)
			if !strings.Contains(stack, "goroutine") || !strings.Contains(stack, "recoverAndLog") {
				t.Errorf("exception.stacktrace missing or empty: %q", stack)
			}
			if line["path"] != "/x" || line["panic"] == nil {
				t.Errorf("caller attrs / panic value lost: %v", line)
			}
			if line["level"] != "ERROR" {
				t.Errorf("level = %v", line["level"])
			}
		})
	}
}

func TestLogPanicWithErrorValueUsesInnermost(t *testing.T) {
	var buf bytes.Buffer
	recoverAndLog(traceJSONLogger(&buf, Config{}), func() { panic(fmt.Errorf("wrap: %w", &typedErr{"deep"})) })

	line := decodeLines(t, &buf)[0]
	if line[LogKeyExceptionType] != "*observe.typedErr" || line[LogKeyExceptionMessage] != "deep" {
		t.Errorf("got %v / %v", line[LogKeyExceptionType], line[LogKeyExceptionMessage])
	}
}

func TestLogPanicBoundsStackAndMessage(t *testing.T) {
	var buf bytes.Buffer
	recoverAndLog(traceJSONLogger(&buf, Config{}), func() { panic(strings.Repeat("x", 10_000)) })

	line := decodeLines(t, &buf)[0]
	if m, _ := line[LogKeyExceptionMessage].(string); len(m) > MaxExceptionMessage {
		t.Errorf("message len = %d", len(m))
	}
	if s, _ := line[LogKeyExceptionStacktrace].(string); len(s) > maxStacktrace {
		t.Errorf("stack len = %d", len(s))
	}
}

func TestLogPanicRespectsLevelAndNilLogger(t *testing.T) {
	var buf bytes.Buffer
	off := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError + 4}))
	LogPanic(context.Background(), off, "panic recovered", "x")
	if buf.Len() != 0 {
		t.Errorf("a disabled logger wrote: %s", buf.String())
	}
	LogPanic(context.Background(), nil, "panic recovered", "x") // slog.Default; must not panic
}

func TestRecoveryInterceptorNamesThePanickingFrame(t *testing.T) {
	var buf bytes.Buffer
	icep := RecoveryInterceptor(traceJSONLogger(&buf, Config{}))
	wrapped := icep.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		panickingFrame()
		return nil, nil
	})
	if _, err := wrapped(context.Background(), newTestRequest()); err == nil {
		t.Fatal("expected an error")
	}
	line := decodeLines(t, &buf)[0]
	if got := line[keyFunc]; got != "github.com/reliant-labs/forge/pkg/observe.panickingFrame" {
		t.Errorf("%s = %v", keyFunc, got)
	}
	if line["procedure"] == nil || line[LogKeyExceptionStacktrace] == nil {
		t.Errorf("missing procedure / stacktrace: %v", line)
	}
}

func TestRecoverMiddlewareNamesThePanickingFrame(t *testing.T) {
	var buf bytes.Buffer
	chain := NewComponentChain(RecoverMiddleware(traceJSONLogger(&buf, Config{})))
	err := chain.Run(context.Background(), "pkg.Do", func(context.Context) error { panickingFrame(); return nil })
	if err == nil {
		t.Fatal("expected an error")
	}
	line := decodeLines(t, &buf)[0]
	if got := line[keyFunc]; got != "github.com/reliant-labs/forge/pkg/observe.panickingFrame" {
		t.Errorf("%s = %v", keyFunc, got)
	}
}

func TestErrorInputsAreAddedUnderWithGroupAndWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	logger := traceJSONLogger(&buf, Config{}).With("component", "db")
	logger.Error("boom", "err", errors.New("x"))

	line := decodeLines(t, &buf)[0]
	if line["component"] != "db" || line[LogKeyExceptionType] == nil || line[keyFunc] == nil {
		t.Errorf("With() lost data: %v", line)
	}
}

// nopHandler isolates the wrapper's own allocations from the inner handler's
// (a real JSON handler's sync.Pool makes counts jitter under -race).
type nopHandler struct{}

func (nopHandler) Enabled(context.Context, slog.Level) bool  { return true }
func (nopHandler) Handle(context.Context, slog.Record) error { return nil }
func (h nopHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h nopHandler) WithGroup(string) slog.Handler           { return h }

// Below ERROR the handler must cost nothing: no scan, no clone, no allocation.
func TestErrorInputsCostNothingBelowError(t *testing.T) {
	h := errorInputsHandler{next: nopHandler{}}
	ctx := context.Background()
	var pcs [1]uintptr
	runtime.Callers(1, pcs[:])
	mk := func(level slog.Level) slog.Record {
		r := slog.NewRecord(time.Now(), level, "m", pcs[0])
		r.AddAttrs(slog.Any("err", errors.New("x")), slog.String("k", "v"))
		return r
	}
	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn} {
		r := mk(level)
		if got := testing.AllocsPerRun(100, func() { _ = h.Handle(ctx, r) }); got != 0 {
			t.Errorf("%v: %v allocs added below ERROR, want 0", level, got)
		}
	}
	r := mk(slog.LevelError)
	if got := testing.AllocsPerRun(100, func() { _ = h.Handle(ctx, r) }); got == 0 {
		t.Error("ERROR record allocated nothing: the attributes were not added")
	}
}

// captureProcessor records what reaches the OTLP logs SDK.
type captureProcessor struct {
	mu   sync.Mutex
	recs []map[string]attribute.Value
	body []string
}

func (p *captureProcessor) OnEmit(_ context.Context, r *sdklog.Record) error {
	m := map[string]attribute.Value{}
	r.WalkAttributes(func(kv attribute.KeyValue) bool { m[string(kv.Key)] = kv.Value; return true })
	p.mu.Lock()
	p.recs = append(p.recs, m)
	p.body = append(p.body, r.Body().AsString())
	p.mu.Unlock()
	return nil
}
func (p *captureProcessor) Enabled(context.Context, sdklog.EnabledParameters) bool { return true }
func (p *captureProcessor) Shutdown(context.Context) error                         { return nil }
func (p *captureProcessor) ForceFlush(context.Context) error                       { return nil }

func TestErrorInputsReachTheOTLPBridge(t *testing.T) {
	clearOTelEnv(t)
	t.Setenv("OTEL_LOGS_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")
	prev := otel.GetLoggerProvider()
	t.Cleanup(func() { otel.SetLoggerProvider(prev) })
	cap := &captureProcessor{}
	otel.SetLoggerProvider(sdklog.NewLoggerProvider(sdklog.WithProcessor(cap)))

	var buf bytes.Buffer
	cfg := Config{ServiceName: "svc"}
	h := NewLogHandler(slog.NewJSONHandler(&buf, nil), cfg)
	logger := slog.New(h)
	logger.Warn("warned", "err", errors.New("w"))
	callerLogsError(logger, "err", fmt.Errorf("a: %w", &typedErr{"deep"}))
	temporalActivityBody(context.Background(), logger)

	if len(cap.recs) != 3 {
		t.Fatalf("bridge got %d records, want 3", len(cap.recs))
	}
	if _, ok := cap.recs[0][keyFunc]; ok {
		t.Errorf("WARN record carries a call site: %v", cap.recs[0])
	}
	// The OTLP SDK derives exception.* from the bridge's error slot at every
	// level on its own; we add nothing below ERROR (see the stdout assertions).
	if _, ok := cap.recs[0][LogKeyExceptionStacktrace]; ok {
		t.Errorf("WARN record carries a stacktrace: %v", cap.recs[0])
	}
	e := cap.recs[1]
	if e[keyFunc].AsString() != "github.com/reliant-labs/forge/pkg/observe.callerLogsError" ||
		e[LogKeyExceptionType].AsString() != "*observe.typedErr" ||
		e[LogKeyExceptionMessage].AsString() != "deep" { // not the SDK-derived "a: deep"
		t.Errorf("ERROR record on the bridge: %v", e)
	}
	if got := cap.recs[2][keyFunc].AsString(); got != "github.com/reliant-labs/forge/pkg/observe.temporalActivityBody" {
		t.Errorf("Temporal-style record on the bridge names %q", got)
	}
	// Both paths agree.
	lines := decodeLines(t, &buf)
	if lines[1][keyFunc] != e[keyFunc].AsString() {
		t.Errorf("stdout %v != bridge %v", lines[1][keyFunc], e[keyFunc].AsString())
	}
}
