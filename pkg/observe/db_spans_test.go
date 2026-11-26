package observe

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// emit ends one span of the given shape, d long, through a provider whose
// processor is the filter over a recorder, and reports whether it was exported.
func emit(t *testing.T, policy DBSpans, name string, kind trace.SpanKind, db bool, d time.Duration, failed bool) bool {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(newDBSpanFilter(rec, policy)))
	start := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	opts := []trace.SpanStartOption{trace.WithSpanKind(kind), trace.WithTimestamp(start)}
	if db {
		opts = append(opts, trace.WithAttributes(attribute.String("db.system.name", "postgresql")))
	}
	_, span := tp.Tracer("test").Start(context.Background(), name, opts...)
	if failed {
		span.RecordError(errors.New("boom"))
		span.SetStatus(codes.Error, "boom")
	}
	span.End(trace.WithTimestamp(start.Add(d)))
	return len(rec.Ended()) == 1
}

func TestDBSpanFilter(t *testing.T) {
	t.Parallel()
	policy := DBSpans{OmitTransactions: true, MinDuration: 20 * time.Millisecond}
	client := trace.SpanKindClient
	cases := []struct {
		name   string
		policy DBSpans
		span   string
		kind   trace.SpanKind
		db     bool
		d      time.Duration
		failed bool
		want   bool
	}{
		{"zero policy keeps a fast query", DBSpans{}, "SELECT chats", client, true, time.Millisecond, false, true},
		{"zero policy keeps BEGIN", DBSpans{}, "BEGIN", client, true, time.Millisecond, false, true},
		{"fast query dropped", policy, "SELECT chats", client, true, 19 * time.Millisecond, false, false},
		{"slow query kept", policy, "SELECT chats", client, true, 20 * time.Millisecond, false, true},
		{"failed fast query kept", policy, "SELECT chats", client, true, time.Millisecond, true, true},
		{"BEGIN dropped", policy, "BEGIN", client, true, time.Second, false, false},
		{"COMMIT dropped even when slow", policy, "COMMIT", client, true, time.Second, false, false},
		{"failed COMMIT kept", policy, "COMMIT", client, true, time.Millisecond, true, true},
		{"slow ROLLBACK kept", policy, "ROLLBACK", client, true, time.Second, false, true},
		{"BEGIN kept when only a duration is set", DBSpans{MinDuration: 20 * time.Millisecond}, "BEGIN", client, true, time.Second, false, true},
		{"fast BEGIN dropped by duration alone", DBSpans{MinDuration: 20 * time.Millisecond}, "BEGIN", client, true, time.Millisecond, false, false},
		{"fast query kept when only transactions are omitted", DBSpans{OmitTransactions: true}, "SELECT chats", client, true, time.Millisecond, false, true},
		{"non-db client span untouched", policy, "GET /v1/x", client, false, time.Millisecond, false, true},
		{"non-client db span untouched", policy, "BEGIN", trace.SpanKindInternal, true, time.Millisecond, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := emit(t, c.policy, c.span, c.kind, c.db, c.d, c.failed); got != c.want {
				t.Fatalf("exported = %v, want %v", got, c.want)
			}
		})
	}
}

func TestInactiveDBSpanPolicyIsTheBareProcessor(t *testing.T) {
	t.Parallel()
	rec := tracetest.NewSpanRecorder()
	if p := newDBSpanFilter(rec, DBSpans{}); p != sdktrace.SpanProcessor(rec) {
		t.Fatalf("zero policy wrapped the processor: %T", p)
	}
}

func TestResolveDBSpansEnvWinsOverConfig(t *testing.T) {
	t.Setenv(EnvDBSpanMinDuration, "50ms")
	t.Setenv(EnvDBSpanOmitTx, "false")
	p, warns := resolveDBSpans(Config{DBSpans: DBSpans{OmitTransactions: true, MinDuration: 20 * time.Millisecond}})
	if len(warns) != 0 || p.MinDuration != 50*time.Millisecond || p.OmitTransactions {
		t.Fatalf("policy = %+v, warns = %v", p, warns)
	}
}

func TestResolveDBSpansIgnoresInvalidEnv(t *testing.T) {
	t.Setenv(EnvDBSpanMinDuration, "fast")
	t.Setenv(EnvDBSpanOmitTx, "maybe")
	want := DBSpans{OmitTransactions: true, MinDuration: 20 * time.Millisecond}
	p, warns := resolveDBSpans(Config{DBSpans: want})
	if len(warns) != 2 || p != want {
		t.Fatalf("policy = %+v, warns = %v", p, warns)
	}
}
