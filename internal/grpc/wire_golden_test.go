// Copyright (c) 2025 Reliant Labs
package grpc

// Golden wire bytes of reliant's two real listeners.
//
// These goldens were recorded on the LAST connect-go v1 commit, before the
// move to connectrpc.com/connect/v2. A test service is served through the
// REAL listeners (NewAPIListener over NewUserChain, NewDaemonListener over
// NewDaemonChain: the real chains, edge, HTTP server and HTTP/2 settings), and
// every case is requested by hand-built net/http requests in each protocol a
// deployed client speaks (Connect unary and streaming, gRPC, gRPC-Web). The
// client half names no connect type, so it is identical before and after the
// migration; only the server-side handler registration (wireGoldenRoutes and
// the upstream server) is connect-major specific.
//
// What is recorded per exchange: the HTTP status, response headers, the exact
// body bytes (text when UTF-8, hex otherwise; enveloped protocols are split
// into their envelopes), and HTTP trailers. A diff after the v2 migration is
// either a regression or an intended wire change, and an intended one must be
// listed in the migration PR. Never run -update to make a red diff go away.
//
// Normalized, because the value differs per run or per compression library:
// the Date header, X-Request-Id and Traceresponse (their shape is kept), and a gzip-encoded body's Content-Length and bytes (the
// decompressed body is recorded, with Gzipped set). Envelope compression
// (flag 0x01) is recorded as the compressed flag with its bytes in hex. JSON bodies are only
// byte-stable across builds when a message has one field (protojson inserts a
// build-dependent space after commas), so every message here has one field.
//
// To rewrite the goldens from the current server:
//
//	go test ./internal/grpc -run TestWireGolden -update

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/google/go-cmp/cmp"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

var updateWireGolden = flag.Bool("update", false, "rewrite internal/grpc/testdata/wire_golden from the current server")

const (
	wireGoldenDir     = "testdata/wire_golden"
	goldenService     = "/wiregolden.v1.Golden/"
	goldenAPIKey      = "golden-api-key"
	goldenOrigin      = "https://app.example.test"
	goldenMarker      = "<normalized>"
	goldenUpstreamMsg = "upstream refused this"
)

// ---- server side: the test service (the only connect-major-specific part) ----

type (
	goldenUnaryFn  func(ctx context.Context, req *connect.Request[wrapperspb.StringValue]) (*connect.Response[wrapperspb.StringValue], error)
	goldenStreamFn func(ctx context.Context, req *connect.Request[wrapperspb.StringValue], stream *connect.ServerStream[wrapperspb.StringValue]) error
)

// goldenHandlers builds the service's handler set, each registered the way
// generated code registers a method: NewUnaryHandler / NewServerStreamHandler
// with the chain's handler options.
func goldenHandlers(upstreamURL string, opts []connect.HandlerOption) http.Handler {
	upstream := connect.NewClient[wrapperspb.StringValue, wrapperspb.StringValue](http.DefaultClient, upstreamURL+"/upstream.v1.Upstream/Fail")
	callUpstream := func(ctx context.Context) error {
		_, err := upstream.CallUnary(ctx, connect.NewRequest(wrapperspb.String("ping")))
		return err
	}
	reply := func(msg string) *connect.Response[wrapperspb.StringValue] {
		return connect.NewResponse(wrapperspb.String(msg))
	}
	fail := func(err error) goldenUnaryFn {
		return func(context.Context, *connect.Request[wrapperspb.StringValue]) (*connect.Response[wrapperspb.StringValue], error) {
			return nil, err
		}
	}

	unary := map[string]goldenUnaryFn{}
	for _, c := range goldenCases() {
		if c.unary != nil {
			unary[c.name] = c.unary(callUpstream, reply, fail)
		}
	}
	stream := map[string]goldenStreamFn{}
	for _, c := range goldenCases() {
		if c.stream != nil {
			stream[c.name] = c.stream(callUpstream)
		}
	}

	handlers := map[string]http.Handler{}
	for name, fn := range unary {
		proc := goldenService + name
		handlers[proc] = connect.NewUnaryHandler(proc, fn, opts...)
	}
	for name, fn := range stream {
		proc := goldenService + name
		handlers[proc] = connect.NewServerStreamHandler(proc, fn, opts...)
	}
	// Like generated code: a switch on the path, http.NotFound for anything else.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := handlers[r.URL.Path]; ok {
			h.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

// startGoldenUpstream serves a Connect server whose one rpc fails with a
// classified error carrying a detail, a reason header and custom metadata:
// the shape of an error one reliant service gets back from another.
func startGoldenUpstream(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	const proc = "/upstream.v1.Upstream/Fail"
	mux.Handle(proc, connect.NewUnaryHandler(proc,
		func(context.Context, *connect.Request[wrapperspb.StringValue]) (*connect.Response[wrapperspb.StringValue], error) {
			ce := connect.NewError(connect.CodeFailedPrecondition, errors.New(goldenUpstreamMsg))
			if d, err := connect.NewErrorDetail(wrapperspb.String("upstream-detail")); err == nil {
				ce.AddDetail(d)
			}
			ce.Meta().Set(svcerr.ReasonHeader, "upstream_reason")
			ce.Meta().Set("X-Upstream-Meta", "from-upstream")
			return nil, ce
		}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// ---- the cases ----

type authMode int

const (
	authOK authMode = iota
	authNone
	authBad
)

type caseKind int

const (
	kindUnary caseKind = iota
	kindStream
	kindRaw
)

type goldenCase struct {
	name string
	kind caseKind
	// proc is the procedure requested (default: the case's own name).
	proc string
	auth authMode
	// timeout is the client deadline the request carries (zero: none).
	timeout time.Duration
	// unary / stream build the handler registered under the case's name.
	unary  func(callUpstream func(context.Context) error, reply func(string) *connect.Response[wrapperspb.StringValue], fail func(error) goldenUnaryFn) goldenUnaryFn
	stream func(callUpstream func(context.Context) error) goldenStreamFn
	// raw requests, for the cases that are not one rpc in each protocol.
	raw func() []reqSpec
}

// failWith is a unary case whose handler returns err.
func failWith(name string, err error) goldenCase {
	return goldenCase{name: name, kind: kindUnary, unary: func(_ func(context.Context) error, _ func(string) *connect.Response[wrapperspb.StringValue], fail func(error) goldenUnaryFn) goldenUnaryFn {
		return fail(err)
	}}
}

// streamTwoThen is a server-stream case that sends "one" and "two", then
// returns err (nil ends the stream OK).
func streamTwoThen(name string, send int, err func() error) goldenCase {
	return goldenCase{name: name, kind: kindStream, stream: func(func(context.Context) error) goldenStreamFn {
		return func(_ context.Context, _ *connect.Request[wrapperspb.StringValue], s *connect.ServerStream[wrapperspb.StringValue]) error {
			s.ResponseHeader().Set("X-Golden-Header", "header-value")
			s.ResponseTrailer().Set("X-Golden-Trailer", "trailer-value")
			for _, m := range []string{"one", "two"}[:send] {
				if e := s.Send(wrapperspb.String(m)); e != nil {
					return e
				}
			}
			return err()
		}
	}}
}

func svcerrReasonMeta() error {
	err := svcerr.PermissionDenied("you may not touch this project")
	err = svcerr.WithReason(err, "project_locked")
	err = svcerr.WithMetadata(err, "x-forge-error-scopes", "projects:write")
	err = svcerr.WithMetadata(err, "x-golden-extra", "extra-value")
	return svcerr.WithMessage(err, "This project is locked. Ask an owner to unlock it.")
}

func connectErrorWithDetail() error {
	ce := connect.NewError(connect.CodeAborted, errors.New("raw connect error with a detail"))
	if d, err := connect.NewErrorDetail(wrapperspb.String("conn-detail")); err == nil {
		ce.AddDetail(d)
	}
	return ce
}

func goldenCases() []goldenCase {
	structDetail, _ := structpb.NewStruct(map[string]any{"field": "value"})
	cases := []goldenCase{
		{name: "Success", kind: kindUnary, unary: func(_ func(context.Context) error, reply func(string) *connect.Response[wrapperspb.StringValue], _ func(error) goldenUnaryFn) goldenUnaryFn {
			return func(_ context.Context, req *connect.Request[wrapperspb.StringValue]) (*connect.Response[wrapperspb.StringValue], error) {
				resp := reply("pong: " + req.Msg.GetValue())
				resp.Header().Set("X-Golden-Header", "header-value")
				resp.Trailer().Set("X-Golden-Trailer", "trailer-value")
				return resp, nil
			}
		}},
		{name: "LargeSuccess", kind: kindUnary, unary: func(_ func(context.Context) error, reply func(string) *connect.Response[wrapperspb.StringValue], _ func(error) goldenUnaryFn) goldenUnaryFn {
			return func(context.Context, *connect.Request[wrapperspb.StringValue]) (*connect.Response[wrapperspb.StringValue], error) {
				return reply(strings.Repeat("large payload ", 300)), nil
			}
		}},
		failWith("SvcerrReasonMeta", svcerrReasonMeta()),
		failWith("SvcerrDetail", svcerr.WithReason(svcerr.WithDetail(svcerr.FailedPrecondition("needs review"), wrapperspb.String("detail-payload")), "needs_review")),
		failWith("SvcerrStructDetail", svcerr.WithDetail(svcerr.InvalidArgument("bad field"), structDetail)),
		failWith("SvcerrCause", svcerr.WithCause(svcerr.Unavailable("try again shortly"), errors.New("dial tcp 10.0.0.5:5432: secret internals"))),
		failWith("NotFound", svcerr.NotFound("project")),
		failWith("WrappedNotFound", fmt.Errorf("load project: %w", svcerr.NotFound("project"))),
		failWith("InvalidArgument", svcerr.InvalidArgument("name must not be empty")),
		failWith("PlainError", errors.New("secret internals: db password=hunter2")),
		failWith("ConnectErrorDetail", connectErrorWithDetail()),
		failWith("ConnectErrorMeta", func() error {
			ce := connect.NewError(connect.CodeResourceExhausted, errors.New("slow down"))
			ce.Meta().Set("Retry-After", "7")
			ce.Meta().Set("X-Golden-Meta", "meta-value")
			return ce
		}()),
		failWith("Canceled", context.Canceled),
		failWith("WrappedCanceled", fmt.Errorf("op: %w", context.Canceled)),
		failWith("DeadlineExceeded", context.DeadlineExceeded),
		{name: "ClientDeadline", kind: kindUnary, timeout: 30 * time.Millisecond, unary: func(func(context.Context) error, func(string) *connect.Response[wrapperspb.StringValue], func(error) goldenUnaryFn) goldenUnaryFn {
			return func(ctx context.Context, _ *connect.Request[wrapperspb.StringValue]) (*connect.Response[wrapperspb.StringValue], error) {
				<-ctx.Done()
				return nil, ctx.Err()
			}
		}},
		{name: "RemoteUnary", kind: kindUnary, unary: func(call func(context.Context) error, _ func(string) *connect.Response[wrapperspb.StringValue], _ func(error) goldenUnaryFn) goldenUnaryFn {
			return func(ctx context.Context, _ *connect.Request[wrapperspb.StringValue]) (*connect.Response[wrapperspb.StringValue], error) {
				return nil, call(ctx)
			}
		}},

		{name: "StreamLarge", kind: kindStream, stream: func(func(context.Context) error) goldenStreamFn {
			return func(_ context.Context, _ *connect.Request[wrapperspb.StringValue], s *connect.ServerStream[wrapperspb.StringValue]) error {
				return s.Send(wrapperspb.String(strings.Repeat("large payload ", 300)))
			}
		}},
		streamTwoThen("StreamOK", 2, func() error { return nil }),
		streamTwoThen("StreamErrAfterTwo", 2, svcerrReasonMeta),
		streamTwoThen("StreamErrImmediate", 0, func() error { return svcerr.NotFound("project") }),
		streamTwoThen("StreamPlainError", 1, func() error { return errors.New("secret internals: stream") }),
		streamTwoThen("StreamConnectErrorDetail", 1, connectErrorWithDetail),
		streamTwoThen("StreamCanceled", 1, func() error { return context.Canceled }),
		{name: "StreamRemote", kind: kindStream, stream: func(call func(context.Context) error) goldenStreamFn {
			return func(ctx context.Context, _ *connect.Request[wrapperspb.StringValue], s *connect.ServerStream[wrapperspb.StringValue]) error {
				if err := s.Send(wrapperspb.String("one")); err != nil {
					return err
				}
				return call(ctx)
			}
		}},

		// The auth slot, through the real authenticators.
		{name: "AuthMissing", kind: kindUnary, proc: "Success", auth: authNone},
		{name: "AuthBad", kind: kindUnary, proc: "Success", auth: authBad},
		{name: "StreamAuthMissing", kind: kindStream, proc: "StreamOK", auth: authNone},

		// Requests no handler answers.
		{name: "UnknownProcedure", kind: kindRaw, raw: func() []reqSpec {
			return []reqSpec{
				connectUnaryJSON(goldenService+"NoSuchMethod", 0),
				grpcUnary(goldenService+"NoSuchMethod", 0),
			}
		}},
		{name: "UnknownService", kind: kindRaw, raw: func() []reqSpec {
			return []reqSpec{
				connectUnaryJSON("/nosuch.v1.Nothing/Method", 0),
				grpcUnary("/nosuch.v1.Nothing/Method", 0),
			}
		}},
		{name: "UnsupportedContentType", kind: kindRaw, raw: func() []reqSpec {
			return []reqSpec{{
				name: "text-plain", method: http.MethodPost, path: goldenService + "Success",
				headers: map[string]string{"Content-Type": "text/plain"}, body: []byte("ping"),
			}}
		}},
		{name: "GetNotAllowed", kind: kindRaw, raw: func() []reqSpec {
			return []reqSpec{{name: "get", method: http.MethodGet, path: goldenService + "Success"}}
		}},
	}
	return cases
}

// ---- raw requests (no connect type: identical before and after the migration) ----

type reqSpec struct {
	name    string
	method  string
	path    string
	headers map[string]string
	body    []byte
	h2      bool
}

func envelope(flags byte, payload []byte) []byte {
	out := make([]byte, 5, 5+len(payload))
	out[0] = flags
	binary.BigEndian.PutUint32(out[1:], uint32(len(payload)))
	return append(out, payload...)
}

func protoString(s string) []byte {
	b, err := proto.Marshal(wrapperspb.String(s))
	if err != nil {
		panic(err)
	}
	return b
}

func connectTimeout(h map[string]string, d time.Duration) {
	if d > 0 {
		h["Connect-Timeout-Ms"] = fmt.Sprint(d.Milliseconds())
	}
}

func grpcTimeout(h map[string]string, d time.Duration) {
	if d > 0 {
		h["Grpc-Timeout"] = fmt.Sprintf("%dm", d.Milliseconds())
	}
}

func connectUnaryJSON(path string, timeout time.Duration) reqSpec {
	h := map[string]string{"Content-Type": "application/json", "Connect-Protocol-Version": "1", "Origin": goldenOrigin}
	connectTimeout(h, timeout)
	return reqSpec{name: "connect-json", method: http.MethodPost, path: path, headers: h, body: []byte(`"ping"`)}
}

func grpcUnary(path string, timeout time.Duration) reqSpec {
	h := map[string]string{"Content-Type": "application/grpc", "Te": "trailers"}
	grpcTimeout(h, timeout)
	return reqSpec{name: "grpc", method: http.MethodPost, path: path, headers: h, body: envelope(0, protoString("ping")), h2: true}
}

// protocolsFor returns every protocol a client speaks to this case.
func protocolsFor(c goldenCase) []reqSpec {
	proc := c.proc
	if proc == "" {
		proc = c.name
	}
	path := goldenService + proc
	if c.kind == kindRaw {
		return c.raw()
	}
	if c.kind == kindUnary {
		gz := connectUnaryJSON(path, c.timeout)
		gz.name = "connect-json-gzip"
		gz.headers["Accept-Encoding"] = "gzip"

		proto := map[string]string{"Content-Type": "application/proto", "Connect-Protocol-Version": "1", "Origin": goldenOrigin}
		connectTimeout(proto, c.timeout)
		web := map[string]string{"Content-Type": "application/grpc-web+proto", "X-Grpc-Web": "1", "Origin": goldenOrigin}
		grpcTimeout(web, c.timeout)
		return []reqSpec{
			connectUnaryJSON(path, c.timeout),
			{name: "connect-proto", method: http.MethodPost, path: path, headers: proto, body: protoString("ping")},
			gz,
			grpcUnary(path, c.timeout),
			{name: "grpc-web-proto", method: http.MethodPost, path: path, headers: web, body: envelope(0, protoString("ping"))},
		}
	}
	jsonH := map[string]string{"Content-Type": "application/connect+json", "Connect-Protocol-Version": "1"}
	protoH := map[string]string{"Content-Type": "application/connect+proto", "Connect-Protocol-Version": "1"}
	browserH := map[string]string{"Content-Type": "application/connect+json", "Connect-Protocol-Version": "1", "Origin": goldenOrigin, "Accept-Encoding": "gzip"}
	grpcH := map[string]string{"Content-Type": "application/grpc", "Te": "trailers"}
	webH := map[string]string{"Content-Type": "application/grpc-web+proto", "X-Grpc-Web": "1", "Origin": goldenOrigin}
	return []reqSpec{
		{name: "connect-stream-json", method: http.MethodPost, path: path, headers: jsonH, body: envelope(0, []byte(`"ping"`))},
		{name: "connect-stream-proto", method: http.MethodPost, path: path, headers: protoH, body: envelope(0, protoString("ping"))},
		{name: "connect-stream-json-browser-gzip", method: http.MethodPost, path: path, headers: browserH, body: envelope(0, []byte(`"ping"`))},
		{name: "grpc", method: http.MethodPost, path: path, headers: grpcH, body: envelope(0, protoString("ping")), h2: true},
		{name: "grpc-web-proto", method: http.MethodPost, path: path, headers: webH, body: envelope(0, protoString("ping"))},
	}
}

// ---- capture ----

type goldenExchange struct {
	Request     string      `json:"request"`
	Status      int         `json:"status"`
	HTTPProto   string      `json:"http_proto"`
	Headers     http.Header `json:"headers"`
	Body        goldenBody  `json:"body"`
	Trailers    http.Header `json:"trailers,omitempty"`
	Annotations []string    `json:"annotations,omitempty"`
}

type goldenBody struct {
	// Gzipped marks a body that came Content-Encoded: gzip; what follows is
	// the decompressed content, because compressed bytes are the compression
	// library's, not the protocol's.
	Gzipped bool `json:"gzipped,omitempty"`
	// Text is the body when it is UTF-8 (exact), Hex when it is not.
	Text      string           `json:"text,omitempty"`
	Hex       string           `json:"hex,omitempty"`
	Decoded   string           `json:"decoded,omitempty"`
	Envelopes []goldenEnvelope `json:"envelopes,omitempty"`
}

type goldenEnvelope struct {
	Flags   string `json:"flags"`
	Kind    string `json:"kind"`
	Text    string `json:"text,omitempty"`
	Hex     string `json:"hex,omitempty"`
	Decoded string `json:"decoded,omitempty"`
}

type goldenFile struct {
	Chain     string                    `json:"chain"`
	Case      string                    `json:"case"`
	Exchanges map[string]goldenExchange `json:"exchanges"`
}

type goldenTransports struct{ h1, h2 *http.Client }

func newGoldenTransports(t *testing.T) goldenTransports {
	t.Helper()
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	h1 := &http.Client{
		Transport:     &http.Transport{DisableCompression: true, DisableKeepAlives: true},
		CheckRedirect: noRedirect,
	}
	// Prior-knowledge h2c: how every HTTP/2 client of the daemon and API dials
	// the cleartext listeners.
	h2 := &http.Client{
		Transport: &http2.Transport{
			AllowHTTP:          true,
			DisableCompression: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			},
		},
		CheckRedirect: noRedirect,
	}
	t.Cleanup(func() { h1.CloseIdleConnections(); h2.CloseIdleConnections() })
	return goldenTransports{h1: h1, h2: h2}
}

func (g goldenTransports) do(t *testing.T, baseURL, bearer string, mode authMode, spec reqSpec) goldenExchange {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var body io.Reader
	if spec.body != nil {
		body = bytes.NewReader(spec.body)
	}
	req, err := http.NewRequestWithContext(ctx, spec.method, baseURL+spec.path, body)
	require.NoError(t, err)
	for k, v := range spec.headers {
		req.Header.Set(k, v)
	}
	switch mode {
	case authOK:
		req.Header.Set("Authorization", "Bearer "+bearer)
	case authBad:
		req.Header.Set("Authorization", "Bearer not-a-valid-credential")
	}
	client := g.h1
	if spec.h2 {
		client = g.h2
	}
	resp, err := client.Do(req)
	require.NoError(t, err, "%s %s", spec.name, spec.path)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "%s %s", spec.name, spec.path)

	ex := goldenExchange{
		Request:   spec.method + " " + spec.path + " [" + spec.name + "]",
		Status:    resp.StatusCode,
		HTTPProto: resp.Proto,
	}
	ct := resp.Header.Get("Content-Type")
	gzipped := resp.Header.Get("Content-Encoding") == "gzip"
	if gzipped {
		zr, zerr := gzip.NewReader(bytes.NewReader(raw))
		require.NoError(t, zerr)
		raw, err = io.ReadAll(zr)
		require.NoError(t, err)
	}
	ex.Headers = normalizeGoldenHeaders(resp.Header, gzipped)
	ex.Trailers = normalizeGoldenHeaders(resp.Trailer, false)
	if len(ex.Trailers) == 0 {
		ex.Trailers = nil
	}
	ex.Body = recordGoldenBody(ct, raw)
	ex.Body.Gzipped = gzipped
	ex.Annotations = goldenAnnotations(ex)
	return ex
}

// normalizeGoldenHeaders keeps presence but drops values that differ per run.
func normalizeGoldenHeaders(h http.Header, gzipped bool) http.Header {
	out := http.Header{}
	for k, v := range h {
		switch {
		case k == "Date":
			out[k] = []string{goldenMarker}
		case k == "Content-Length" && gzipped:
			out[k] = []string{goldenMarker}
		case k == "X-Request-Id" || k == "Traceresponse":
			out[k] = normalizeGoldenIDs(v)
		default:
			out[k] = append([]string(nil), v...)
		}
	}
	return out
}

var (
	goldenRequestID = regexp.MustCompile(`^[0-9a-f]{32}$`)
	goldenTraceResp = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`)
)

// normalizeGoldenIDs replaces a request or trace id with a marker that names
// its shape, so a changed format still shows as a diff; a value of any other
// shape is kept verbatim.
func normalizeGoldenIDs(vs []string) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		switch {
		case goldenRequestID.MatchString(v):
			out[i] = "<normalized: 32 hex>"
		case goldenTraceResp.MatchString(v):
			out[i] = "<normalized: traceparent>"
		default:
			out[i] = v
		}
	}
	return out
}

func isEnveloped(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	return strings.HasPrefix(ct, "application/connect+") || strings.HasPrefix(ct, "application/grpc")
}

func recordGoldenBody(ct string, body []byte) goldenBody {
	if isEnveloped(ct) && len(body) > 0 {
		if envs, ok := splitEnvelopes(ct, body); ok {
			return goldenBody{Envelopes: envs}
		}
	}
	return plainGoldenBody(ct, body)
}

func plainGoldenBody(ct string, body []byte) goldenBody {
	var out goldenBody
	if len(body) == 0 {
		return out
	}
	if utf8.Valid(body) && !bytes.ContainsFunc(body, func(r rune) bool { return r < 0x09 || (r > 0x0d && r < 0x20) }) {
		out.Text = string(body)
		return out
	}
	out.Hex = hex.EncodeToString(body)
	out.Decoded = decodeStringValue(body)
	return out
}

func decodeStringValue(b []byte) string {
	var sv wrapperspb.StringValue
	if proto.Unmarshal(b, &sv) != nil {
		return ""
	}
	return fmt.Sprintf("StringValue(%q)", sv.GetValue())
}

func splitEnvelopes(ct string, body []byte) ([]goldenEnvelope, bool) {
	jsonMsgs := strings.Contains(strings.ToLower(ct), "json")
	var out []goldenEnvelope
	for len(body) > 0 {
		if len(body) < 5 {
			return nil, false
		}
		flags, n := body[0], int(binary.BigEndian.Uint32(body[1:5]))
		if len(body) < 5+n {
			return nil, false
		}
		payload := body[5 : 5+n]
		body = body[5+n:]
		e := goldenEnvelope{Flags: fmt.Sprintf("0x%02x", flags)}
		switch {
		case flags&0x02 != 0:
			e.Kind = "connect-end-stream"
			e.Text = string(payload)
		case flags&0x80 != 0:
			e.Kind = "grpc-web-trailers"
			e.Text = string(payload)
		case flags&0x01 != 0:
			e.Kind = "message-compressed"
			e.Hex = hex.EncodeToString(payload)
		default:
			e.Kind = "message"
			if jsonMsgs {
				e.Text = string(payload)
			} else {
				e.Hex = hex.EncodeToString(payload)
				e.Decoded = decodeStringValue(payload)
			}
		}
		out = append(out, e)
	}
	return out, true
}

// goldenAnnotations decodes what a reader cannot: the base64 google.rpc.Status
// a gRPC response carries in grpc-status-details-bin.
func goldenAnnotations(ex goldenExchange) []string {
	var found []string
	for _, h := range []http.Header{ex.Headers, ex.Trailers} {
		for k, v := range h {
			if strings.EqualFold(k, "Grpc-Status-Details-Bin") {
				found = append(found, v...)
			}
		}
	}
	for _, e := range ex.Body.Envelopes {
		for _, line := range strings.Split(e.Text, "\r\n") {
			if v, ok := strings.CutPrefix(line, "grpc-status-details-bin: "); ok {
				found = append(found, v)
			}
		}
	}
	var out []string
	for _, v := range found {
		out = append(out, "grpc-status-details-bin = "+describeStatus(v))
	}
	sort.Strings(out)
	return out
}

func describeStatus(b64 string) string {
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(b64, "="))
	if err != nil {
		return "undecodable: " + err.Error()
	}
	var code int64
	var msg string
	var details []string
	for len(raw) > 0 {
		num, typ, n := protowire.ConsumeTag(raw)
		if n < 0 {
			return "malformed status"
		}
		raw = raw[n:]
		switch {
		case num == 1 && typ == protowire.VarintType:
			v, m := protowire.ConsumeVarint(raw)
			code, raw = int64(v), raw[m:]
		case typ == protowire.BytesType:
			v, m := protowire.ConsumeBytes(raw)
			raw = raw[m:]
			switch num {
			case 2:
				msg = string(v)
			case 3:
				details = append(details, describeAny(v))
			}
		default:
			m := protowire.ConsumeFieldValue(num, typ, raw)
			if m < 0 {
				return "malformed status"
			}
			raw = raw[m:]
		}
	}
	return fmt.Sprintf("code=%d message=%q details=%v", code, msg, details)
}

func describeAny(b []byte) string {
	var url string
	var value []byte
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 || typ != protowire.BytesType {
			return "malformed any"
		}
		b = b[n:]
		v, m := protowire.ConsumeBytes(b)
		b = b[m:]
		switch num {
		case 1:
			url = string(v)
		case 2:
			value = v
		}
	}
	return fmt.Sprintf("{%s %s}", url, decodeStringValue(value))
}

// ---- the real listeners ----

type goldenListener interface {
	Listen(context.Context) error
	Run(context.Context) error
	Addr() net.Addr
}

func serveGolden(t *testing.T, l goldenListener) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, l.Listen(ctx))
	ran := make(chan error, 1)
	go func() { ran <- l.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-ran)
	})
	return "http://" + l.Addr().String()
}

func wireGoldenRoutes(upstreamURL string) serverkit.Routes {
	return serverkit.RoutesFunc(func(mux *http.ServeMux, opts ...connect.HandlerOption) []string {
		mux.Handle(goldenService, goldenHandlers(upstreamURL, opts))
		// No service name is returned: the test service is not in the proto
		// registry, so the listener has no audience to check it against.
		return nil
	})
}

type goldenChain struct {
	name    string
	baseURL string
	bearer  string
}

func startUserGolden(t *testing.T, upstreamURL string) goldenChain {
	t.Helper()
	userAuth, err := NewUserAuth(auth.Settings{Mode: auth.ModeAPIKey, APIKey: goldenAPIKey}, tokenauthority.NewMemory())
	require.NoError(t, err)
	quiet := slog.New(slog.DiscardHandler)
	chain, err := NewUserChain(UserChainDeps{Auth: userAuth, Logger: quiet})
	require.NoError(t, err)
	l, err := NewAPIListener(APIListenerDeps{BindAddress: "127.0.0.1", Chain: chain, Routes: wireGoldenRoutes(upstreamURL), Logger: quiet})
	require.NoError(t, err)
	return goldenChain{name: "user", baseURL: serveGolden(t, l), bearer: goldenAPIKey}
}

func startDaemonGolden(t *testing.T, upstreamURL string) goldenChain {
	t.Helper()
	authority := tokenauthority.NewMemory()
	minted, err := authority.MintForUser(context.Background(), tokenauthority.MintRequest{
		UserID: "user-golden", Name: "golden", Scopes: []fat.Scope{fat.ScopeDaemonConnect},
	})
	require.NoError(t, err)
	daemonAuth, err := NewDaemonAuth(authority)
	require.NoError(t, err)
	quiet := slog.New(slog.DiscardHandler)
	chain, err := NewDaemonChain(DaemonChainDeps{Auth: daemonAuth, Logger: quiet})
	require.NoError(t, err)
	l, err := NewDaemonListener(DaemonListenerDeps{BindAddress: "127.0.0.1", Chain: chain, Routes: wireGoldenRoutes(upstreamURL), Logger: quiet})
	require.NoError(t, err)
	return goldenChain{name: "daemon", baseURL: serveGolden(t, l), bearer: minted.Plaintext}
}

// ---- compare / update ----

func TestWireGolden(t *testing.T) {
	upstreamURL := startGoldenUpstream(t)
	clients := newGoldenTransports(t)
	chains := []goldenChain{startUserGolden(t, upstreamURL), startDaemonGolden(t, upstreamURL)}

	produced := map[string]bool{}
	for _, chain := range chains {
		for _, c := range goldenCases() {
			rel := filepath.Join(chain.name, c.name+".json")
			produced[rel] = true
			t.Run(chain.name+"/"+c.name, func(t *testing.T) {
				file := goldenFile{Chain: chain.name, Case: c.name, Exchanges: map[string]goldenExchange{}}
				for _, spec := range protocolsFor(c) {
					file.Exchanges[spec.name] = clients.do(t, chain.baseURL, chain.bearer, c.auth, spec)
				}
				checkWireGolden(t, filepath.Join(wireGoldenDir, rel), file)
			})
		}
	}
	checkNoStaleGoldens(t, produced)
}

func checkWireGolden(t *testing.T, path string, got goldenFile) {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	require.NoError(t, enc.Encode(got))
	gotJSON := buf.Bytes()

	if *updateWireGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, gotJSON, 0o644))
		return
	}
	wantJSON, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no golden %s: record it on the connect-v1 commit with `go test ./internal/grpc -run TestWireGolden -update`", path)
	}
	require.NoError(t, err)
	if bytes.Equal(wantJSON, gotJSON) {
		return
	}
	var want, gotAny any
	require.NoError(t, json.Unmarshal(wantJSON, &want))
	require.NoError(t, json.Unmarshal(gotJSON, &gotAny))
	t.Errorf("the wire changed for %s (-golden +now).\n"+
		"A diff is a regression, or an intended wire change that the migration PR must list; do not -update to silence it.\n%s",
		path, cmp.Diff(want, gotAny))
}

func checkNoStaleGoldens(t *testing.T, produced map[string]bool) {
	t.Helper()
	var onDisk []string
	_ = filepath.WalkDir(wireGoldenDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".json") {
			rel, _ := filepath.Rel(wireGoldenDir, p)
			onDisk = append(onDisk, rel)
		}
		return nil
	})
	for _, rel := range onDisk {
		if produced[rel] {
			continue
		}
		if *updateWireGolden {
			require.NoError(t, os.Remove(filepath.Join(wireGoldenDir, rel)))
			continue
		}
		t.Errorf("stale golden %s: no case produces it any more (run with -update to remove)", rel)
	}
}
