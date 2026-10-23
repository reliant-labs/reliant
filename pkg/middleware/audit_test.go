package middleware

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/forge/pkg/auth"
)

// fakeSink is a test AuditSink that records every event it receives.
type fakeSink struct {
	mu        sync.Mutex
	events    []AuditEvent
	deadlines []bool // whether each Record ctx carried a deadline
	done      chan struct{}
}

func newFakeSink() *fakeSink { return &fakeSink{done: make(chan struct{}, 8)} }

func (f *fakeSink) Record(ctx context.Context, e AuditEvent) {
	_, hasDeadline := ctx.Deadline()
	f.mu.Lock()
	f.events = append(f.events, e)
	f.deadlines = append(f.deadlines, hasDeadline)
	f.mu.Unlock()
	f.done <- struct{}{}
}

func (f *fakeSink) waitOne(t *testing.T) AuditEvent {
	t.Helper()
	select {
	case <-f.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for sink.Record")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.events[len(f.events)-1]
}

// ctxWithSpan returns a context carrying a remote span with a fixed,
// valid trace id so we can assert the interceptor propagates trace_id.
func ctxWithSpan(t *testing.T) (context.Context, string) {
	t.Helper()
	tid, err := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("bad trace id: %v", err)
	}
	sid, err := trace.SpanIDFromHex("0123456789abcdef")
	if err != nil {
		t.Fatalf("bad span id: %v", err)
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	return trace.ContextWithSpanContext(context.Background(), sc), tid.String()
}

// Success path with sink + span: the sink receives a fully-populated
// event including the TraceID, and slog carries the same trace_id.
func TestAuditInterceptorWithSink_SuccessPopulatesAllFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	sink := newFakeSink()

	ic := AuditInterceptorWithSink(logger, rlClaimsFromContext, sink)

	baseCtx, wantTrace := ctxWithSpan(t)
	ctx := rlContextWithClaims(baseCtx, &auth.Claims{UserID: "u-7", Email: "u@example.com"})

	wrapped := ic.WrapUnary(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		time.Sleep(time.Millisecond)
		return nil, nil
	})
	if _, err := wrapped(ctx, connect.NewRequest(&struct{}{})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ev := sink.waitOne(t)
	// NB: req.Spec().Procedure is empty in this bare-harness (no Connect
	// client routing populates it); the interceptor copies it verbatim,
	// so we assert the fields it derives itself rather than Procedure.
	if ev.UserID != "u-7" || ev.Email != "u@example.com" {
		t.Errorf("identity not propagated: %+v", ev)
	}
	if ev.Status != "ok" {
		t.Errorf("Status=%q want ok", ev.Status)
	}
	if ev.ErrorCode != "" || ev.ErrorMessage != "" {
		t.Errorf("success event must have no error fields: %+v", ev)
	}
	if ev.TraceID != wantTrace {
		t.Errorf("TraceID=%q want %q", ev.TraceID, wantTrace)
	}
	if ev.Duration <= 0 {
		t.Errorf("Duration not measured: %v", ev.Duration)
	}
	if ev.Timestamp.IsZero() {
		t.Errorf("Timestamp not set")
	}

	out := buf.String()
	if !strings.Contains(out, `"msg":"audit.event"`) || !strings.Contains(out, `"log_type":"audit"`) {
		t.Errorf("slog audit shape changed: %s", out)
	}
	if !strings.Contains(out, `"trace_id":"`+wantTrace+`"`) {
		t.Errorf("slog must carry trace_id: %s", out)
	}
	// The slog record identifies the caller by id only. It is emitted for
	// every RPC into the general log stream, so an email there is personal
	// data in every log line; the durable sink (above) still receives it.
	if !strings.Contains(out, `"user_id":"u-7"`) {
		t.Errorf("slog must carry user_id: %s", out)
	}
	if strings.Contains(out, "u@example.com") {
		t.Errorf("slog audit record must not carry the email: %s", out)
	}
}

// Error path: the sink event records status/code/message, and slog logs
// WARN with the connect code.
func TestAuditInterceptorWithSink_ErrorPath(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	sink := newFakeSink()

	ic := AuditInterceptorWithSink(logger, rlClaimsFromContext, sink)
	wrapped := ic.WrapUnary(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("nope"))
	})
	_, _ = wrapped(context.Background(), connect.NewRequest(&struct{}{}))

	ev := sink.waitOne(t)
	if ev.Status != "error" {
		t.Errorf("Status=%q want error", ev.Status)
	}
	if ev.ErrorCode != "permission_denied" {
		t.Errorf("ErrorCode=%q want permission_denied", ev.ErrorCode)
	}
	if !strings.Contains(ev.ErrorMessage, "nope") {
		t.Errorf("ErrorMessage=%q want to contain nope", ev.ErrorMessage)
	}
	if ev.UserID != "anonymous" {
		t.Errorf("UserID=%q want anonymous", ev.UserID)
	}

	out := buf.String()
	// A rejected request is a user error: INFO, the same level the
	// operational "rpc failed" record uses for it (observe.LevelForError).
	if !strings.Contains(out, `"level":"INFO"`) || !strings.Contains(out, `"code":"permission_denied"`) {
		t.Errorf("error audit must be INFO with code: %s", out)
	}
}

// The slog-only constructor still works and never touches a sink (there
// is none) — backward compatibility guard.
func TestAuditInterceptor_SlogOnlyStillWorks(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	ic := AuditInterceptor(logger, rlClaimsFromContext)
	ctx := rlContextWithClaims(context.Background(), &auth.Claims{UserID: "u-9"})
	wrapped := ic.WrapUnary(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, nil
	})
	if _, err := wrapped(ctx, connect.NewRequest(&struct{}{})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"msg":"audit.event"`) || !strings.Contains(out, `"user_id":"u-9"`) || !strings.Contains(out, `"status":"ok"`) {
		t.Fatalf("slog-only audit record shape changed: %s", out)
	}
}

// The sink is invoked off the request path on a bounded-timeout context:
// the RPC returns immediately (never waiting on the sink) and the
// context handed to Record carries a deadline (so a wedged sink is
// always cut off rather than running unbounded).
func TestAuditInterceptorWithSink_DispatchOffPathAndBounded(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))

	// A sink that blocks until released, so if the interceptor dispatched
	// synchronously the RPC would hang.
	release := make(chan struct{})
	blocking := &blockingSink{released: release, recorded: make(chan struct{})}

	ic := AuditInterceptorWithSink(logger, nil, blocking)
	wrapped := ic.WrapUnary(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, nil
	})

	rpcStart := time.Now()
	if _, err := wrapped(context.Background(), connect.NewRequest(&struct{}{})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// RPC returned while the sink is still blocked → dispatch is off-path.
	if elapsed := time.Since(rpcStart); elapsed > time.Second {
		t.Fatalf("RPC blocked on sink dispatch: %v", elapsed)
	}
	close(release)
	select {
	case <-blocking.recorded:
	case <-time.After(2 * time.Second):
		t.Fatal("sink Record never ran")
	}
	if !blocking.hadDeadline {
		t.Fatal("sink Record ctx must carry a bounded deadline")
	}

	// Also assert the deadline via the recording fakeSink path.
	fast := newFakeSink()
	ic2 := AuditInterceptorWithSink(logger, nil, fast)
	w2 := ic2.WrapUnary(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, nil
	})
	_, _ = w2(context.Background(), connect.NewRequest(&struct{}{}))
	_ = fast.waitOne(t)
	fast.mu.Lock()
	defer fast.mu.Unlock()
	if len(fast.deadlines) == 0 || !fast.deadlines[0] {
		t.Fatalf("dispatch ctx must carry a deadline, got %v", fast.deadlines)
	}
}

// blockingSink blocks in Record until released, recording whether its
// context carried a deadline.
type blockingSink struct {
	released    <-chan struct{}
	recorded    chan struct{}
	hadDeadline bool
}

func (b *blockingSink) Record(ctx context.Context, _ AuditEvent) {
	_, b.hadDeadline = ctx.Deadline()
	<-b.released
	close(b.recorded)
}
