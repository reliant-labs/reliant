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
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// span_canceled_test.go — the Connect server span of a call nobody has to act
// on is not counted as a server error.
//
// The Trace slot is otelconnect, which marks a server span Error for any
// Unavailable. A daemon gateway's only rpc is a bidi stream that ends as
// Unavailable twice over by design: a rollout's drain, and the sweeper cutting a
// stream whose daemon went quiet (a laptop asleep) so it redials. Both are
// svcerr class canceled. In prod the gateway read 100% errors in `forge obs
// status` (197 reaped + 10 drained streams in 48h, every one an Error span),
// while its logs, which honour the class, showed none.

// serverSpanOfStream runs one server stream whose handler returns handlerErr
// through Chain with otelconnect in the Trace slot, and returns its server span.
func serverSpanOfStream(t *testing.T, handlerErr error) sdktrace.ReadOnlySpan {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	trace, err := otelconnect.NewInterceptor(otelconnect.WithTracerProvider(tp), otelconnect.WithoutMetrics())
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(&lockedBuffer{}, nil))

	mux := http.NewServeMux()
	mux.Handle(watchProcedure, connect.NewServerStreamHandler(watchProcedure,
		func(context.Context, *connect.Request[structpb.Struct], *connect.ServerStream[structpb.Struct]) error {
			return handlerErr
		},
		connect.WithInterceptors(Chain(Deps{Logger: logger, Trace: trace})...)))
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
		t.Fatalf("client got %v (%v), want %v: the wire code is not changed", got, stream.Err(), want)
	}
	_ = stream.Close()
	srv.Close() // the handler has returned and its span has ended

	// otelconnect names the span after the procedure without its leading slash.
	for _, s := range rec.Ended() {
		if "/"+s.Name() == watchProcedure {
			return s
		}
	}
	t.Fatalf("no server span for %s among %d", watchProcedure, len(rec.Ended()))
	return nil
}

func spanAttr(s sdktrace.ReadOnlySpan, key string) string {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.Emit()
		}
	}
	return ""
}

func TestRequestSpan_CanceledClassEndingIsNotAServerError(t *testing.T) {
	t.Parallel()
	for name, handlerErr := range map[string]error{
		// serverkit's StreamDrainInterceptor, for a RedialOnDrain procedure.
		"drained": svcerr.ToConnect(svcerr.ErrServerDraining),
		// A handler's own drain status (a daemon gateway's Close).
		"handler drain": connect.NewError(connect.CodeUnavailable, svcerr.Draining("gateway draining: reconnect")),
		// A stream reaped because its peer went quiet: Unavailable on the wire
		// (the peer must redial), canceled by class (nobody here must act).
		"peer went quiet": svcerr.WithClass(svcerr.Unavailable("read nothing for 1m30s; closing so the peer redials"), svcerr.ClassCanceled),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := serverSpanOfStream(t, handlerErr)
			if got := spanAttr(s, ErrorClassKey); got != "canceled" {
				t.Errorf("server span error_class = %q, want canceled: obsquery's error expressions exclude it by that", got)
			}
			if len(s.Events()) == 0 || s.Events()[0].Name != "exception" {
				t.Errorf("server span events = %v, want the error recorded as an exception event", s.Events())
			}
		})
	}
}

// A real server fault with the same wire code keeps everything otelconnect
// says: it is an error, and nothing marks it canceled.
func TestRequestSpan_ServerFaultStaysAnError(t *testing.T) {
	t.Parallel()
	s := serverSpanOfStream(t, connect.NewError(connect.CodeUnavailable, errors.New("database refused the connection")))
	if s.Status().Code != codes.Error {
		t.Errorf("server fault span status = %v, want Error", s.Status())
	}
	if got := spanAttr(s, ErrorClassKey); got == "canceled" {
		t.Errorf("a server fault was classed canceled")
	}
}
