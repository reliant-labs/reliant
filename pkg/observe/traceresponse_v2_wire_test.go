package observe

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

const (
	trWireUnary  = "/observe.v1.TraceWire/Unary"
	trWireStream = "/observe.v1.TraceWire/Stream"
)

// trWireSpan is the fixed server span both majors' servers run under, so the
// traceresponse values are comparable byte for byte.
var trWireSpan = trace.NewSpanContext(trace.SpanContextConfig{
	TraceID:    trace.TraceID{0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6, 0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36},
	SpanID:     trace.SpanID{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7},
	TraceFlags: trace.FlagsSampled,
})

// trWireV1 serves a unary and a server-streaming procedure on connect-go v1
// behind the span, TraceResponseInterceptor and svcerr — observe.Chain's
// order. Both fail with svcerr.NotFound when fail is set.
func trWireV1(fail bool) http.Handler {
	span := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			return next(trace.ContextWithSpanContext(ctx, trWireSpan), req)
		}
	})
	opts := connect.WithInterceptors(span, streamSpanV1{}, TraceResponseInterceptor(), svcerr.Interceptor())
	mux := http.NewServeMux()
	mux.Handle(trWireUnary, connect.NewUnaryHandler(trWireUnary,
		func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			if fail {
				return nil, svcerr.NotFound("note")
			}
			return connect.NewResponse(&emptypb.Empty{}), nil
		}, opts))
	mux.Handle(trWireStream, connect.NewServerStreamHandler(trWireStream,
		func(_ context.Context, _ *connect.Request[emptypb.Empty], stream *connect.ServerStream[emptypb.Empty]) error {
			if err := stream.Send(&emptypb.Empty{}); err != nil {
				return err
			}
			if fail {
				return svcerr.NotFound("note")
			}
			return nil
		}, opts))
	return mux
}

// streamSpanV1 puts the fixed span on a v1 stream's context (a
// UnaryInterceptorFunc does not wrap streams).
type streamSpanV1 struct{}

func (streamSpanV1) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc { return next }
func (streamSpanV1) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (streamSpanV1) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(trace.ContextWithSpanContext(ctx, trWireSpan), conn)
	}
}

// trWireV2 is trWireV1 on connect-go v2.
func trWireV2(fail bool) http.Handler {
	span := func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			return next(trace.ContextWithSpanContext(ctx, trWireSpan), spec, stream)
		}
	}
	server := connectv2.NewServer(span, TraceResponseInterceptorV2(), svcerr.InterceptorV2())
	server.Register(
		connectv2.Method{Spec: connectv2.Spec{StreamType: connectv2.StreamTypeUnary, Procedure: trWireUnary},
			Handler: func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
				var req emptypb.Empty
				if err := stream.Receive(&req); err != nil {
					return err
				}
				if fail {
					return svcerr.NotFound("note")
				}
				return stream.Send(&emptypb.Empty{})
			}},
		connectv2.Method{Spec: connectv2.Spec{StreamType: connectv2.StreamTypeServer, Procedure: trWireStream},
			Handler: func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
				var req emptypb.Empty
				if err := stream.Receive(&req); err != nil {
					return err
				}
				if err := stream.Send(&emptypb.Empty{}); err != nil {
					return err
				}
				if fail {
					return svcerr.NotFound("note")
				}
				return nil
			}},
	)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)
	return mux
}

func trEnvelope(payload []byte) []byte {
	out := make([]byte, 5, 5+len(payload))
	binary.BigEndian.PutUint32(out[1:], uint32(len(payload)))
	return append(out, payload...)
}

type trWireExchange struct {
	status          int
	header, trailer http.Header
	body            []byte
}

// trRawCall makes one raw HTTP/2 call and keeps the response headers and
// trailers apart — a client library would merge them.
func trRawCall(t *testing.T, srv *httptest.Server, procedure, contentType string, body []byte) trWireExchange {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+procedure, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Header.Del("Date")
	return trWireExchange{status: resp.StatusCode, header: resp.Header, trailer: resp.Trailer, body: data}
}

func trDiff(what string, a, b http.Header) []string {
	var diffs []string
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	for k := range keys {
		if !slices.Equal(a.Values(k), b.Values(k)) {
			diffs = append(diffs, fmt.Sprintf("%s %s: v1 %q, v2 %q", what, k, a.Values(k), b.Values(k)))
		}
	}
	slices.Sort(diffs)
	return diffs
}

func trH2Server(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// TestTraceResponseInterceptorV2_WireMatchesV1 compares the raw response
// headers and trailers, each on its own, of v1's TraceResponseInterceptor
// and TraceResponseInterceptorV2 under one fixed server span, for a unary
// and a streaming call that succeed and fail, over Connect, gRPC and
// gRPC-Web. On a failed unary call v1 sends traceresponse with the error's
// metadata: a response header for Connect, a trailer for gRPC and gRPC-Web.
func TestTraceResponseInterceptorV2_WireMatchesV1(t *testing.T) {
	want := TraceResponseValue(trWireSpan)
	protocols := []struct {
		name, procedure, contentType string
		body                         []byte
		// where a failed call's traceresponse goes on v1
		failInTrailer bool
	}{
		{"connect unary", trWireUnary, "application/json", []byte("{}"), false},
		{"connect server stream", trWireStream, "application/connect+json", trEnvelope([]byte("{}")), false},
		{"grpc unary", trWireUnary, "application/grpc", trEnvelope(nil), true},
		{"grpc server stream", trWireStream, "application/grpc", trEnvelope(nil), false},
		{"grpc-web unary", trWireUnary, "application/grpc-web+proto", trEnvelope(nil), false},
		{"grpc-web server stream", trWireStream, "application/grpc-web+proto", trEnvelope(nil), false},
	}
	for _, fail := range []bool{false, true} {
		v1, v2 := trH2Server(t, trWireV1(fail)), trH2Server(t, trWireV2(fail))
		for _, p := range protocols {
			t.Run(fmt.Sprintf("fail=%v/%s", fail, p.name), func(t *testing.T) {
				a := trRawCall(t, v1, p.procedure, p.contentType, p.body)
				b := trRawCall(t, v2, p.procedure, p.contentType, p.body)
				var diffs []string
				if a.status != b.status {
					diffs = append(diffs, fmt.Sprintf("status: v1 %d, v2 %d", a.status, b.status))
				}
				diffs = append(diffs, trDiff("header", a.header, b.header)...)
				diffs = append(diffs, trDiff("trailer", a.trailer, b.trailer)...)
				if !bytes.Equal(a.body, b.body) {
					diffs = append(diffs, fmt.Sprintf("body: v1 %q, v2 %q", a.body, b.body))
				}
				if len(diffs) > 0 {
					t.Errorf("the v2 wire differs from v1's:\n  %s", strings.Join(diffs, "\n  "))
				}
				// Pin v1 itself, so the parity cannot agree on "absent".
				if fail && p.failInTrailer {
					if got := a.trailer.Get(TraceResponseHeader); got != want {
						t.Errorf("v1 failed unary gRPC: trailer traceresponse = %q, want %q", got, want)
					}
					if got := a.header.Get(TraceResponseHeader); got != "" {
						t.Errorf("v1 failed unary gRPC: header traceresponse = %q, want none", got)
					}
				} else if p.name == "connect unary" || !fail || strings.HasSuffix(p.name, "stream") {
					if got := a.header.Get(TraceResponseHeader); got != want {
						t.Errorf("v1: header traceresponse = %q, want %q", got, want)
					}
				}
			})
		}
	}
}
