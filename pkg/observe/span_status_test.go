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

// span_status_test.go — a server span's status says whether the server failed,
// not what the wire code was. A user refused with Unavailable ("your machine is
// waking up") was an Error span under otelconnect, thousands a day in reliant
// prod, in every span-based error rate.

const unaryStatusProcedure = "/observe.test.v1.StatusService/Get"

// errMachineWaking is the refusal reliant's gateway answers while a machine
// wakes: Unavailable so the client retries, user-class because the user's
// machine state is the cause and the server behaved correctly.
func errMachineWaking() error {
	return svcerr.WithReason(svcerr.WithClass(svcerr.Unavailable("your machine is waking up"), svcerr.ClassUser), "machine_waking")
}

// unaryOutcome is what the chain made of one unary call.
type unaryOutcome struct {
	span   sdktrace.ReadOnlySpan
	errors []metricdata.DataPoint[int64]
}

// runUnary ends one unary call with handlerErr through Chain(deps) — the real
// otelconnect in the Trace slot unless deps names a Tracer instead — and
// returns its server span and its rpc.server.errors points.
func runUnary(t *testing.T, deps Deps, handlerErr error) unaryOutcome {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	if deps.Tracer == nil {
		traceSlot, err := otelconnect.NewInterceptor(otelconnect.WithTracerProvider(tp), otelconnect.WithoutMetrics())
		if err != nil {
			t.Fatal(err)
		}
		deps.Trace = traceSlot
	} else {
		deps.Tracer = tp.Tracer("span-status-test")
	}
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	deps.Meter = mp.Meter("span-status-test")
	deps.Logger = slog.New(slog.NewTextHandler(&lockedBuffer{}, nil))

	mux := http.NewServeMux()
	mux.Handle(unaryStatusProcedure, connect.NewUnaryHandler(unaryStatusProcedure,
		func(context.Context, *connect.Request[structpb.Struct]) (*connect.Response[structpb.Struct], error) {
			return nil, handlerErr
		},
		connect.WithInterceptors(Chain(deps)...)))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := connect.NewClient[structpb.Struct, structpb.Struct](srv.Client(), srv.URL+unaryStatusProcedure)
	_, err := client.CallUnary(context.Background(), connect.NewRequest(&structpb.Struct{}))
	if got, want := connect.CodeOf(err), svcerr.Code(handlerErr); got != want {
		t.Fatalf("client got %v (%v), want %v: the wire code must not change", got, err, want)
	}
	if want, ok := svcerr.ReasonOf(handlerErr); ok {
		var ce *connect.Error
		if !errors.As(err, &ce) || ce.Meta().Get(svcerr.ReasonHeader) != want {
			t.Fatalf("client reason header = %v, want %q: the wire must not change", err, want)
		}
	}
	srv.Close() // the handler has returned: its span has ended

	var out unaryOutcome
	for _, s := range rec.Ended() {
		if "/"+s.Name() == unaryStatusProcedure || s.Name() == unaryStatusProcedure {
			out.span = s
		}
	}
	if out.span == nil {
		t.Fatalf("no server span for %s among %d", unaryStatusProcedure, len(rec.Ended()))
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if sum, ok := m.Data.(metricdata.Sum[int64]); ok && m.Name == "rpc.server.errors" {
				out.errors = append(out.errors, sum.DataPoints...)
			}
		}
	}
	return out
}

func hasException(s sdktrace.ReadOnlySpan) bool {
	for _, ev := range s.Events() {
		if ev.Name == "exception" {
			return true
		}
	}
	return false
}

// A user-class Unavailable: otelconnect marks the span Error after the chain
// returns, and the OK the chain set first must survive it.
func TestServerSpan_UserClassUnavailableIsNotAnError(t *testing.T) {
	t.Parallel()
	check := func(t *testing.T, s sdktrace.ReadOnlySpan) {
		t.Helper()
		if s.Status().Code == codes.Error {
			t.Errorf("span status = %v, want not Error: the server correctly refused", s.Status())
		}
		if got := spanAttr(s, ErrorClassKey); got != "user" {
			t.Errorf("span %s = %q, want user", ErrorClassKey, got)
		}
		if got := spanAttr(s, AttrErrorReason); got != "machine_waking" {
			t.Errorf("span %s = %q, want machine_waking", AttrErrorReason, got)
		}
		if !hasException(s) {
			t.Errorf("span events = %v, want the refusal recorded as an exception event", s.Events())
		}
	}
	t.Run("unary", func(t *testing.T) {
		t.Parallel()
		o := runUnary(t, Deps{}, errMachineWaking())
		check(t, o.span)
		// rpc.server.errors keeps counting it, labelled, as it always has.
		var n int64
		for _, dp := range o.errors {
			n += dp.Value
			if v, _ := dp.Attributes.Value(ErrorClassKey); v.AsString() != "user" {
				t.Errorf("rpc.server.errors %s = %q, want user", ErrorClassKey, v.AsString())
			}
		}
		if n != 1 {
			t.Errorf("rpc.server.errors = %d, want 1 (labelled user)", n)
		}
	})
	t.Run("stream", func(t *testing.T) {
		t.Parallel()
		check(t, serverSpanOfStream(t, errMachineWaking()))
	})
}

// Every user-class error reads the same, including the codes otelconnect
// already left unset, so a span view needs one rule, not a code table.
func TestServerSpan_UserClassErrorsAreOK(t *testing.T) {
	t.Parallel()
	for name, handlerErr := range map[string]error{
		"not found":           svcerr.NotFound("chat"),
		"failed precondition": svcerr.WithReason(svcerr.FailedPrecondition("machine is suspended"), "machine_suspended"),
		"user unavailable":    errMachineWaking(),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := runUnary(t, Deps{}, handlerErr).span
			if s.Status().Code != codes.Ok {
				t.Errorf("span status = %v, want Ok", s.Status())
			}
			if got := spanAttr(s, ErrorClassKey); got != "user" {
				t.Errorf("span %s = %q, want user", ErrorClassKey, got)
			}
		})
	}
}

// A server fault with the same wire code stays an Error, now with its class.
func TestServerSpan_ServerClassUnavailableStaysAnError(t *testing.T) {
	t.Parallel()
	for name, handlerErr := range map[string]error{
		"dependency refused": connect.NewError(connect.CodeUnavailable, errors.New("database refused the connection")),
		// An outer WithClass wins over an inner user marker.
		"reclassified as a fault": svcerr.WithClass(errMachineWaking(), svcerr.ClassServer),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := runUnary(t, Deps{}, handlerErr).span
			if s.Status().Code != codes.Error {
				t.Errorf("span status = %v, want Error", s.Status())
			}
			if got := spanAttr(s, ErrorClassKey); got != "server" {
				t.Errorf("span %s = %q, want server", ErrorClassKey, got)
			}
		})
	}
	t.Run("stream", func(t *testing.T) {
		t.Parallel()
		s := serverSpanOfStream(t, svcerr.WithClass(svcerr.Unavailable("replica cannot serve"), svcerr.ClassServer))
		if s.Status().Code != codes.Error {
			t.Errorf("span status = %v, want Error", s.Status())
		}
	})
}

// A canceled-class ending (a drain, a peer cut so it redials) is OK on the
// span too: otelconnect's Error for its Unavailable no longer survives.
func TestServerSpan_CanceledClassUnavailableIsOK(t *testing.T) {
	t.Parallel()
	err := svcerr.WithClass(svcerr.Unavailable("read nothing for 1m30s; closing so the peer redials"), svcerr.ClassCanceled)
	for name, s := range map[string]sdktrace.ReadOnlySpan{
		"unary":  runUnary(t, Deps{}, err).span,
		"stream": serverSpanOfStream(t, err),
	} {
		if s.Status().Code != codes.Ok {
			t.Errorf("%s: span status = %v, want Ok", name, s.Status())
		}
		if got := spanAttr(s, ErrorClassKey); got != "canceled" {
			t.Errorf("%s: span %s = %q, want canceled", name, ErrorClassKey, got)
		}
	}
}

// An expected close keeps its own shape: OK, rpc.close_reason, no exception.
func TestServerSpan_ExpectedCloseUnchanged(t *testing.T) {
	t.Parallel()
	s := runUnary(t, Deps{}, svcerr.ExpectedClose(svcerr.Unavailable("closing"), "peer_idle")).span
	if s.Status().Code != codes.Ok {
		t.Errorf("span status = %v, want Ok", s.Status())
	}
	if got := spanAttr(s, AttrCloseReason); got != "peer_idle" {
		t.Errorf("span %s = %q, want peer_idle", AttrCloseReason, got)
	}
	if hasException(s) {
		t.Errorf("an expected close carries an exception event: %v", s.Events())
	}
}

// The opt-in counts user refusals as span errors, for the codes otelconnect
// marks and the ones it leaves unset alike; canceled stays OK.
func TestServerSpan_WithUserErrorsAsSpanErrors(t *testing.T) {
	t.Parallel()
	deps := Deps{ServerSpan: []ServerSpanOption{WithUserErrorsAsSpanErrors()}}
	for name, handlerErr := range map[string]error{
		"user unavailable": errMachineWaking(),
		"not found":        svcerr.NotFound("chat"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := runUnary(t, deps, handlerErr).span
			if s.Status().Code != codes.Error {
				t.Errorf("span status = %v, want Error under WithUserErrorsAsSpanErrors", s.Status())
			}
			if got := spanAttr(s, ErrorClassKey); got != "user" {
				t.Errorf("span %s = %q, want user", ErrorClassKey, got)
			}
		})
	}
	t.Run("canceled", func(t *testing.T) {
		t.Parallel()
		s := runUnary(t, deps, svcerr.WithClass(svcerr.Unavailable("draining"), svcerr.ClassCanceled)).span
		if s.Status().Code != codes.Ok {
			t.Errorf("span status = %v, want Ok", s.Status())
		}
	})
}

// forge's own TracingInterceptor (Deps.Tracer) reads failures the same way.
func TestServerSpan_TracerSlotReadsTheClass(t *testing.T) {
	t.Parallel()
	deps := Deps{Tracer: sdktrace.NewTracerProvider().Tracer("placeholder")} // runUnary swaps in its recorder
	if s := runUnary(t, deps, errMachineWaking()).span; s.Status().Code != codes.Ok || spanAttr(s, AttrErrorReason) != "machine_waking" {
		t.Errorf("user refusal: status %v, %s %q; want Ok, machine_waking", s.Status(), AttrErrorReason, spanAttr(s, AttrErrorReason))
	}
	if s := runUnary(t, deps, connect.NewError(connect.CodeUnavailable, errors.New("database refused"))).span; s.Status().Code != codes.Error {
		t.Errorf("server fault: status %v, want Error", s.Status())
	}
}
