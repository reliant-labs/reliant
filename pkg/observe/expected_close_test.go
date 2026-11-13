package observe

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// expected_close_test.go — an ending the server chose is not an error.
//
// A daemon gateway cuts a ConnectDaemon stream whose daemon has sent nothing
// for 90s, with Unavailable so the daemon redials. Classed canceled, its log
// line already said so; its server span still ended Error (otelconnect reads
// only the wire code) and rpc.server.errors counted it, so the rpc read ~81%
// errors in obs. svcerr.ExpectedClose is the handler's statement that the
// server ended the call on purpose; the wire is unchanged.

// streamOutcome is what the default chain made of one server stream.
type streamOutcome struct {
	span   sdktrace.ReadOnlySpan
	calls  int64
	errors []metricdata.DataPoint[int64]
	logs   []map[string]any
}

// runStream ends one server stream with handlerErr through Chain — otelconnect
// in the Trace slot, a real meter, a JSON logger — and collects all three.
func runStream(t *testing.T, handlerErr error) streamOutcome {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	traceSlot, err := otelconnect.NewInterceptor(otelconnect.WithTracerProvider(tp), otelconnect.WithoutMetrics())
	if err != nil {
		t.Fatal(err)
	}
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	var buf lockedBuffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	mux := http.NewServeMux()
	mux.Handle(watchProcedure, connect.NewServerStreamHandler(watchProcedure,
		func(context.Context, *connect.Request[structpb.Struct], *connect.ServerStream[structpb.Struct]) error {
			return handlerErr
		},
		connect.WithInterceptors(Chain(Deps{Logger: logger, Trace: traceSlot, Meter: mp.Meter("expected-close-test")})...)))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := connect.NewClient[structpb.Struct, structpb.Struct](srv.Client(), srv.URL+watchProcedure)
	stream, err := client.CallServerStream(context.Background(), connect.NewRequest(&structpb.Struct{}))
	if err != nil {
		t.Fatal(err)
	}
	for stream.Receive() {
	}
	if got, want := connect.CodeOf(stream.Err()), svcerr.Code(handlerErr); got != want {
		t.Fatalf("client got %v (%v), want %v: the wire code must not change", got, stream.Err(), want)
	}
	_ = stream.Close()
	srv.Close() // the handler has returned: its span has ended, its metrics are recorded

	var out streamOutcome
	for _, s := range rec.Ended() {
		if "/"+s.Name() == watchProcedure {
			out.span = s
		}
	}
	if out.span == nil {
		t.Fatalf("no server span for %s among %d", watchProcedure, len(rec.Ended()))
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			switch m.Name {
			case "rpc.server.calls":
				for _, dp := range sum.DataPoints {
					out.calls += dp.Value
				}
			case "rpc.server.errors":
				out.errors = append(out.errors, sum.DataPoints...)
			}
		}
	}
	out.logs = records(t, buf.snapshot(), "stream failed")
	return out
}

func TestExpectedClose_IsNotAnErrorInSpanMetricsOrLogs(t *testing.T) {
	t.Parallel()
	o := runStream(t, svcerr.ExpectedClose(
		svcerr.Unavailable("read nothing for 1m30s; closing so the daemon redials"), "peer_idle"))

	// The span: OK, although otelconnect sets Error for Unavailable after the
	// chain returns — OK is final.
	if o.span.Status().Code != codes.Ok {
		t.Errorf("span status = %v, want Ok: the server ended the call on purpose", o.span.Status())
	}
	if got := spanAttr(o.span, AttrCloseReason); got != "peer_idle" {
		t.Errorf("span %s = %q, want peer_idle", AttrCloseReason, got)
	}
	if got := spanAttr(o.span, ErrorClassKey); got != "canceled" {
		t.Errorf("span %s = %q, want canceled", ErrorClassKey, got)
	}
	for _, ev := range o.span.Events() {
		if ev.Name == "exception" {
			t.Errorf("span carries an exception event for an expected close: %v", ev.Attributes)
		}
	}

	// The metric: the call counts, the error does not.
	if o.calls != 1 {
		t.Errorf("rpc.server.calls = %d, want 1", o.calls)
	}
	var errs int64
	for _, dp := range o.errors {
		errs += dp.Value
	}
	if errs != 0 {
		t.Errorf("rpc.server.errors counted %d for an expected close, want 0", errs)
	}

	// The log: an expected outcome, below anything that pages, saying why.
	if len(o.logs) != 1 {
		t.Fatalf("want one stream record, got %d: %v", len(o.logs), o.logs)
	}
	rec := o.logs[0]
	if rec["level"] != "INFO" || rec["expected"] != true || rec[CloseReasonKey] != "peer_idle" {
		t.Errorf("stream record level/expected/%s = %v/%v/%v, want INFO/true/peer_idle",
			CloseReasonKey, rec["level"], rec["expected"], rec[CloseReasonKey])
	}
}

// A real Unavailable — the same wire code, nothing marking it expected — is
// still an error everywhere.
func TestExpectedClose_GenuineUnavailableStillCounts(t *testing.T) {
	t.Parallel()
	for name, handlerErr := range map[string]error{
		"dependency refused": connect.NewError(connect.CodeUnavailable, errors.New("database refused the connection")),
		// An outer WithClass wins over the marker, as it does for Classify.
		"reclassified as a fault": svcerr.WithClass(
			svcerr.ExpectedClose(svcerr.Unavailable("closing"), "peer_idle"), svcerr.ClassServer),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			o := runStream(t, handlerErr)
			if o.span.Status().Code != codes.Error {
				t.Errorf("span status = %v, want Error", o.span.Status())
			}
			if got := spanAttr(o.span, AttrCloseReason); got != "" {
				t.Errorf("span %s = %q on a failure", AttrCloseReason, got)
			}
			var errs int64
			for _, dp := range o.errors {
				errs += dp.Value
				if v, _ := dp.Attributes.Value(ErrorClassKey); v.AsString() != "server" {
					t.Errorf("rpc.server.errors %s = %q, want server", ErrorClassKey, v.AsString())
				}
			}
			if errs != 1 {
				t.Errorf("rpc.server.errors = %d, want 1", errs)
			}
			if len(o.logs) != 1 || o.logs[0]["level"] != "ERROR" || o.logs[0]["expected"] != nil {
				t.Errorf("want one ERROR stream record not marked expected, got %v", o.logs)
			}
		})
	}
}

// RecordSpanError on forge's own spans (TraceCall, the per-package observe.go
// wrappers) treats an expected close the same way.
func TestRecordSpanError_ExpectedClose(t *testing.T) {
	t.Parallel()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	_, span := tp.Tracer("t").Start(context.Background(), "op")
	RecordSpanError(span, svcerr.ExpectedClose(svcerr.Unavailable("bye"), ""))
	span.SetStatus(codes.Error, "a later layer reading the wire code") // ignored: OK is final
	span.End()

	s := rec.Ended()[0]
	if s.Status().Code != codes.Ok {
		t.Errorf("status = %v, want Ok", s.Status())
	}
	if got := spanAttr(s, AttrCloseReason); got != svcerr.CloseReasonExpected {
		t.Errorf("%s = %q, want the default %q", AttrCloseReason, got, svcerr.CloseReasonExpected)
	}
}
