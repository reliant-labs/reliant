package observe_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/forge/pkg/observe"
)

// serveTraced mounts one unary procedure behind otelconnect (recording into
// rec) and TraceResponseInterceptor, in observe.Chain's order.
func serveTraced(t *testing.T, rec *tracetest.SpanRecorder, fail bool) *httptest.Server {
	t.Helper()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otelIC, err := otelconnect.NewInterceptor(otelconnect.WithTracerProvider(tp))
	if err != nil {
		t.Fatal(err)
	}
	h := connect.NewUnaryHandler("/test.v1.Svc/Do",
		func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			if fail {
				return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("not yet"))
			}
			return connect.NewResponse(&emptypb.Empty{}), nil
		},
		connect.WithInterceptors(otelIC, observe.TraceResponseInterceptor()),
	)
	mux := http.NewServeMux()
	mux.Handle("/test.v1.Svc/Do", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func serverSpanValue(t *testing.T, rec *tracetest.SpanRecorder) string {
	t.Helper()
	for _, s := range rec.Ended() {
		if s.SpanKind() == trace.SpanKindServer {
			return observe.TraceResponseValue(s.SpanContext())
		}
	}
	t.Fatal("no server span recorded")
	return ""
}

func TestTraceResponseNamesTheServerSpan(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		rec := tracetest.NewSpanRecorder()
		srv := serveTraced(t, rec, false)
		client := connect.NewClient[emptypb.Empty, emptypb.Empty](srv.Client(), srv.URL+"/test.v1.Svc/Do")
		resp, err := client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{}))
		if err != nil {
			t.Fatal(err)
		}
		got := resp.Header().Get(observe.TraceResponseHeader)
		if want := serverSpanValue(t, rec); got == "" || got != want {
			t.Fatalf("traceresponse = %q, want the server span %q", got, want)
		}
	})
	t.Run("error", func(t *testing.T) {
		rec := tracetest.NewSpanRecorder()
		srv := serveTraced(t, rec, true)
		client := connect.NewClient[emptypb.Empty, emptypb.Empty](srv.Client(), srv.URL+"/test.v1.Svc/Do")
		_, err := client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{}))
		var ce *connect.Error
		if !errors.As(err, &ce) || ce.Code() != connect.CodeFailedPrecondition {
			t.Fatalf("err = %v, want failed_precondition", err)
		}
		got := ce.Meta().Get(observe.TraceResponseHeader)
		if want := serverSpanValue(t, rec); got == "" || got != want {
			t.Fatalf("failed call: traceresponse = %q, want the server span %q", got, want)
		}
	})
}

func TestTraceResponseValue(t *testing.T) {
	if v := observe.TraceResponseValue(trace.SpanContext{}); v != "" {
		t.Errorf("invalid span context rendered %q, want empty", v)
	}
	tid, _ := trace.TraceIDFromHex("0af7651916cd43dd8448eb211c80319c")
	sid, _ := trace.SpanIDFromHex("b7ad6b7169203331")
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled})
	if v, want := observe.TraceResponseValue(sc), "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"; v != want {
		t.Errorf("TraceResponseValue = %q, want %q", v, want)
	}
}
