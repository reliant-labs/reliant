package observe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// error_class_test.go — a user error is written, and pages nobody.
//
// The measured incident: a product's ERROR stream — what its alerts and its
// Sentry forwarder route on — was dominated by "no daemon connected for user"
// and "AI provider usage limit reached". The user's laptop was closed; the
// user's subscription was spent. Nobody on the server side could act, and the
// real faults were buried. These tests pin the policy at all three layers a
// record can come from.

// levelGate stands in for a Sentry forwarder: it captures every record at or
// above ERROR, the way the real ones gate, and passes everything through.
type levelGate struct {
	inner slog.Handler

	mu       *sync.Mutex
	captured *[]slog.Record
}

func newLevelGate(inner slog.Handler) *levelGate {
	return &levelGate{inner: inner, mu: &sync.Mutex{}, captured: &[]slog.Record{}}
}

func (g *levelGate) Enabled(ctx context.Context, l slog.Level) bool { return g.inner.Enabled(ctx, l) }
func (g *levelGate) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError {
		g.mu.Lock()
		*g.captured = append(*g.captured, r)
		g.mu.Unlock()
	}
	return g.inner.Handle(ctx, r)
}
func (g *levelGate) WithAttrs(as []slog.Attr) slog.Handler {
	return &levelGate{inner: g.inner.WithAttrs(as), mu: g.mu, captured: g.captured}
}
func (g *levelGate) WithGroup(name string) slog.Handler {
	return &levelGate{inner: g.inner.WithGroup(name), mu: g.mu, captured: g.captured}
}
func (g *levelGate) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(*g.captured)
}

// classLogger is the process logger shape NewErrorClassHandler documents:
// JSON lines, a level-gated forwarder, the class handler outermost.
func classLogger(buf *bytes.Buffer, level slog.Level, opts ...LogOption) (*slog.Logger, *levelGate) {
	gate := newLevelGate(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level}))
	return slog.New(NewErrorClassHandler(gate, opts...)), gate
}

func TestErrorClassHandler_UserErrorIsWrittenAndNotForwarded(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger, gate := classLogger(&buf, slog.LevelInfo)

	// The two prod offenders, logged the way hand-written code logs them.
	logger.Error("[TerminalWS] create terminal session failed",
		"error", fmt.Errorf("create terminal session: %w",
			svcerr.WithClass(svcerr.Unavailable("no daemon connected for user"), svcerr.ClassUser)))
	logger.Error("[CallLLM] stream failed", "err", svcerr.PlanLimit("AI provider usage limit reached"))

	if n := gate.count(); n != 0 {
		t.Fatalf("a user error reached the ERROR forwarder %d times", n)
	}
	for _, msg := range []string{"[TerminalWS] create terminal session failed", "[CallLLM] stream failed"} {
		got := records(t, &buf, msg)
		if len(got) != 1 {
			t.Fatalf("want the %q line written once, got %d:\n%s", msg, len(got), buf.String())
		}
		if got[0]["level"] != "INFO" || got[0]["error_class"] != "user" {
			t.Errorf("%q = level %v class %v, want INFO user", msg, got[0]["level"], got[0]["error_class"])
		}
	}
}

func TestErrorClassHandler_ServerErrorStillPages(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger, gate := classLogger(&buf, slog.LevelInfo)

	logger.Error("[Statsig] failed to get analytics directory", "error", errors.New("mkdir /.config: read-only file system"))
	logger.Error("billing call failed", "error", svcerr.Wrap(svcerr.Internal("charge failed")))

	if n := gate.count(); n != 2 {
		t.Fatalf("server errors forwarded %d times, want 2", n)
	}
	for _, rec := range records(t, &buf, "[Statsig] failed to get analytics directory") {
		if rec["level"] != "ERROR" || rec["error_class"] != "server" {
			t.Errorf("server error = level %v class %v, want ERROR server", rec["level"], rec["error_class"])
		}
	}
}

func TestErrorClassHandler_CanceledIsNotAnError(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger, gate := classLogger(&buf, slog.LevelInfo)

	logger.Error("[InlineLoop] iteration failed, exiting loop", "error", fmt.Errorf("stream: %w", context.Canceled))

	if gate.count() != 0 {
		t.Fatal("a cancellation reached the ERROR forwarder")
	}
	rec := records(t, &buf, "[InlineLoop] iteration failed, exiting loop")
	if len(rec) != 1 || rec[0]["level"] != "INFO" || rec[0]["error_class"] != "canceled" {
		t.Fatalf("canceled record = %v, want one INFO record with error_class=canceled", rec)
	}
}

// TestErrorClassHandler_OnlyEverLowers: the call site's level is a ceiling
// the policy may lower, never raise.
func TestErrorClassHandler_OnlyEverLowers(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger, _ := classLogger(&buf, slog.LevelDebug)

	logger.Warn("transient", "error", errors.New("connection reset"))
	logger.Debug("noisy", "error", svcerr.NotFound("x"))

	if rec := records(t, &buf, "transient"); len(rec) != 1 || rec[0]["level"] != "WARN" {
		t.Errorf("a server error at WARN must stay WARN, got %v", rec)
	}
	if rec := records(t, &buf, "noisy"); len(rec) != 1 || rec[0]["level"] != "DEBUG" {
		t.Errorf("a user error at DEBUG must stay DEBUG, got %v", rec)
	}
}

func TestErrorClassHandler_UserErrorLevelIsConfigurable(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger, gate := classLogger(&buf, slog.LevelInfo, WithUserErrorLevel(slog.LevelWarn))

	logger.Error("rejected", "error", svcerr.InvalidArgument("bad name"))

	if gate.count() != 0 {
		t.Fatal("a user error reached the ERROR forwarder")
	}
	if rec := records(t, &buf, "rejected"); len(rec) != 1 || rec[0]["level"] != "WARN" {
		t.Fatalf("want WARN under WithUserErrorLevel(WARN), got %v", rec)
	}
}

// TestErrorClassHandler_DemotedBelowTheFloorIsDropped: a handler running at
// WARN does not want an INFO record, even one that arrived as ERROR.
func TestErrorClassHandler_DemotedBelowTheFloorIsDropped(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger, _ := classLogger(&buf, slog.LevelWarn)

	logger.Error("rejected", "error", svcerr.InvalidArgument("bad name"))

	if buf.Len() != 0 {
		t.Fatalf("a record demoted below the handler's level was written: %s", buf.String())
	}
}

func TestErrorClassHandler_LeavesOtherRecordsAlone(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger, gate := classLogger(&buf, slog.LevelInfo)

	// No error value: nothing to classify. A string is not an error.
	logger.Error("plain", "error", "no daemon connected for user")
	// Already classified by a layer that knew better.
	logger.Error("classified", "error", svcerr.NotFound("x"), ErrorClassKey, "server")

	if gate.count() != 2 {
		t.Fatalf("records without a classifiable error must pass through at their level, forwarded %d of 2", gate.count())
	}
	if rec := records(t, &buf, "plain"); len(rec) != 1 || rec[0]["error_class"] != nil {
		t.Errorf("an unclassifiable record must not gain an error_class: %v", rec)
	}
	rec := records(t, &buf, "classified")
	if len(rec) != 1 || rec[0]["level"] != "ERROR" || rec[0]["error_class"] != "server" {
		t.Errorf("an already-classified record must pass through untouched: %v", rec)
	}
}

func TestErrorClassHandler_ClassifierExtendsSvcerr(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	// A library error svcerr cannot know: say, a workflow engine's
	// "benign" application error rebuilt after a serialization boundary.
	type benign struct{ error }
	logger, gate := classLogger(&buf, slog.LevelInfo, WithErrorClassifier(func(err error) svcerr.Class {
		var b benign
		if errors.As(err, &b) {
			return svcerr.ClassUser
		}
		return svcerr.ClassNone // no opinion: svcerr.Classify decides
	}))

	logger.Error("activity failed", "error", fmt.Errorf("call llm: %w", benign{errors.New("usage limit")}))
	logger.Error("plan", "error", svcerr.PlanLimit("seats"))
	logger.Error("boom", "error", errors.New("nil map"))

	if gate.count() != 1 {
		t.Fatalf("forwarded %d records, want only the server fault", gate.count())
	}
	if rec := records(t, &buf, "activity failed"); len(rec) != 1 || rec[0]["error_class"] != "user" {
		t.Errorf("the classifier's verdict was not applied: %v", rec)
	}
	if rec := records(t, &buf, "plan"); len(rec) != 1 || rec[0]["error_class"] != "user" {
		t.Errorf("ClassNone must fall back to svcerr.Classify: %v", rec)
	}
}

func TestErrorClassHandler_SurvivesWithAttrsAndGroups(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger, gate := classLogger(&buf, slog.LevelInfo)

	logger.With("chat_id", "c1").WithGroup("req").Error("scoped", "error", svcerr.NotFound("chat"))

	if gate.count() != 0 {
		t.Fatal("a user error logged through With/WithGroup reached the forwarder")
	}
	rec := records(t, &buf, "scoped")
	if len(rec) != 1 || rec[0]["level"] != "INFO" {
		t.Fatalf("want one INFO record, got %v", rec)
	}
}

// ─── The RPC edge and the component boundary ────────────────────────────

func TestLoggingInterceptor_MarkedUserErrorIsNotAnIncident(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	offline := svcerr.WithClass(svcerr.Unavailable("your machine is not connected"), svcerr.ClassUser)
	call := rpcEdge(t, []connect.Interceptor{LoggingInterceptor(jsonLogger(&buf, slog.LevelDebug))},
		func(proc string) error {
			if proc == pollProcedure {
				return svcerr.Wrap(fmt.Errorf("resolve daemon: %w", offline))
			}
			return svcerr.Wrap(svcerr.Unavailable("payments offline"))
		})

	call(pollProcedure)
	call(otherProcedure)

	byProc := map[string]map[string]any{}
	for _, rec := range records(t, &buf, "rpc failed") {
		byProc[rec["procedure"].(string)] = rec
	}
	if rec := byProc[pollProcedure]; rec["level"] != "INFO" || rec["error_class"] != "user" || rec["code"] != "unavailable" {
		t.Errorf("marked user error = %v, want INFO user unavailable", rec)
	}
	if rec := byProc[otherProcedure]; rec["level"] != "ERROR" || rec["error_class"] != "server" {
		t.Errorf("unmarked unavailable = %v, want ERROR server — a dependency refusing is still a page", rec)
	}
}

func TestLoggingInterceptor_UserErrorLevelIsConfigurable(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	call := rpcEdge(t, []connect.Interceptor{LoggingInterceptor(jsonLogger(&buf, slog.LevelDebug), WithUserErrorLevel(slog.LevelWarn))},
		func(string) error { return svcerr.Wrap(svcerr.PermissionDenied("admin only")) })

	call(pollProcedure)

	rec := records(t, &buf, "rpc failed")
	if len(rec) != 1 || rec[0]["level"] != "WARN" || rec[0]["error_class"] != "user" {
		t.Fatalf("want one WARN user record, got %v", rec)
	}
}

func TestLogMiddleware_UserErrorsLogAtTheUserLevel(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	mw := LogMiddleware(jsonLogger(&buf, slog.LevelDebug), slog.LevelDebug)

	runComponent(t, mw, "billing.Charge", 1, func(context.Context) error { return svcerr.InsufficientBalance("wallet empty") })
	runComponent(t, mw, "billing.Refund", 1, func(context.Context) error { return errors.New("stripe: 500") })

	if rec := records(t, &buf, "billing.Charge"); len(rec) != 1 || rec[0]["level"] != "INFO" || rec[0]["error_class"] != "user" {
		t.Errorf("user error = %v, want one INFO user record", rec)
	}
	if rec := records(t, &buf, "billing.Refund"); len(rec) != 1 || rec[0]["level"] != "ERROR" || rec[0]["error_class"] != "server" {
		t.Errorf("server error = %v, want one ERROR server record", rec)
	}
}

func TestMetricsInterceptor_CountsErrorsByClassAndCode(t *testing.T) {
	t.Parallel()
	m := newFakeMeter()
	icep := MetricsInterceptor(m)
	for _, err := range []error{svcerr.Wrap(svcerr.PlanLimit("seats")), errors.New("boom")} {
		wrapped := icep.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) { return nil, err })
		_, _ = wrapped(context.Background(), newTestRequest())
	}

	got := m.counters["rpc.server.errors"].attrs
	if len(got) != 2 {
		t.Fatalf("want 2 error samples, got %d", len(got))
	}
	for i, want := range []struct{ class, code string }{{"user", "resource_exhausted"}, {"server", "internal"}} {
		class, _ := got[i].Value(attribute.Key(ErrorClassKey))
		code, _ := got[i].Value("code")
		if class.AsString() != want.class || code.AsString() != want.code {
			t.Errorf("sample %d = class %q code %q, want %q %q", i, class.AsString(), code.AsString(), want.class, want.code)
		}
	}
}
