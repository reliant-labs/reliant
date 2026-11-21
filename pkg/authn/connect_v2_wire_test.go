package authn

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
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/reliant-labs/forge/pkg/auth"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// This file proves the v2 interceptor's rejections are the v1 interceptor's
// on the wire: not the code and message a client library reports, but the
// raw HTTP exchange — status, response headers, response trailers and body,
// each compared on its own — for every protocol an old client may speak. A
// client library merges headers and trailers into one error metadata map,
// which is how a reason header that moved (or vanished) went unnoticed.

// muxV1 is muxV2 on connect-go v1: a unary Echo and a server-streaming
// Watch answering with h, behind ics.
func muxV1(h echoHandler, ics ...connect.Interceptor) *http.ServeMux {
	mux := http.NewServeMux()
	opts := connect.WithInterceptors(ics...)
	mux.Handle(echoProcedure, connect.NewUnaryHandler(echoProcedure,
		func(ctx context.Context, _ *connect.Request[wrapperspb.StringValue]) (*connect.Response[wrapperspb.StringValue], error) {
			out, err := h(ctx)
			if err != nil {
				return nil, err
			}
			return connect.NewResponse(wrapperspb.String(out)), nil
		}, opts))
	mux.Handle(watchProcedure, connect.NewServerStreamHandler(watchProcedure,
		func(ctx context.Context, _ *connect.Request[wrapperspb.StringValue], stream *connect.ServerStream[wrapperspb.StringValue]) error {
			out, err := h(ctx)
			if err != nil {
				return err
			}
			return stream.Send(wrapperspb.String(out))
		}, opts))
	return mux
}

// h2Server serves h over TLS with HTTP/2, which gRPC needs; the Connect
// protocol runs over it unchanged.
func h2Server(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// wireProtocol is one way an old client calls a procedure.
type wireProtocol struct {
	name        string
	procedure   string
	contentType string
	body        []byte
}

func envelope(payload []byte) []byte {
	out := make([]byte, 5, 5+len(payload))
	binary.BigEndian.PutUint32(out[1:], uint32(len(payload)))
	return append(out, payload...)
}

func wireProtocols(t *testing.T) []wireProtocol {
	t.Helper()
	msg, err := proto.Marshal(wrapperspb.String("hi"))
	if err != nil {
		t.Fatal(err)
	}
	return []wireProtocol{
		{"connect unary", echoProcedure, "application/json", []byte(`"hi"`)},
		{"connect server stream", watchProcedure, "application/connect+json", envelope([]byte(`"hi"`))},
		{"grpc unary", echoProcedure, "application/grpc", envelope(msg)},
		{"grpc server stream", watchProcedure, "application/grpc", envelope(msg)},
		{"grpc-web unary", echoProcedure, "application/grpc-web+proto", envelope(msg)},
	}
}

// wireExchange is what any client reads off the wire, minus what varies
// between two responses whoever serves them (Date).
type wireExchange struct {
	status  int
	header  http.Header
	trailer http.Header
	body    []byte
}

func rawCall(t *testing.T, srv *httptest.Server, p wireProtocol, headers map[string]string) wireExchange {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+p.procedure, bytes.NewReader(p.body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", p.contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
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

// assertSameExchange fails unless v1 and v2 put the same exchange on the
// wire: status, headers and trailers (each on its own) and body.
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

// upstreamErrors serves one rejection on each major and returns how to
// fetch it as a client — the remote error a hook that calls another service
// gets back. The v1 upstream also sets metadata on the error, which a
// forwarded (wire) error must not carry onward.
func upstreamErrors(t *testing.T) (v1 func(context.Context) error, v2 func(context.Context) error) {
	t.Helper()
	detail, err := connectv2proto.NewErrorDetail(wrapperspb.String("acct-7"))
	if err != nil {
		t.Fatal(err)
	}
	up1 := h2Server(t, muxV1(func(context.Context) (string, error) {
		ce := connect.NewError(connect.CodePermissionDenied, errors.New("suspended upstream"))
		d, err := connect.NewErrorDetail(wrapperspb.String("acct-7"))
		if err != nil {
			return "", err
		}
		ce.AddDetail(d)
		ce.Meta().Set("X-Upstream", "yes")
		return "", ce
	}))
	up2 := h2Server(t, muxV2(func(ctx context.Context) (string, error) {
		if info, ok := connectv2.CallInfoForServerContext(ctx); ok {
			info.ResponseHeader().Set("X-Upstream", "yes")
		}
		return "", connectv2.NewError(connectv2.CodePermissionDenied, "suspended upstream").WithDetail(detail)
	}))
	v1 = func(ctx context.Context) error {
		_, err := connect.NewClient[wrapperspb.StringValue, wrapperspb.StringValue](up1.Client(), up1.URL+echoProcedure).
			CallUnary(ctx, connect.NewRequest(wrapperspb.String("who")))
		if !connect.IsWireError(err) {
			t.Errorf("v1 upstream returned %v, want a wire error", err)
		}
		return err
	}
	v2 = func(ctx context.Context) error {
		var res wrapperspb.StringValue
		err := connectv2.NewClient(connecthttp.NewTransport(up2.Client(), up2.URL)).
			CallUnary(ctx, connectv2.Spec{StreamType: connectv2.StreamTypeUnary, Procedure: echoProcedure}, wrapperspb.String("who"), &res)
		if e, ok := errors.AsType[*connectv2.Error](err); !ok || !e.IsRemote() {
			t.Errorf("v2 upstream returned %v, want a remote error", err)
		}
		return err
	}
	return v1, v2
}

// metaError is a v1 rejection carrying metadata, a value repeated: v1 sends
// every value, in order.
func metaError(code connect.Code, msg string) *connect.Error {
	ce := connect.NewError(code, errors.New(msg))
	ce.Meta().Add("X-Value", "same")
	ce.Meta().Add("X-Value", "same")
	ce.Meta().Add("X-Value", "other")
	return ce
}

// Every rejection the v1 interceptor sends, the v2 interceptor sends
// byte-for-byte — the policy hooks' remaps with their reason and metadata,
// errors the hooks got from an upstream call — over Connect, gRPC and
// gRPC-Web, alone and in the production chain with svcerr's interceptor
// innermost (which an auth rejection never reaches) and outermost too.
func TestNewInterceptorV2_WireParityWithV1(t *testing.T) {
	revoked := errors.New("account revoked")
	validate := func(tok string) (*auth.Claims, error) {
		switch tok {
		case "good":
			return &auth.Claims{UserID: "u1"}, nil
		case "revoked":
			return nil, revoked
		}
		return nil, errors.New("signature invalid")
	}
	base := Policy{ValidatorConfigured: true, Validate: validate}
	same := func(p Policy) Policy { return p }
	upstreamV1, upstreamV2 := upstreamErrors(t)
	enrich := func(f func(context.Context) error) func(Policy) Policy {
		return func(p Policy) Policy {
			p.Enrich = func(ctx context.Context, _ *auth.Claims) (*auth.Claims, error) { return nil, f(ctx) }
			return p
		}
	}
	good := map[string]string{"Authorization": "Bearer good"}
	bad := map[string]string{"Authorization": "Bearer revoked"}

	for name, tc := range map[string]struct {
		policy   func(Policy) Policy
		policyV2 func(Policy) Policy // the v2 spelling, when it differs
		headers  map[string]string
		want     connect.Code
	}{
		"missing header": {policy: same, want: connect.CodeUnauthenticated},
		"malformed":      {policy: same, headers: map[string]string{"Authorization": "Token x"}, want: connect.CodeUnauthenticated},
		"invalid token":  {policy: same, headers: map[string]string{"Authorization": "Bearer nope"}, want: connect.CodeUnauthenticated},
		"MapError remap": {
			policy: func(p Policy) Policy {
				p.MapError = func(err error, fb *connect.Error) *connect.Error {
					if errors.Is(err, revoked) {
						return connect.NewError(connect.CodePermissionDenied, errors.New("account revoked"))
					}
					return nil
				}
				return p
			},
			headers: bad, want: connect.CodePermissionDenied,
		},
		"MapError with reason": {
			policy: func(p Policy) Policy {
				p.MapError = func(error, *connect.Error) *connect.Error {
					return svcerr.ToConnect(svcerr.WithReason(svcerr.PermissionDenied("revoked"), "account_revoked"))
				}
				return p
			},
			headers: bad, want: connect.CodePermissionDenied,
		},
		"MapError with repeated metadata": {
			policy: func(p Policy) Policy {
				p.MapError = func(error, *connect.Error) *connect.Error {
					return metaError(connect.CodePermissionDenied, "revoked")
				}
				return p
			},
			headers: bad, want: connect.CodePermissionDenied,
		},
		"MapError remote verdict": {
			policy: func(p Policy) Policy {
				p.MapError = func(error, *connect.Error) *connect.Error {
					ce := connect.NewWireError(connect.CodePermissionDenied, errors.New("revoked"))
					ce.Meta().Set("X-Forwarded", "never sent")
					return ce
				}
				return p
			},
			policyV2: func(p Policy) Policy {
				p.MapErrorV2 = func(error, *connectv2.Error) *connectv2.Error {
					return connectv2.NewError(connectv2.CodePermissionDenied, "revoked").WithRemote()
				}
				return p
			},
			headers: bad, want: connect.CodePermissionDenied,
		},
		"MapErrorV2 remap": {
			policy: func(p Policy) Policy {
				p.MapErrorV2 = func(err error, fb *connectv2.Error) *connectv2.Error {
					if errors.Is(err, revoked) {
						return connectv2.NewError(connectv2.CodePermissionDenied, "account revoked").WithCause(fb)
					}
					return nil
				}
				return p
			},
			headers: bad, want: connect.CodePermissionDenied,
		},
		"MapErrorV2 with reason": {
			policy: func(p Policy) Policy {
				p.MapError = func(error, *connect.Error) *connect.Error {
					return svcerr.ToConnect(svcerr.WithReason(svcerr.PermissionDenied("revoked"), "account_revoked"))
				}
				return p
			},
			policyV2: func(p Policy) Policy {
				p.MapErrorV2 = func(error, *connectv2.Error) *connectv2.Error {
					return svcerr.ToConnectV2(svcerr.WithReason(svcerr.PermissionDenied("revoked"), "account_revoked"))
				}
				return p
			},
			headers: bad, want: connect.CodePermissionDenied,
		},
		"MapErrorV2 keeps fallback": {
			policy: func(p Policy) Policy {
				p.MapErrorV2 = func(error, *connectv2.Error) *connectv2.Error { return nil }
				return p
			},
			headers: map[string]string{"Authorization": "Bearer nope"}, want: connect.CodeUnauthenticated,
		},
		"Enrich plain error": {
			policy:  enrich(func(context.Context) error { return errors.New("db down") }),
			headers: good, want: connect.CodeUnauthenticated,
		},
		"Enrich v1 connect error": {
			policy: enrich(func(context.Context) error {
				return connect.NewError(connect.CodePermissionDenied, errors.New("suspended"))
			}),
			headers: good, want: connect.CodePermissionDenied,
		},
		"Enrich v2 connect error": {
			policy:  enrich(func(context.Context) error { return connectv2.NewError(connectv2.CodePermissionDenied, "suspended") }),
			headers: good, want: connect.CodePermissionDenied,
		},
		"Enrich with reason and metadata": {
			policy: enrich(func(context.Context) error {
				return svcerr.ToConnect(svcerr.WithMetadata(
					svcerr.WithReason(svcerr.PermissionDenied("suspended"), "account_suspended"), "x-forge-error-scopes", "a,b"))
			}),
			headers: good, want: connect.CodePermissionDenied,
		},
		"Enrich wrapped error with repeated metadata": {
			policy: enrich(func(context.Context) error {
				return fmt.Errorf("lookup: %w", metaError(connect.CodePermissionDenied, "suspended"))
			}),
			headers: good, want: connect.CodePermissionDenied,
		},
		"Enrich remote error from an upstream call": {
			policy: enrich(upstreamV1), policyV2: enrich(upstreamV2),
			headers: good, want: connect.CodePermissionDenied,
		},
		"Enrich wrapped remote error from an upstream call": {
			policy:   enrich(func(ctx context.Context) error { return fmt.Errorf("account service: %w", upstreamV1(ctx)) }),
			policyV2: enrich(func(ctx context.Context) error { return fmt.Errorf("account service: %w", upstreamV2(ctx)) }),
			headers:  good, want: connect.CodePermissionDenied,
		},
		"no principal in handler": {
			policy:  func(p Policy) Policy { p.AnonymousOK = true; return p },
			headers: nil, want: connect.CodeUnauthenticated,
		},
	} {
		t.Run(name, func(t *testing.T) {
			p1 := tc.policy(base)
			p2 := p1
			if tc.policyV2 != nil {
				p2 = tc.policyV2(base)
			}
			ic1, err := NewInterceptor(p1)
			if err != nil {
				t.Fatal(err)
			}
			ic2, err := NewInterceptorV2(p2)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := callV1(t, whoAmI, ic1, tc.headers); connect.CodeOf(err) != tc.want {
				t.Fatalf("v1 rejects with %v, want %v", err, tc.want)
			}
			whoAmIV2 := func(ctx context.Context) (string, error) {
				c, err := GetUserV2(ctx)
				if err != nil {
					return "", err
				}
				return c.UserID, nil
			}
			for chain, srv := range map[string]struct{ v1, v2 http.Handler }{
				"alone": {
					muxV1(whoAmI, ic1),
					muxV2(whoAmIV2, ic2),
				},
				"svcerr innermost": {
					muxV1(whoAmI, ic1, svcerr.Interceptor()),
					muxV2(whoAmIV2, ic2, svcerr.InterceptorV2()),
				},
				"svcerr on both sides": {
					muxV1(whoAmI, svcerr.Interceptor(), ic1, svcerr.Interceptor()),
					muxV2(whoAmIV2, svcerr.InterceptorV2(), ic2, svcerr.InterceptorV2()),
				},
			} {
				s1, s2 := h2Server(t, srv.v1), h2Server(t, srv.v2)
				for _, wp := range wireProtocols(t) {
					t.Run(chain+"/"+wp.name, func(t *testing.T) {
						assertSameExchange(t, rawCall(t, s1, wp, tc.headers), rawCall(t, s2, wp, tc.headers))
					})
				}
			}
		})
	}
}

// The reason a MapError hook annotates is on the wire where a client reads
// it: a response header for unary Connect, a trailer for gRPC. (The parity
// test proves v2 matches v1; this pins what v1 is, so a parity that agreed
// on "absent" could not pass.)
func TestNewInterceptorV2_RejectionCarriesTheReason(t *testing.T) {
	ic, err := NewInterceptorV2(Policy{
		ValidatorConfigured: true,
		Validate:            func(string) (*auth.Claims, error) { return nil, errors.New("expired") },
		MapError: func(error, *connect.Error) *connect.Error {
			return svcerr.ToConnect(svcerr.WithReason(svcerr.PermissionDenied("revoked"), "account_revoked"))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := h2Server(t, muxV2(whoAmI, ic, svcerr.InterceptorV2()))
	headers := map[string]string{"Authorization": "Bearer x"}
	for _, wp := range wireProtocols(t) {
		got := rawCall(t, srv, wp, headers)
		switch wp.name {
		case "connect unary":
			if v := got.header.Values(svcerr.ReasonHeader); !slices.Equal(v, []string{"account_revoked"}) {
				t.Errorf("%s: reason header %q, want [account_revoked]", wp.name, v)
			}
		case "grpc unary", "grpc server stream":
			if v := got.trailer.Values(svcerr.ReasonHeader); !slices.Equal(v, []string{"account_revoked"}) {
				t.Errorf("%s: reason trailer %q, want [account_revoked]", wp.name, v)
			}
			if v := got.header.Values(svcerr.ReasonHeader); len(v) != 0 {
				t.Errorf("%s: reason also sent as a header %q", wp.name, v)
			}
		case "grpc-web unary":
			// A trailers-only gRPC-Web response carries its trailers as headers.
			if v := got.header.Values(svcerr.ReasonHeader); !slices.Equal(v, []string{"account_revoked"}) {
				t.Errorf("%s: reason %q, want [account_revoked]", wp.name, v)
			}
		default:
			if !bytes.Contains(got.body, []byte("account_revoked")) {
				t.Errorf("%s: end-of-stream metadata %q lacks the reason", wp.name, got.body)
			}
		}
	}
}
