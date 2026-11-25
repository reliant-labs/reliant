package middleware

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/reliant-labs/forge/pkg/auth"
)

const (
	echoProcedure  = "/test.v1.EchoService/Echo"
	watchProcedure = "/test.v1.EchoService/Watch"
)

// echoHandler answers a call from its context: the reply text, or an error.
type echoHandler func(ctx context.Context) (string, error)

// testContextV2 plays the interceptors that run before the ones under test:
// it stashes the caller named by X-Test-User and the request id named by
// X-Request-Id, as auth and the request-id middleware would.
func testContextV2(next connectv2.ServerFunc) connectv2.ServerFunc {
	return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
		if info, ok := connectv2.CallInfoForServerContext(ctx); ok {
			ctx = testContext(ctx, info.RequestHeader().Get)
		}
		return next(ctx, spec, stream)
	}
}

// testContextV1 is testContextV2 for a v1 chain.
var testContextV1 = connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return next(testContext(ctx, req.Header().Get), req)
	}
})

func testContext(ctx context.Context, header func(string) string) context.Context {
	if id := header("X-Test-User"); id != "" {
		ctx = rlContextWithClaims(ctx, &auth.Claims{UserID: id, Email: id + "@example.com"})
	}
	if rid := header("X-Request-Id"); rid != "" {
		ctx = ContextWithRequestID(ctx, rid)
	}
	return ctx
}

// serveV2 mounts a v2 server with a unary Echo and a server-streaming Watch
// behind ints and returns a client for it.
func serveV2(t *testing.T, h echoHandler, ints ...connectv2.ServerInterceptor) *connectv2.Client {
	t.Helper()
	serve := func(ctx context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
		var req wrapperspb.StringValue
		if err := stream.Receive(&req); err != nil {
			return err
		}
		out, err := h(ctx)
		if err != nil {
			return err
		}
		return stream.Send(wrapperspb.String(out))
	}
	srv := connectv2.NewServer(ints...)
	srv.Register(
		connectv2.Method{Spec: connectv2.Spec{StreamType: connectv2.StreamTypeUnary, Procedure: echoProcedure}, Handler: serve},
		connectv2.Method{Spec: connectv2.Spec{StreamType: connectv2.StreamTypeServer, Procedure: watchProcedure}, Handler: serve},
	)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, srv)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	return connectv2.NewClient(connecthttp.NewTransport(hs.Client(), hs.URL))
}

// v2Reply is what a v2 call returned: the reply, its response headers, and
// the error.
type v2Reply struct {
	value  string
	header *connectv2.Header
	err    error
}

// callV2 calls Echo, or Watch when stream is set, with headers.
func callV2(t *testing.T, c *connectv2.Client, stream bool, headers map[string]string) v2Reply {
	t.Helper()
	ctx, info := connectv2.NewClientContext(context.Background())
	for k, v := range headers {
		info.RequestHeader().Set(k, v)
	}
	if !stream {
		var res wrapperspb.StringValue
		err := c.CallUnary(ctx, connectv2.Spec{StreamType: connectv2.StreamTypeUnary, Procedure: echoProcedure}, wrapperspb.String("hi"), &res)
		return v2Reply{value: res.GetValue(), header: info.ResponseHeader(), err: err}
	}
	cs, err := c.CallServerStream(ctx, connectv2.Spec{StreamType: connectv2.StreamTypeServer, Procedure: watchProcedure}, wrapperspb.String("hi"))
	if err != nil {
		return v2Reply{err: err}
	}
	defer cs.Close()
	var res wrapperspb.StringValue
	if err := cs.Receive(&res); err != nil {
		return v2Reply{err: err}
	}
	if err := cs.Receive(&wrapperspb.StringValue{}); !errors.Is(err, io.EOF) {
		return v2Reply{err: err}
	}
	return v2Reply{value: res.GetValue(), header: info.ResponseHeader()}
}

// serveV1 serves the same unary Echo on connect-go v1 behind ints and
// returns a function calling it.
func serveV1(t *testing.T, h echoHandler, ints ...connect.Interceptor) func(headers map[string]string) error {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(echoProcedure, connect.NewUnaryHandler(echoProcedure,
		func(ctx context.Context, _ *connect.Request[wrapperspb.StringValue]) (*connect.Response[wrapperspb.StringValue], error) {
			out, err := h(ctx)
			if err != nil {
				return nil, err
			}
			return connect.NewResponse(wrapperspb.String(out)), nil
		}, connect.WithInterceptors(ints...)))
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	client := connect.NewClient[wrapperspb.StringValue, wrapperspb.StringValue](hs.Client(), hs.URL+echoProcedure)
	return func(headers map[string]string) error {
		req := connect.NewRequest(wrapperspb.String("hi"))
		for k, v := range headers {
			req.Header().Set(k, v)
		}
		_, err := client.CallUnary(context.Background(), req)
		return err
	}
}

func okHandler(context.Context) (string, error) { return "ok", nil }

func TestRateLimitInterceptorV2_Disabled(t *testing.T) {
	if RateLimitInterceptorV2(RateLimitOptions{Rps: 0}, nil) != nil || RateLimitInterceptorV2(RateLimitOptions{Rps: -1, Burst: 10}, nil) != nil {
		t.Fatal("expected a nil interceptor when Rps <= 0")
	}
}

// The burst is enforced per peer IP, over unary and streaming calls alike,
// and a throttled call carries the same code and message as v1's.
func TestRateLimitInterceptorV2_EnforcesBurstWithV1WireError(t *testing.T) {
	c := serveV2(t, okHandler, RateLimitInterceptorV2(RateLimitOptions{Rps: 1, Burst: 2}, nil))
	if r := callV2(t, c, false, nil); r.err != nil {
		t.Fatalf("first call: %v", r.err)
	}
	if r := callV2(t, c, true, nil); r.err != nil {
		t.Fatalf("second call (a stream, within burst): %v", r.err)
	}
	errV2 := callV2(t, c, false, nil).err

	callV1 := serveV1(t, okHandler, RateLimitInterceptor(RateLimitOptions{Rps: 1, Burst: 2}, nil))
	_ = callV1(nil)
	_ = callV1(nil)
	errV1 := callV1(nil)

	e1, ok1 := errors.AsType[*connect.Error](errV1)
	e2, ok2 := errors.AsType[*connectv2.Error](errV2)
	if !ok1 || !ok2 {
		t.Fatalf("third calls: v1 %v, v2 %v; want both throttled", errV1, errV2)
	}
	if e2.Code() != connectv2.CodeResourceExhausted || uint32(e2.Code()) != uint32(e1.Code()) || e2.Message() != e1.Message() {
		t.Errorf("v2 %q (%v), v1 %q (%v); want the same throttle error", e2.Message(), e2.Code(), e1.Message(), e1.Code())
	}
}

// Authenticated callers are keyed by subject, so one user exhausting the
// bucket does not throttle another on the same IP.
func TestRateLimitInterceptorV2_KeysByClaimSubject(t *testing.T) {
	c := serveV2(t, okHandler, testContextV2, RateLimitInterceptorV2(RateLimitOptions{Rps: 1, Burst: 1}, rlClaimsFromContext))
	if r := callV2(t, c, false, map[string]string{"X-Test-User": "a"}); r.err != nil {
		t.Fatal(r.err)
	}
	if r := callV2(t, c, false, map[string]string{"X-Test-User": "a"}); connectv2.CodeOf(r.err) != connectv2.CodeResourceExhausted {
		t.Fatalf("user a's second call: %v, want throttled", r.err)
	}
	if r := callV2(t, c, false, map[string]string{"X-Test-User": "b"}); r.err != nil {
		t.Fatalf("user b shares a's IP but not its bucket: %v", r.err)
	}
}

// The v2 audit record is the v1 record, field for field, on success and on
// failure.
func TestAuditInterceptorWithSinkV2_MatchesV1(t *testing.T) {
	for name, tc := range map[string]struct {
		v1, v2 echoHandler
	}{
		"success": {okHandler, okHandler},
		"denied": {
			v1: func(context.Context) (string, error) {
				return "", connect.NewError(connect.CodePermissionDenied, errors.New("nope"))
			},
			v2: func(context.Context) (string, error) {
				return "", connectv2.NewError(connectv2.CodePermissionDenied, "nope")
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var buf1, buf2 bytes.Buffer
			sink1, sink2 := newFakeSink(), newFakeSink()
			headers := map[string]string{"X-Test-User": "u-7", "X-Request-Id": "req-1"}

			a1 := newAuditInterceptor(slog.New(slog.NewJSONHandler(&buf1, nil)), rlClaimsFromContext, sink1)
			a2 := newAuditInterceptorV2(slog.New(slog.NewJSONHandler(&buf2, nil)), rlClaimsFromContext, sink2)
			clock1, clock2 := newSteppingClock(), newSteppingClock()
			a1.now, a2.now = clock1.now, clock2.now
			_ = serveV1(t, tc.v1, testContextV1, a1)(headers)
			_ = callV2(t, serveV2(t, tc.v2, testContextV2, a2.wrapV2), false, headers)

			ev1, ev2 := sink1.waitOne(t), sink2.waitOne(t)
			if ev2.Procedure != echoProcedure || !strings.HasPrefix(ev2.PeerAddress, "127.0.0.1:") {
				t.Errorf("v2 event procedure %q peer %q", ev2.Procedure, ev2.PeerAddress)
			}
			clock1.assertTiming(t, ev1)
			clock2.assertTiming(t, ev2)
			ev1.PeerAddress, ev2.PeerAddress = "", ""
			if ev1 != ev2 {
				t.Errorf("audit events differ:\n  v1 %+v\n  v2 %+v", ev1, ev2)
			}
			if ev2.UserID != "u-7" || ev2.Email != "u-7@example.com" || ev2.RequestID != "req-1" {
				t.Errorf("v2 event identity/correlation: %+v", ev2)
			}
			for _, field := range []string{`"msg":"audit.event"`, `"log_type":"audit"`, `"user_id":"u-7"`, `"request_id":"req-1"`} {
				if !strings.Contains(buf2.String(), field) {
					t.Errorf("v2 slog record lacks %s: %s", field, buf2.String())
				}
			}
			if name == "denied" {
				if ev2.ErrorCode != "permission_denied" || ev2.ErrorMessage != "permission_denied: nope" || ev2.Status != "error" {
					t.Errorf("v2 error fields: %+v", ev2)
				}
				// Same severity as the v1 record (a rejected request is INFO).
				lvl := func(s string) string {
					_, after, _ := strings.Cut(s, `"level":"`)
					v, _, _ := strings.Cut(after, `"`)
					return v
				}
				if lvl(buf1.String()) != lvl(buf2.String()) {
					t.Errorf("v1 logged at %s, v2 at %s", lvl(buf1.String()), lvl(buf2.String()))
				}
			}
		})
	}
}

// A stream is audited once, when it ends, as v1's streaming handler was.
func TestAuditInterceptorV2_Streaming(t *testing.T) {
	sink := newFakeSink()
	var buf bytes.Buffer
	ic := AuditInterceptorWithSinkV2(slog.New(slog.NewJSONHandler(&buf, nil)), nil, sink)
	if r := callV2(t, serveV2(t, okHandler, ic), true, nil); r.err != nil {
		t.Fatal(r.err)
	}
	ev := sink.waitOne(t)
	if ev.Procedure != watchProcedure || ev.Status != "ok" || ev.UserID != "anonymous" {
		t.Errorf("stream event = %+v", ev)
	}
	if AuditInterceptorV2(nil, nil) == nil {
		t.Error("AuditInterceptorV2 must build a slog-only interceptor with a nil logger")
	}
}

func TestErrorCode(t *testing.T) {
	v1 := connect.NewError(connect.CodeNotFound, errors.New("x"))
	v2 := connectv2.NewError(connectv2.CodePermissionDenied, "y")
	v2OverV1 := v2.WithCause(v1)
	for _, tc := range []struct {
		err      error
		preferV2 bool
		want     string
	}{
		{v1, false, "not_found"},
		{v1, true, "not_found"},
		{v2, false, "permission_denied"},
		{v2, true, "permission_denied"},
		{v2OverV1, true, "permission_denied"}, // the outermost verdict
		{v2OverV1, false, "not_found"},        // v1's connect.CodeOf, unchanged
		{errors.New("plain"), true, "unknown"},
		{errors.New("plain"), false, "unknown"},
	} {
		if got := errorCode(tc.err, tc.preferV2); got != tc.want {
			t.Errorf("errorCode(%v, preferV2=%v) = %q, want %q", tc.err, tc.preferV2, got, tc.want)
		}
	}
}

func TestIdempotencyInterceptorV2_Disabled(t *testing.T) {
	if IdempotencyInterceptorV2(IdempotencyOptions{CacheSize: 0}, rlClaimsFromContext) != nil || IdempotencyInterceptorV2(IdempotencyOptions{CacheSize: -1}, rlClaimsFromContext) != nil {
		t.Fatal("expected a nil interceptor when CacheSize <= 0")
	}
	if IdempotencyInterceptorV2(IdempotencyOptions{CacheSize: 10}, nil) != nil {
		t.Fatal("expected a nil interceptor when there is no caller lookup")
	}
}

// A repeated Idempotency-Key replays the first answer — its message and the
// headers the handler set — without running the handler again; the headers
// outer interceptors set stay the current request's.
func TestIdempotencyInterceptorV2_ReplaysTheFirstAnswer(t *testing.T) {
	var runs atomic.Int32
	h := func(ctx context.Context) (string, error) {
		n := runs.Add(1)
		info, _ := connectv2.CallInfoForServerContext(ctx)
		info.ResponseHeader().Set("X-Handler", "run-"+string(rune('0'+n)))
		return "answer-" + string(rune('0'+n)), nil
	}
	var outer atomic.Int32
	stampOuter := func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			info, _ := connectv2.CallInfoForServerContext(ctx)
			info.ResponseHeader().Set("X-Outer", "call-"+string(rune('0'+outer.Add(1))))
			return next(ctx, spec, stream)
		}
	}
	c := serveV2(t, h, stampOuter, testContextV2, IdempotencyInterceptorV2(IdempotencyOptions{CacheSize: 10}, rlClaimsFromContext))
	key := map[string]string{IdempotencyKeyHeader: "k1", "X-Test-User": "alice"}

	first := callV2(t, c, false, key)
	second := callV2(t, c, false, key)
	if first.err != nil || second.err != nil {
		t.Fatalf("calls: %v, %v", first.err, second.err)
	}
	if runs.Load() != 1 {
		t.Fatalf("handler ran %d times for one key, want 1", runs.Load())
	}
	if second.value != "answer-1" || second.header.Get("X-Handler") != "run-1" {
		t.Errorf("replay = %q with X-Handler %q, want answer-1 / run-1", second.value, second.header.Get("X-Handler"))
	}
	if second.header.Get("X-Outer") != "call-2" {
		t.Errorf("replay overwrote the current request's outer header: X-Outer %q, want call-2", second.header.Get("X-Outer"))
	}

	if r := callV2(t, c, false, map[string]string{IdempotencyKeyHeader: "k2", "X-Test-User": "alice"}); r.value != "answer-2" {
		t.Errorf("a new key must run the handler, got %q", r.value)
	}
	if r := callV2(t, c, false, map[string]string{"X-Test-User": "alice"}); r.value != "answer-3" {
		t.Errorf("no key must run the handler, got %q", r.value)
	}
	// Streams are not deduplicated: the same key on Watch runs again.
	if r := callV2(t, c, true, key); r.err != nil || r.value != "answer-4" {
		t.Errorf("a stream with a seen key must run the handler, got %q (%v)", r.value, r.err)
	}
}

func TestIdempotencyInterceptorV2_ReplaysTheError(t *testing.T) {
	var runs atomic.Int32
	c := serveV2(t, func(context.Context) (string, error) {
		runs.Add(1)
		return "", connectv2.NewError(connectv2.CodeAlreadyExists, "duplicate")
	}, testContextV2, IdempotencyInterceptorV2(IdempotencyOptions{CacheSize: 10, TTL: time.Minute}, rlClaimsFromContext))
	key := map[string]string{IdempotencyKeyHeader: "k", "X-Test-User": "alice"}
	e1, e2 := callV2(t, c, false, key).err, callV2(t, c, false, key).err
	if runs.Load() != 1 {
		t.Fatalf("handler ran %d times, want 1", runs.Load())
	}
	for _, err := range []error{e1, e2} {
		if ce, ok := errors.AsType[*connectv2.Error](err); !ok || ce.Code() != connectv2.CodeAlreadyExists || ce.Message() != "duplicate" {
			t.Errorf("call err = %v, want already_exists: duplicate", err)
		}
	}
}

// callerContextV2 stashes the caller named by X-Test-User in the tenant
// named by X-Test-Org, as auth would.
func callerContextV2(next connectv2.ServerFunc) connectv2.ServerFunc {
	return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
		if info, ok := connectv2.CallInfoForServerContext(ctx); ok && info.RequestHeader().Get("X-Test-User") != "" {
			ctx = rlContextWithClaims(ctx, &auth.Claims{
				UserID: info.RequestHeader().Get("X-Test-User"),
				OrgID:  info.RequestHeader().Get("X-Test-Org"),
			})
		}
		return next(ctx, spec, stream)
	}
}

// The v2 cache is scoped to the caller: a principal — or the same user id in
// another tenant — reusing someone else's (procedure, key) pair runs the
// handler and gets its own answer, while each caller's repeat replays theirs.
func TestIdempotencyInterceptorV2_ScopesTheCacheToTheCaller(t *testing.T) {
	var runs atomic.Int32
	h := func(ctx context.Context) (string, error) {
		claims, _ := rlClaimsFromContext(ctx)
		return claims.OrgID + "/" + claims.UserID + "#" + string(rune('0'+runs.Add(1))), nil
	}
	c := serveV2(t, h, callerContextV2, IdempotencyInterceptorV2(IdempotencyOptions{CacheSize: 10}, rlClaimsFromContext))
	as := func(org, user string) map[string]string {
		return map[string]string{IdempotencyKeyHeader: "shared-key", "X-Test-Org": org, "X-Test-User": user}
	}

	for _, step := range []struct {
		org, user, want string
	}{
		{"acme", "alice", "acme/alice#1"},
		{"acme", "bob", "acme/bob#2"},         // another principal: not alice's answer
		{"globex", "alice", "globex/alice#3"}, // same user id, another tenant
		{"acme", "alice", "acme/alice#1"},     // alice's repeat replays hers
		{"acme", "bob", "acme/bob#2"},         // and bob's replays his
	} {
		r := callV2(t, c, false, as(step.org, step.user))
		if r.err != nil || r.value != step.want {
			t.Errorf("%s/%s: got %q (%v), want %q", step.org, step.user, r.value, r.err, step.want)
		}
	}
	if runs.Load() != 3 {
		t.Errorf("handler ran %d times, want 3 (once per caller)", runs.Load())
	}
}

// The cache is scoped to the authority presented, not just the person: a
// token-authorized answer is never replayed to an API key issued to the same
// user and org (a handler may refuse keys), nor to a different key, nor to
// the same caller holding a different role.
func TestIdempotencyInterceptorV2_ScopesTheCacheToTheCredential(t *testing.T) {
	var runs atomic.Int32
	h := func(ctx context.Context) (string, error) {
		claims, _ := rlClaimsFromContext(ctx)
		if claims.Credential.IsAPIKey() {
			return "", connectv2.NewError(connectv2.CodePermissionDenied, "keys may not call this")
		}
		return claims.Role + "#" + string(rune('0'+runs.Add(1))), nil
	}
	withCredential := func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			info, _ := connectv2.CallInfoForServerContext(ctx)
			claims := &auth.Claims{UserID: "alice", OrgID: "acme", Role: info.RequestHeader().Get("X-Test-Role")}
			if prefix := info.RequestHeader().Get("X-Test-Key"); prefix != "" {
				claims.Credential = auth.APIKeyCredential(prefix)
			} else {
				claims.Credential = auth.TokenCredential()
			}
			return next(rlContextWithClaims(ctx, claims), spec, stream)
		}
	}
	c := serveV2(t, h, withCredential, IdempotencyInterceptorV2(IdempotencyOptions{CacheSize: 10}, rlClaimsFromContext))
	call := func(key, role string) v2Reply {
		hdr := map[string]string{IdempotencyKeyHeader: "shared-key", "X-Test-Role": role}
		if key != "" {
			hdr["X-Test-Key"] = key
		}
		return callV2(t, c, false, hdr)
	}

	if r := call("", "admin"); r.err != nil || r.value != "admin#1" {
		t.Fatalf("token call = %q (%v), want admin#1", r.value, r.err)
	}
	if r := call("fk_live_abc", "admin"); connectv2.CodeOf(r.err) != connectv2.CodePermissionDenied {
		t.Errorf("API key for the same user/org got %q (%v), want the handler's permission_denied", r.value, r.err)
	}
	if r := call("", "viewer"); r.err != nil || r.value != "viewer#2" {
		t.Errorf("same token user with another role = %q (%v), want a fresh viewer#2", r.value, r.err)
	}
	if r := call("", "admin"); r.err != nil || r.value != "admin#1" {
		t.Errorf("the token caller's own repeat = %q (%v), want its replay admin#1", r.value, r.err)
	}
}

// A call with no authenticated caller is never cached or replayed, so two
// anonymous calls with one key both run the handler.
func TestIdempotencyInterceptorV2_NoIdentityNoReplay(t *testing.T) {
	var runs atomic.Int32
	h := func(context.Context) (string, error) {
		return "answer-" + string(rune('0'+runs.Add(1))), nil
	}
	c := serveV2(t, h, callerContextV2, IdempotencyInterceptorV2(IdempotencyOptions{CacheSize: 10}, rlClaimsFromContext))
	key := map[string]string{IdempotencyKeyHeader: "k"}
	for i, want := range []string{"answer-1", "answer-2"} {
		if r := callV2(t, c, false, key); r.err != nil || r.value != want {
			t.Errorf("anonymous call %d = %q (%v), want %q", i+1, r.value, r.err, want)
		}
	}
	// An anonymous answer was not cached for a later authenticated caller.
	key["X-Test-User"] = "alice"
	if r := callV2(t, c, false, key); r.value != "answer-3" {
		t.Errorf("first authenticated call = %q, want answer-3", r.value)
	}
}
