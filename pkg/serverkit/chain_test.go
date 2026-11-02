package serverkit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/reliant-labs/forge/pkg/authn"
	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// A chain authenticates: a missing Auth, an audience-less one, or one wrapping
// no interceptor fails at NewChain, so a server with no authenticator is a
// boot error and never a running process.
func TestNewChain_RequiresAuth(t *testing.T) {
	cases := map[string]struct {
		deps serverkit.ChainDeps
		want string
	}{
		"nil auth":           {serverkit.ChainDeps{}, "ChainDeps.Auth is required"},
		"no audience":        {serverkit.ChainDeps{Auth: serverkit.AuthFor("", tokenAuth{})}, "names no audience"},
		"nil interceptor":    {serverkit.ChainDeps{Auth: serverkit.AuthFor(serverkit.AudienceUser, nil)}, "wraps a nil interceptor"},
		"omit in wrong slot": {serverkit.ChainDeps{Auth: serverkit.AuthFor(serverkit.AudienceUser, tokenAuth{}), Audit: serverkit.NoTrace}, `ChainDeps.Audit is serverkit.Omit("trace")`},
		"omit as deadline":   {serverkit.ChainDeps{Auth: serverkit.AuthFor(serverkit.AudienceUser, tokenAuth{}), Deadline: serverkit.NoAudit}, `ChainDeps.Deadline is serverkit.Omit("audit")`},
		"bad deadline key":   {serverkit.ChainDeps{Auth: serverkit.AuthFor(serverkit.AudienceUser, tokenAuth{}), Deadline: serverkit.Deadline{Procedures: map[string]time.Duration{"svc.v1.Svc/Do": time.Second}}}, `"svc.v1.Svc/Do" is not a procedure`},
		"omit as rate limit": {serverkit.ChainDeps{Auth: serverkit.AuthFor(serverkit.AudienceUser, tokenAuth{}), RateLimit: serverkit.NoAudit}, "ChainDeps.RateLimit"},
		"nil extra":          {serverkit.ChainDeps{Auth: serverkit.AuthFor(serverkit.AudienceUser, tokenAuth{}), Extras: []connect.Interceptor{nil}}, "Extras[0] is nil"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, err := serverkit.NewChain(tc.deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NewChain = %v, %v; want an error containing %q", c, err, tc.want)
			}
		})
	}
}

// recorder is an interceptor that records, in order, which slot it fills.
type recorder struct {
	name string
	mu   *sync.Mutex
	got  *[]string
}

// Claims names forge authn's claims reader, as authn's own interceptor does, so
// AuthFor gives this fixture a Caller and the chain's default Audit and
// Activity slots build.
func (recorder) Claims() func(context.Context) (*authn.Claims, bool) { return authn.ClaimsFromContext }

func (r recorder) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		r.mu.Lock()
		*r.got = append(*r.got, r.name)
		r.mu.Unlock()
		return next(ctx, req)
	}
}

func (r recorder) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (r recorder) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// The order a request passes through the chain is the order serve.go used to
// spell inline: the span outermost, then the deadline before auth (the RPC's
// budget bounds authenticating), auth before audit (audit knows who called),
// rate limit, the project's extras, the activity stream (next to the handler,
// so it judges the handler's outcome), the stream drain, and protovalidate
// innermost, right before the handler.
func TestNewChain_SlotOrder(t *testing.T) {
	var (
		mu  sync.Mutex
		got []string
	)
	rec := func(name string) recorder { return recorder{name: name, mu: &mu, got: &got} }
	chain, err := serverkit.NewChain(serverkit.ChainDeps{
		Auth:        serverkit.AuthFor(serverkit.AudienceUser, rec("auth")),
		Trace:       rec("trace"),
		Deadline:    rec("deadline"),
		Audit:       rec("audit"),
		RateLimit:   rec("rate-limit"),
		Extras:      []connect.Interceptor{rec("extra-1"), rec("extra-2")},
		Activity:    rec("activity"),
		StreamDrain: rec("stream-drain"),
		Validate:    rec("validate"),
	})
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	const procedure = "/serverkit.chain.v1.Svc/Do"
	mux := http.NewServeMux()
	mux.Handle(procedure, connect.NewUnaryHandler(procedure,
		func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			mu.Lock()
			got = append(got, "handler")
			mu.Unlock()
			return connect.NewResponse(&emptypb.Empty{}), nil
		}, chain.HandlerOptions()...))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := connect.NewClient[emptypb.Empty, emptypb.Empty](srv.Client(), srv.URL+procedure)
	if _, err := client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{})); err != nil {
		t.Fatalf("call: %v", err)
	}
	want := []string{"trace", "deadline", "auth", "audit", "rate-limit", "extra-1", "extra-2", "activity", "stream-drain", "validate", "handler"}
	if !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if chain.Audience() != serverkit.AudienceUser {
		t.Fatalf("Audience = %q", chain.Audience())
	}
}

// Every slot but Auth defaults, and only its Omit drops it: a chain with
// every default dropped is exactly observe's fixed core plus Auth.
func TestNewChain_DefaultsAndOmits(t *testing.T) {
	auth := serverkit.AuthFor(serverkit.AudienceUser, tokenAuth{token: "t"})
	full, err := serverkit.NewChain(serverkit.ChainDeps{Auth: auth})
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	bare, err := serverkit.NewChain(serverkit.ChainDeps{
		Auth:        auth,
		Trace:       serverkit.NoTrace,
		Deadline:    serverkit.NoDeadline,
		Audit:       serverkit.NoAudit,
		Activity:    serverkit.NoActivity,
		StreamDrain: serverkit.NoStreamDrain,
		Validate:    serverkit.NoValidate,
	})
	if err != nil {
		t.Fatalf("NewChain (all omitted): %v", err)
	}
	core := len(observe.Chain(observe.Deps{Auth: auth, Deadline: serverkit.NoDeadline}))
	// The svcerr interceptor is not a slot: observe.Chain always ends with it,
	// so it is already part of core.
	if n := len(bare.Interceptors()); n != core {
		t.Fatalf("all defaults omitted: %d interceptors, want observe's core (%d)", n, core)
	}
	// trace (+ its profile-labels and traceresponse companions), deadline,
	// audit, activity, stream drain, validate.
	if n := len(full.Interceptors()); n != core+8 {
		t.Fatalf("all defaults: %d interceptors, want %d", n, core+8)
	}
}

// The payload caps default to 4 MiB, because connect-go reads a whole body
// before any interceptor can refuse it and treats a zero cap as unlimited.
func TestNewChain_PayloadCapDefault(t *testing.T) {
	const procedure = "/serverkit.chain.v1.Svc/Echo"
	for name, tc := range map[string]struct {
		read int
		want connect.Code
	}{
		"zero is 4 MiB":         {0, connect.CodeResourceExhausted},
		"negative is unlimited": {-1, 0},
	} {
		t.Run(name, func(t *testing.T) {
			chain, err := serverkit.NewChain(serverkit.ChainDeps{
				Auth:         serverkit.AuthFor(serverkit.AudienceUser, recorder{mu: &sync.Mutex{}, got: new([]string)}),
				ReadMaxBytes: tc.read,
			})
			if err != nil {
				t.Fatalf("NewChain: %v", err)
			}
			mux := http.NewServeMux()
			mux.Handle(procedure, connect.NewUnaryHandler(procedure,
				func(_ context.Context, req *connect.Request[wrapperspb.BytesValue]) (*connect.Response[emptypb.Empty], error) {
					return connect.NewResponse(&emptypb.Empty{}), nil
				}, chain.HandlerOptions()...))
			srv := httptest.NewServer(mux)
			defer srv.Close()
			client := connect.NewClient[wrapperspb.BytesValue, emptypb.Empty](srv.Client(), srv.URL+procedure)
			_, err = client.CallUnary(context.Background(), connect.NewRequest(wrapperspb.Bytes(bytes.Repeat([]byte{'x'}, serverkit.DefaultMaxBytes+1))))
			if got := connect.CodeOf(err); err != nil && got != tc.want || err == nil && tc.want != 0 {
				t.Fatalf("5 MiB request: err = %v, want code %v", err, tc.want)
			}
		})
	}
}

// The default Trace slot is otelconnect WithTrustRemote: an inbound
// traceparent (the browser's) becomes the PARENT of the server span, so a
// click and the API call it caused share one trace. The control — otelconnect
// without the option — only links to it, and the handler sees another trace.
func TestNewChain_DefaultTraceTrustsRemoteParent(t *testing.T) {
	// A recording tracer: the no-op one hands back whatever span context the
	// request context carries, which cannot tell a parent from a link.
	prevProp, prevTP := otel.GetTextMapPropagator(), otel.GetTracerProvider()
	tp := sdktrace.NewTracerProvider()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTextMapPropagator(prevProp)
		otel.SetTracerProvider(prevTP)
		_ = tp.Shutdown(context.Background())
	})

	const (
		procedure   = "/serverkit.chain.v1.Svc/Trace"
		inbound     = "4bf92f3577b34da6a3ce929d0e0e4736"
		traceparent = "00-" + inbound + "-00f067aa0ba902b7-01"
	)
	untrusted, err := otelconnect.NewInterceptor()
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		trace connect.Interceptor
		same  bool
	}{
		"default":          {nil, true},
		"untrusted remote": {untrusted, false},
	} {
		t.Run(name, func(t *testing.T) {
			chain, err := serverkit.NewChain(serverkit.ChainDeps{
				Auth:  serverkit.AuthFor(serverkit.AudienceUser, recorder{mu: &sync.Mutex{}, got: new([]string)}),
				Trace: tc.trace,
			})
			if err != nil {
				t.Fatalf("NewChain: %v", err)
			}
			var seen string
			mux := http.NewServeMux()
			mux.Handle(procedure, connect.NewUnaryHandler(procedure,
				func(ctx context.Context, _ *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
					seen = oteltrace.SpanContextFromContext(ctx).TraceID().String()
					return connect.NewResponse(&emptypb.Empty{}), nil
				}, chain.HandlerOptions()...))
			srv := httptest.NewServer(mux)
			defer srv.Close()
			req := connect.NewRequest(&emptypb.Empty{})
			req.Header().Set("traceparent", traceparent)
			client := connect.NewClient[emptypb.Empty, emptypb.Empty](srv.Client(), srv.URL+procedure)
			if _, err := client.CallUnary(context.Background(), req); err != nil {
				t.Fatalf("call: %v", err)
			}
			if (seen == inbound) != tc.same {
				t.Fatalf("handler trace = %s, inbound %s; want same=%v", seen, inbound, tc.same)
			}
		})
	}
}

// A handler that returns a svcerr constructor's error unwrapped reaches the
// client with that constructor's Connect code, not CodeUnknown — and the same
// code is what the interceptors outside the handler (audit, logging) read.
func TestNewChain_HandlerSvcerrReachesClientWithItsCode(t *testing.T) {
	const procedure = "/serverkit.chain.v1.Svc/Err"
	for name, tc := range map[string]struct {
		err  error
		want connect.Code
		msg  string
	}{
		"permission denied": {svcerr.PermissionDenied("admins only"), connect.CodePermissionDenied, "admins only"},
		"not found":         {svcerr.NotFound("note"), connect.CodeNotFound, "note not found"},
		"already wrapped":   {svcerr.Wrap(svcerr.InvalidArgument("bad id")), connect.CodeInvalidArgument, "bad id"},
		"own connect error": {connect.NewError(connect.CodeUnauthenticated, errors.New("sign in")), connect.CodeUnauthenticated, "sign in"},
		"unrecognised":      {errors.New(`pq: relation "notes" does not exist`), connect.CodeInternal, svcerr.InternalMessage},
	} {
		t.Run(name, func(t *testing.T) {
			var seen error
			observer := errObserver{seen: &seen}
			chain, err := serverkit.NewChain(serverkit.ChainDeps{
				Auth:  serverkit.AuthFor(serverkit.AudienceUser, tokenAuth{token: "t"}),
				Audit: observer,
			})
			if err != nil {
				t.Fatalf("NewChain: %v", err)
			}
			mux := http.NewServeMux()
			mux.Handle(procedure, connect.NewUnaryHandler(procedure,
				func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
					return nil, tc.err
				}, chain.HandlerOptions()...))
			srv := httptest.NewServer(mux)
			defer srv.Close()

			client := connect.NewClient[emptypb.Empty, emptypb.Empty](srv.Client(), srv.URL+procedure)
			req := connect.NewRequest(&emptypb.Empty{})
			req.Header().Set("Authorization", "Bearer t")
			_, err = client.CallUnary(context.Background(), req)
			if got := connect.CodeOf(err); got != tc.want {
				t.Fatalf("client code = %v (%v), want %v", got, err, tc.want)
			}
			var ce *connect.Error
			if !errors.As(err, &ce) || ce.Message() != tc.msg {
				t.Fatalf("client message = %q, want %q", ce.Message(), tc.msg)
			}
			if got := connect.CodeOf(seen); got != tc.want {
				t.Fatalf("audit slot saw code %v, want %v: the error was converted outside it", got, tc.want)
			}
		})
	}
}

// errObserver records the error the handler's result carries at its position.
type errObserver struct{ seen *error }

func (o errObserver) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		resp, err := next(ctx, req)
		*o.seen = err
		return resp, err
	}
}

func (o errObserver) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (o errObserver) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// plainAuth is a project's own authenticator: it admits every call and keeps
// the caller under a context key only the project knows — the shape of an app
// whose auth is not forge/pkg/authn (reliant's stashes auth.WithUser).
type plainAuth struct{}

type plainUserKey struct{}

func (plainAuth) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return next(context.WithValue(ctx, plainUserKey{}, req.Header().Get("X-User")), req)
	}
}

func (plainAuth) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (plainAuth) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// plainCaller is that project's reader, as forge claims.
func plainCaller(ctx context.Context) (*authn.Claims, bool) {
	id, _ := ctx.Value(plainUserKey{}).(string)
	if id == "" {
		return nil, false
	}
	return &authn.Claims{UserID: id, Email: id + "@example.com"}, true
}

// The default Audit and Activity slots record the caller Auth admitted. An
// authenticator that does not say how to read it would have them record every
// call as anonymous — silently — so NewChain refuses, naming each slot and
// the ways out, instead of building that chain.
func TestNewChain_RefusesToGuessTheCaller(t *testing.T) {
	plain := serverkit.AuthFor(serverkit.AudienceUser, plainAuth{})
	if plain.Caller() != nil {
		t.Fatal("AuthFor over an interceptor with no claims reader must leave Caller nil")
	}

	_, err := serverkit.NewChain(serverkit.ChainDeps{Auth: plain})
	if err == nil {
		t.Fatal("NewChain built default Audit and Activity over an authenticator that cannot name its caller")
	}
	for _, want := range []string{
		`ChainDeps.Auth (audience "user") does not say how to read the caller`,
		"the default Audit would audit every call as anonymous",
		"the default Activity would record every event as system:anonymous",
		"serverkit.AuthWithCaller", "serverkit.NoAudit", "serverkit.NoActivity", "activity.Principal",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not say %q:\n%v", want, err)
		}
	}

	// Each way out builds: drop both slots, or fill them yourself.
	if _, err := serverkit.NewChain(serverkit.ChainDeps{Auth: plain, Audit: serverkit.NoAudit, Activity: serverkit.NoActivity}); err != nil {
		t.Errorf("both slots dropped: %v", err)
	}
	if _, err := serverkit.NewChain(serverkit.ChainDeps{Auth: plain, Audit: tokenAuth{}, Activity: tokenAuth{}}); err != nil {
		t.Errorf("both slots filled by the project: %v", err)
	}
	// Only the defaulted slot is refused.
	if _, err := serverkit.NewChain(serverkit.ChainDeps{Auth: plain, Audit: serverkit.NoAudit}); err == nil ||
		strings.Contains(err.Error(), "default Audit") || !strings.Contains(err.Error(), "default Activity") {
		t.Errorf("Audit dropped, Activity defaulted: want only the Activity refusal, got %v", err)
	}

	if _, err := serverkit.NewChain(serverkit.ChainDeps{Auth: serverkit.AuthWithCaller(serverkit.AudienceUser, plainAuth{}, nil)}); err == nil ||
		!strings.Contains(err.Error(), "AuthWithCaller was given a nil caller") {
		t.Errorf("AuthWithCaller(nil) must be refused, got %v", err)
	}
}

// forge's own authenticator names its caller without being told, so the
// scaffolded chain (AuthFor over authn.NewInterceptor) keeps every default.
func TestAuthFor_ReadsAuthnsCaller(t *testing.T) {
	ic, err := authn.NewInterceptor(authn.Policy{
		ValidatorConfigured: true,
		Validate:            func(string) (*authn.Claims, error) { return &authn.Claims{UserID: "u1"}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	a := serverkit.AuthFor(serverkit.AudienceUser, ic)
	if a.Caller() == nil {
		t.Fatal("AuthFor over authn's interceptor must read its claims")
	}
	if _, err := serverkit.NewChain(serverkit.ChainDeps{Auth: a}); err != nil {
		t.Fatalf("the scaffolded chain shape must build with every default: %v", err)
	}
	ctx := authn.ContextWithClaims(context.Background(), &authn.Claims{UserID: "u1"})
	if c, ok := a.Caller()(ctx); !ok || c.UserID != "u1" {
		t.Fatalf("Caller read %+v, %v; want u1", c, ok)
	}
}

// With AuthWithCaller, the defaults record the project's real caller: the
// default Audit's record names the user the project's own auth admitted.
func TestAuthWithCaller_DefaultsRecordTheProjectsCaller(t *testing.T) {
	var logs bytes.Buffer
	chain, err := serverkit.NewChain(serverkit.ChainDeps{
		Auth:   serverkit.AuthWithCaller(serverkit.AudienceUser, plainAuth{}, plainCaller),
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	const procedure = "/serverkit.chain.v1.Svc/Do"
	mux := http.NewServeMux()
	mux.Handle(procedure, connect.NewUnaryHandler(procedure,
		func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			return connect.NewResponse(&emptypb.Empty{}), nil
		}, chain.HandlerOptions()...))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req := connect.NewRequest(&emptypb.Empty{})
	req.Header().Set("X-User", "u_42")
	if _, err := connect.NewClient[emptypb.Empty, emptypb.Empty](srv.Client(), srv.URL+procedure).CallUnary(context.Background(), req); err != nil {
		t.Fatalf("call: %v", err)
	}
	var audit map[string]any
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, `"log_type":"audit"`) {
			if err := json.Unmarshal([]byte(line), &audit); err != nil {
				t.Fatal(err)
			}
		}
	}
	if audit == nil {
		t.Fatalf("no audit record was written:\n%s", logs.String())
	}
	if audit["user_id"] != "u_42" {
		t.Errorf("audit record user_id = %v, want the project's caller u_42:\n%v", audit["user_id"], audit)
	}
}
