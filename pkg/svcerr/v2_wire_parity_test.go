package svcerr_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
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
	connectv2proto "connectrpc.com/connect/v2/connectproto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// parityCase is one handler failure, spelled for each major. The two
// spellings are the same error unless the error is a connect type itself,
// which each major spells its own way (a forwarded remote error, an error the
// handler built).
type parityCase struct {
	name   string
	v1, v2 func() error
	// preset is response metadata already on the HTTP response when the RPC
	// runs — what an HTTP middleware in front of the server writes. It is
	// the same for both majors; error metadata is merged after it.
	preset http.Header
	// streamTrailer is response trailer metadata the streaming handler sets
	// (each major its own way) before it fails; error metadata on a stream
	// is merged into the same end-of-stream metadata after it.
	streamTrailer http.Header
}

func sameError(name string, err func() error) parityCase {
	return parityCase{name: name, v1: err, v2: err}
}

// repeated is a v1 *connect.Error a handler built with a metadata value
// repeated: v1 sends every value, in order, so v2 must too.
func repeated() error {
	ce := connect.NewError(connect.CodePermissionDenied, errors.New("no"))
	ce.Meta().Add("X-Value", "same")
	ce.Meta().Add("X-Value", "same")
	ce.Meta().Add("X-Value", "other")
	return ce
}

func parityCases(t *testing.T) []parityCase {
	t.Helper()
	detailV1, err := connect.NewErrorDetail(wrapperspb.String("sku"))
	if err != nil {
		t.Fatal(err)
	}
	detailV2, err := connectv2proto.NewErrorDetail(wrapperspb.String("sku"))
	if err != nil {
		t.Fatal(err)
	}
	remoteV1 := func() *connect.Error {
		ce := connect.NewWireError(connect.CodeNotFound, errors.New("upstream says no"))
		ce.AddDetail(detailV1)
		return ce
	}
	remoteV2 := func() *connectv2.Error {
		return connectv2.NewError(connectv2.CodeNotFound, "upstream says no").WithDetail(detailV2).WithRemote()
	}
	// A real upstream: the error a handler gets back from calling another
	// Connect service, on each major — with the upstream's response
	// metadata on it, which forwarding must not pass on.
	upstreamV1 := httptest.NewServer(muxV1(func() error {
		ce := connect.NewError(connect.CodeNotFound, errors.New("upstream says no"))
		ce.AddDetail(detailV1)
		ce.Meta().Set("X-Upstream", "yes")
		return ce
	}))
	t.Cleanup(upstreamV1.Close)
	upstreamV2 := httptest.NewServer(muxV2(func() error {
		return svcerr.WithMetadata(connectv2.NewError(connectv2.CodeNotFound, "upstream says no").WithDetail(detailV2), "x-upstream", "yes")
	}, svcerr.InterceptorV2()))
	t.Cleanup(upstreamV2.Close)
	callUpstreamV1 := func() error {
		_, err := connect.NewClient[emptypb.Empty, emptypb.Empty](upstreamV1.Client(), upstreamV1.URL+unaryProcedure).
			CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{}))
		if !connect.IsWireError(err) {
			t.Errorf("v1 upstream returned %v, want a wire error", err)
		}
		return err
	}
	callUpstreamV2 := func() error {
		err := connectv2.NewClient(connecthttp.NewTransport(upstreamV2.Client(), upstreamV2.URL)).
			CallUnary(context.Background(), unarySpecV2, &emptypb.Empty{}, &emptypb.Empty{})
		if e, ok := errors.AsType[*connectv2.Error](err); !ok || !e.IsRemote() {
			t.Errorf("v2 upstream returned %v, want a remote error", err)
		}
		return err
	}
	same := http.Header{"X-Value": {"same"}}
	return []parityCase{
		sameError("kind", func() error { return svcerr.PermissionDenied("admin only") }),
		sameError("kind with reason and metadata", func() error {
			return svcerr.WithMetadata(svcerr.WithReason(svcerr.NotFound("note"), "note_missing"), "x-forge-error-scopes", "a,b")
		}),
		sameError("wrapped kind", func() error { return svcerr.Wrap(svcerr.FailedPrecondition("not yet")) }),
		sameError("bare sentinel", func() error { return svcerr.ErrAborted }),
		sameError("unrecognised error", func() error {
			return errors.New(`ERROR: relation "accounts" does not exist dsn=postgres://app:s3cr3t@db/prod`)
		}),
		sameError("context deadline", func() error { return context.DeadlineExceeded }),
		sameError("with cause", func() error {
			return svcerr.WithCause(svcerr.Unavailable("billing is unavailable"), remoteV1())
		}),
		sameError("with detail", func() error {
			return svcerr.WithDetail(svcerr.InvalidArgument("bad sku"), wrapperspb.String("sku"))
		}),
		sameError("scaffold stub", func() error { return svcerr.ScaffoldStub("Do") }),
		{
			name: "handler-built connect error",
			v1:   func() error { return connect.NewError(connect.CodeFailedPrecondition, errors.New("push rejected")) },
			v2:   func() error { return connectv2.NewError(connectv2.CodeFailedPrecondition, "push rejected") },
		},
		{
			name: "forwarded remote error",
			v1:   func() error { return remoteV1() },
			v2:   func() error { return remoteV2() },
		},
		{
			name: "forwarded remote error with reason",
			v1:   func() error { return svcerr.WithReason(remoteV1(), "ignored_on_remote") },
			v2:   func() error { return svcerr.WithReason(remoteV2(), "ignored_on_remote") },
		},
		{
			name: "remote error from a real upstream call",
			v1:   callUpstreamV1,
			v2:   callUpstreamV2,
		},
		{
			name: "wrapped remote error from a real upstream call",
			v1:   func() error { return fmt.Errorf("inventory: %w", callUpstreamV1()) },
			v2:   func() error { return fmt.Errorf("inventory: %w", callUpstreamV2()) },
		},
		sameError("repeated metadata values", repeated),
		{
			name:   "metadata beside the same value already on the response",
			v1:     repeated,
			v2:     repeated,
			preset: same,
		},
		{
			name:   "reason beside a reason already on the response",
			v1:     func() error { return svcerr.WithReason(svcerr.NotFound("note"), "note_missing") },
			v2:     func() error { return svcerr.WithReason(svcerr.NotFound("note"), "note_missing") },
			preset: http.Header{svcerr.ReasonHeader: {"from_the_edge"}},
		},
		{
			name:          "metadata beside the same value in the stream's trailers",
			v1:            repeated,
			v2:            repeated,
			streamTrailer: same,
		},
	}
}

// withPreset writes preset onto every response before h runs.
func withPreset(h http.Handler, preset http.Header) http.Handler {
	if len(preset) == 0 {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, vs := range preset {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		h.ServeHTTP(w, r)
	})
}

// parityServerV1 is the reference: tc's handler failure on connect-go v1
// behind svcerr.Interceptor. With reject set, an interceptor in front of the
// handler fails the call with svcerr.Wrap(tc.v1()) instead, never calling
// it — what a v1 auth interceptor does.
func parityServerV1(tc parityCase, reject bool) http.Handler {
	ics := []connect.Interceptor{svcerr.Interceptor()}
	if reject {
		ics = append([]connect.Interceptor{rejectingV1{tc.v1}}, ics...)
	}
	opts := connect.WithInterceptors(ics...)
	mux := http.NewServeMux()
	mux.Handle(unaryProcedure, connect.NewUnaryHandler(unaryProcedure,
		func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			return nil, tc.v1()
		}, opts))
	mux.Handle(streamProcedure, connect.NewServerStreamHandler(streamProcedure,
		func(_ context.Context, _ *connect.Request[emptypb.Empty], stream *connect.ServerStream[emptypb.Empty]) error {
			for k, vs := range tc.streamTrailer {
				for _, v := range vs {
					stream.ResponseTrailer().Add(k, v)
				}
			}
			if err := stream.Send(&emptypb.Empty{}); err != nil {
				return err
			}
			return tc.v1()
		}, opts))
	return withPreset(mux, tc.preset)
}

type rejectingV1 struct{ err func() error }

func (r rejectingV1) WrapUnary(connect.UnaryFunc) connect.UnaryFunc {
	return func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, svcerr.Wrap(r.err())
	}
}

func (r rejectingV1) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (r rejectingV1) WrapStreamingHandler(connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(context.Context, connect.StreamingHandlerConn) error { return svcerr.Wrap(r.err()) }
}

// rejectingV2 fails every call with svcerr.RejectV2 without calling next.
func rejectingV2(err func() error) connectv2.ServerInterceptor {
	return func(connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, _ connectv2.Spec, _ connectv2.ServerStream) error {
			return svcerr.RejectV2(ctx, err())
		}
	}
}

// parityServerV2 is tc's handler failure on connect-go v2 behind
// interceptors.
func parityServerV2(tc parityCase, interceptors ...connectv2.ServerInterceptor) http.Handler {
	server := connectv2.NewServer(interceptors...)
	server.Register(
		connectv2.Method{Spec: unarySpecV2, Handler: func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
			var req emptypb.Empty
			if err := stream.Receive(&req); err != nil {
				return err
			}
			return tc.v2()
		}},
		connectv2.Method{Spec: streamSpecV2, Handler: func(ctx context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
			var req emptypb.Empty
			if err := stream.Receive(&req); err != nil {
				return err
			}
			if info, ok := connectv2.CallInfoForServerContext(ctx); ok {
				for k, vs := range tc.streamTrailer {
					for _, v := range vs {
						info.ResponseTrailer().Add(k, v)
					}
				}
			}
			if err := stream.Send(&emptypb.Empty{}); err != nil {
				return err
			}
			return tc.v2()
		}},
	)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)
	return withPreset(mux, tc.preset)
}

// passThroughV2 is an unrelated interceptor sitting between two copies of
// svcerr.InterceptorV2.
func passThroughV2(next connectv2.ServerFunc) connectv2.ServerFunc { return next }

// wireExchange is what a client reads off the wire, minus what varies
// between two responses whoever serves them (Date).
type wireExchange struct {
	status  int
	header  http.Header
	trailer http.Header
	body    []byte
}

type wireProtocol struct {
	name, procedure, contentType string
	body                         []byte
}

func envelope(payload []byte) []byte {
	out := make([]byte, 5, 5+len(payload))
	binary.BigEndian.PutUint32(out[1:], uint32(len(payload)))
	return append(out, payload...)
}

// wireProtocols are the ways an old client calls the two procedures. The
// gRPC ones read the HTTP/2 trailers on their own, so metadata that moved
// between headers and trailers shows up as a difference.
var wireProtocols = []wireProtocol{
	{"connect unary", unaryProcedure, "application/json", []byte("{}")},
	{"connect server stream", streamProcedure, "application/connect+json", envelope([]byte("{}"))},
	{"grpc unary", unaryProcedure, "application/grpc", envelope(nil)},
	{"grpc server stream", streamProcedure, "application/grpc", envelope(nil)},
	{"grpc-web unary", unaryProcedure, "application/grpc-web+proto", envelope(nil)},
	{"grpc-web server stream", streamProcedure, "application/grpc-web+proto", envelope(nil)},
}

// h2Server serves h over TLS with HTTP/2, which gRPC needs.
func h2Server(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func rawCall(t *testing.T, srv *httptest.Server, p wireProtocol) wireExchange {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+p.procedure, bytes.NewReader(p.body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", p.contentType)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Header.Del("Date")
	return wireExchange{status: resp.StatusCode, header: resp.Header, trailer: resp.Trailer, body: body}
}

// diffMetadata lists every key whose values — order and multiplicity
// included — differ between a and b.
func diffMetadata(what string, a, b http.Header) []string {
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

// assertSameExchange fails unless both exchanges are identical: status,
// headers and trailers (each compared on its own) and body.
func assertSameExchange(t *testing.T, v1, v2 wireExchange) {
	t.Helper()
	var diffs []string
	if v1.status != v2.status {
		diffs = append(diffs, fmt.Sprintf("status: v1 %d, v2 %d", v1.status, v2.status))
	}
	diffs = append(diffs, diffMetadata("header", v1.header, v2.header)...)
	diffs = append(diffs, diffMetadata("trailer", v1.trailer, v2.trailer)...)
	if !bytes.Equal(v1.body, v2.body) {
		diffs = append(diffs, fmt.Sprintf("body: v1 %q, v2 %q", v1.body, v2.body))
	}
	if len(diffs) > 0 {
		t.Errorf("the v2 wire differs from v1's:\n  %s", strings.Join(diffs, "\n  "))
	}
}

// TestWireParity_InterceptorV1AndV2 is the proof that moving a server from
// connect-go v1 + svcerr.Interceptor to connect-go v2 + svcerr.InterceptorV2
// changes nothing a client receives: for each handler failure, the raw
// response — HTTP status, response headers, response trailers and body —
// is identical over Connect unary and streaming, gRPC and gRPC-Web, with
// InterceptorV2 installed once or twice (another interceptor between the
// copies). It also proves svcerr.RejectV2: an interceptor that rejects with
// it, alone or inside an InterceptorV2, sends what a v1 interceptor
// rejecting with the same error sends.
func TestWireParity_InterceptorV1AndV2(t *testing.T) {
	for _, tc := range parityCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			v1 := h2Server(t, parityServerV1(tc, false))
			v1Reject := h2Server(t, parityServerV1(tc, true))
			variants := []struct {
				name string
				ref  *httptest.Server
				srv  *httptest.Server
			}{
				{"interceptor", v1, h2Server(t, parityServerV2(tc, svcerr.InterceptorV2()))},
				{"interceptor twice", v1, h2Server(t, parityServerV2(tc,
					svcerr.WithInterceptorV2([]connectv2.ServerInterceptor{svcerr.InterceptorV2(), passThroughV2})...))},
			}
			if tc.streamTrailer == nil {
				// A rejection never reaches the handler that sets them.
				variants = append(variants,
					struct {
						name string
						ref  *httptest.Server
						srv  *httptest.Server
					}{"RejectV2", v1Reject, h2Server(t, parityServerV2(tc, rejectingV2(tc.v2)))},
					struct {
						name string
						ref  *httptest.Server
						srv  *httptest.Server
					}{"RejectV2 inside InterceptorV2", v1Reject, h2Server(t, parityServerV2(tc,
						svcerr.InterceptorV2(), rejectingV2(tc.v2), svcerr.InterceptorV2()))},
				)
			}
			for _, variant := range variants {
				for _, p := range wireProtocols {
					t.Run(variant.name+"/"+p.name, func(t *testing.T) {
						a := rawCall(t, variant.ref, p)
						if p.procedure == unaryProcedure && p.contentType == "application/json" && a.status == http.StatusOK {
							t.Fatalf("v1 reference did not fail: %q", a.body)
						}
						assertSameExchange(t, a, rawCall(t, variant.srv, p))
					})
				}
			}
		})
	}
}

// The multiplicity the parity test compares is real: v1 sends a repeated
// value twice and keeps a pre-set one beside it, so a v2 that collapsed
// them could not agree with it.
func TestWireParity_RepeatedMetadataIsSentAsV1SendsIt(t *testing.T) {
	tc := parityCase{name: "repeated", v1: repeated, v2: repeated, preset: http.Header{"X-Value": {"same"}}}
	want := []string{"same", "same", "same", "other"}
	for name, srv := range map[string]http.Handler{
		"v1":       parityServerV1(tc, false),
		"v2":       parityServerV2(tc, svcerr.InterceptorV2()),
		"v2 twice": parityServerV2(tc, svcerr.InterceptorV2(), svcerr.InterceptorV2()),
	} {
		got := rawCall(t, h2Server(t, srv), wireProtocols[0])
		if v := got.header.Values("X-Value"); !slices.Equal(v, want) {
			t.Errorf("%s: X-Value = %q, want %q", name, v, want)
		}
	}
}
