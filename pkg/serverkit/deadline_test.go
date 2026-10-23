package serverkit_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/forge/pkg/authn"
	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/serverkit"
)

const (
	deadlineUnary  = "/serverkit.deadline.v1.Svc/Do"
	deadlineStream = "/serverkit.deadline.v1.Svc/Watch"
)

// stallingAuth is an authenticator whose credential lookup never returns on
// its own — a token store query against a wedged database. It records the
// deadline it ran under, then blocks until its context ends.
type stallingAuth struct {
	mu       *sync.Mutex
	deadline *time.Time
	ok       *bool
}

// Claims names forge authn's claims reader, as authn's own interceptor does, so
// AuthFor gives this fixture a Caller and the chain's default Audit and
// Activity slots build.
func (stallingAuth) Claims() func(context.Context) (*authn.Claims, bool) {
	return authn.ClaimsFromContext
}

func (a stallingAuth) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		d, ok := ctx.Deadline()
		a.mu.Lock()
		*a.deadline, *a.ok = d, ok
		a.mu.Unlock()
		<-ctx.Done()
		return nil, ctx.Err()
	}
}

func (a stallingAuth) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a stallingAuth) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// deadlineServer serves deadlineUnary and deadlineStream behind a chain built
// from deps, and reports the deadline each handler saw.
type deadlineServer struct {
	url  string
	mu   sync.Mutex
	seen map[string]time.Duration // procedure -> time left at the handler; -1 none
}

func newDeadlineServer(t *testing.T, deps serverkit.ChainDeps) *deadlineServer {
	t.Helper()
	if deps.Auth == nil {
		deps.Auth = serverkit.AuthFor(serverkit.AudienceUser, recorder{mu: &sync.Mutex{}, got: new([]string)})
	}
	chain, err := serverkit.NewChain(deps)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	return serveDeadline(t, chain.HandlerOptions()...)
}

// serveDeadline serves deadlineUnary and deadlineStream with opts.
func serveDeadline(t *testing.T, opts ...connect.HandlerOption) *deadlineServer {
	t.Helper()
	s := &deadlineServer{seen: map[string]time.Duration{}}
	record := func(ctx context.Context, procedure string) {
		left := time.Duration(-1)
		if d, ok := ctx.Deadline(); ok {
			left = time.Until(d)
		}
		s.mu.Lock()
		s.seen[procedure] = left
		s.mu.Unlock()
	}
	mux := http.NewServeMux()
	mux.Handle(deadlineUnary, connect.NewUnaryHandler(deadlineUnary,
		func(ctx context.Context, _ *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			record(ctx, deadlineUnary)
			return connect.NewResponse(&emptypb.Empty{}), nil
		}, opts...))
	mux.Handle(deadlineStream, connect.NewServerStreamHandler(deadlineStream,
		func(ctx context.Context, _ *connect.Request[emptypb.Empty], st *connect.ServerStream[emptypb.Empty]) error {
			record(ctx, deadlineStream)
			return st.Send(&emptypb.Empty{})
		}, opts...))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

func (s *deadlineServer) callUnary(ctx context.Context) error {
	client := connect.NewClient[emptypb.Empty, emptypb.Empty](http.DefaultClient, s.url+deadlineUnary)
	_, err := client.CallUnary(ctx, connect.NewRequest(&emptypb.Empty{}))
	return err
}

func (s *deadlineServer) callStream(ctx context.Context) error {
	client := connect.NewClient[emptypb.Empty, emptypb.Empty](http.DefaultClient, s.url+deadlineStream)
	st, err := client.CallServerStream(ctx, connect.NewRequest(&emptypb.Empty{}))
	if err != nil {
		return err
	}
	defer st.Close()
	for st.Receive() {
	}
	return st.Err()
}

func (s *deadlineServer) left(procedure string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[procedure]
}

// The RPC's deadline bounds authentication: Deadline runs before Auth, so a
// credential lookup that stalls ends with the RPC's DeadlineExceeded instead
// of holding the request until the client gives up. With the default slot
// the authenticator runs under DefaultUnaryDeadline.
func TestNewChain_DeadlineBoundsAuthentication(t *testing.T) {
	for name, tc := range map[string]struct {
		deadline connect.Interceptor
		budget   time.Duration
	}{
		"configured": {serverkit.Deadline{Unary: 50 * time.Millisecond, SlowAfter: -1}, 50 * time.Millisecond},
		"by procedure": {serverkit.Deadline{
			Procedures: map[string]time.Duration{deadlineUnary: 60 * time.Millisecond},
			SlowAfter:  -1,
		}, 60 * time.Millisecond},
	} {
		t.Run(name, func(t *testing.T) {
			var (
				mu       sync.Mutex
				deadline time.Time
				ok       bool
			)
			srv := newDeadlineServer(t, serverkit.ChainDeps{
				Auth:     serverkit.AuthFor(serverkit.AudienceUser, stallingAuth{mu: &mu, deadline: &deadline, ok: &ok}),
				Deadline: tc.deadline,
			})
			start := time.Now()
			err := srv.callUnary(context.Background())
			if got := connect.CodeOf(err); got != connect.CodeDeadlineExceeded {
				t.Fatalf("stalled authentication: err = %v (code %v), want DeadlineExceeded", err, got)
			}
			if took := time.Since(start); took > 5*time.Second {
				t.Fatalf("stalled authentication took %s: the deadline did not bound it", took)
			}
			mu.Lock()
			defer mu.Unlock()
			if !ok {
				t.Fatal("the authenticator ran with no deadline: Deadline must run before Auth")
			}
			if left := time.Until(deadline); left > tc.budget {
				t.Fatalf("authenticator deadline %s away, want within the %s budget", left, tc.budget)
			}
		})
	}

	t.Run("default", func(t *testing.T) {
		var got time.Duration
		auth := serverkit.AuthWithCaller(serverkit.AudienceUser, connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				if d, ok := ctx.Deadline(); ok {
					got = time.Until(d)
				}
				return next(ctx, req)
			}
		}), authn.ClaimsFromContext)
		srv := newDeadlineServer(t, serverkit.ChainDeps{Auth: auth})
		if err := srv.callUnary(context.Background()); err != nil {
			t.Fatalf("call: %v", err)
		}
		if got <= serverkit.DefaultUnaryDeadline-5*time.Second || got > serverkit.DefaultUnaryDeadline {
			t.Fatalf("authenticator saw a deadline %s away, want about DefaultUnaryDeadline (%s)", got, serverkit.DefaultUnaryDeadline)
		}
	})
}

// observe.Chain's Deadline slot has NewChain's one rule, since NewChain builds
// on it: nil bounds a unary call at DefaultUnaryDeadline, serverkit.NoDeadline
// leaves it unbounded, and a configured Deadline — control-plane passes one to
// observe.Chain explicitly — is used as given.
func TestObserveChain_DeadlineSlot(t *testing.T) {
	for name, tc := range map[string]struct {
		deadline connect.Interceptor
		want     time.Duration // -1: no deadline
	}{
		"nil is the default":  {nil, serverkit.DefaultUnaryDeadline},
		"NoDeadline opts out": {serverkit.NoDeadline, -1},
		"configured":          {serverkit.Deadline{Unary: 2 * time.Second}, 2 * time.Second},
		"pointer":             {&serverkit.Deadline{Unary: 3 * time.Second}, 3 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			ics := observe.Chain(observe.Deps{Deadline: tc.deadline})
			srv := serveDeadline(t, connect.WithInterceptors(ics...))
			if err := srv.callUnary(context.Background()); err != nil {
				t.Fatalf("call: %v", err)
			}
			left := srv.left(deadlineUnary)
			switch {
			case tc.want < 0 && left != -1:
				t.Fatalf("handler saw a deadline %s away, want none", left)
			case tc.want > 0 && (left <= tc.want-time.Second || left > tc.want):
				t.Fatalf("handler saw a deadline %s away, want about %s", left, tc.want)
			}
		})
	}
}

// Which budget an RPC gets: Procedures over Unary, zero or negative meaning
// none; streams unbounded unless named; and the server's budget is a ceiling
// a client's shorter deadline still undercuts.
func TestDeadline_Budgets(t *testing.T) {
	const (
		none   = time.Duration(-1)
		minute = time.Minute
	)
	for name, tc := range map[string]struct {
		deadline  connect.Interceptor
		client    time.Duration // the caller's own deadline; 0 none
		procedure string
		want      time.Duration // roughly; none is no deadline
	}{
		"default unary":               {nil, 0, deadlineUnary, serverkit.DefaultUnaryDeadline},
		"configured unary":            {serverkit.Deadline{Unary: minute}, 0, deadlineUnary, minute},
		"negative unary is none":      {serverkit.Deadline{Unary: -1}, 0, deadlineUnary, none},
		"procedure overrides":         {serverkit.Deadline{Unary: minute, Procedures: map[string]time.Duration{deadlineUnary: 2 * minute}}, 0, deadlineUnary, 2 * minute},
		"zero entry is none":          {serverkit.Deadline{Procedures: map[string]time.Duration{deadlineUnary: 0}}, 0, deadlineUnary, none},
		"omitted":                     {serverkit.NoDeadline, 0, deadlineUnary, none},
		"shorter client deadline":     {serverkit.Deadline{Unary: 2 * minute}, minute, deadlineUnary, minute},
		"longer client is capped":     {serverkit.Deadline{Unary: minute}, 2 * minute, deadlineUnary, minute},
		"default stream is unbound":   {nil, 0, deadlineStream, none},
		"configured unary not stream": {serverkit.Deadline{Unary: minute}, 0, deadlineStream, none},
		"named stream is bounded":     {serverkit.Deadline{Procedures: map[string]time.Duration{deadlineStream: minute}}, 0, deadlineStream, minute},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newDeadlineServer(t, serverkit.ChainDeps{Deadline: tc.deadline})
			ctx := context.Background()
			if tc.client > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.client)
				defer cancel()
			}
			call := srv.callUnary
			if tc.procedure == deadlineStream {
				call = srv.callStream
			}
			if err := call(ctx); err != nil {
				t.Fatalf("call: %v", err)
			}
			left := srv.left(tc.procedure)
			switch {
			case tc.want == none && left != none:
				t.Fatalf("handler deadline %s away, want none", left)
			case tc.want != none && (left <= tc.want-10*time.Second || left > tc.want):
				t.Fatalf("handler deadline %s away, want about %s", left, tc.want)
			}
		})
	}
}

// The watchdog reports a unary RPC while it is still running, then once when
// it completes; a fast RPC is never reported, and a negative SlowAfter turns
// it off.
func TestDeadline_Watchdog(t *testing.T) {
	type report struct {
		procedure string
		done      bool
	}
	run := func(t *testing.T, d serverkit.Deadline, handlerTime time.Duration) []report {
		t.Helper()
		var (
			mu  sync.Mutex
			got []report
		)
		d.OnSlow = func(_ context.Context, c serverkit.SlowCall) {
			mu.Lock()
			got = append(got, report{c.Procedure, c.Done})
			mu.Unlock()
		}
		chain, err := serverkit.NewChain(serverkit.ChainDeps{
			Auth:     serverkit.AuthFor(serverkit.AudienceUser, recorder{mu: &sync.Mutex{}, got: new([]string)}),
			Deadline: d,
		})
		if err != nil {
			t.Fatalf("NewChain: %v", err)
		}
		mux := http.NewServeMux()
		mux.Handle(deadlineUnary, connect.NewUnaryHandler(deadlineUnary,
			func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
				time.Sleep(handlerTime)
				return connect.NewResponse(&emptypb.Empty{}), nil
			}, chain.HandlerOptions()...))
		srv := httptest.NewServer(mux)
		defer srv.Close()
		client := connect.NewClient[emptypb.Empty, emptypb.Empty](srv.Client(), srv.URL+deadlineUnary)
		if _, err := client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{})); err != nil {
			t.Fatalf("call: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		return append([]report(nil), got...)
	}

	t.Run("slow", func(t *testing.T) {
		got := run(t, serverkit.Deadline{SlowAfter: 20 * time.Millisecond, SlowRepeat: time.Hour}, 150*time.Millisecond)
		want := []report{{deadlineUnary, false}, {deadlineUnary, true}}
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("reports = %v, want %v", got, want)
		}
	})
	t.Run("fast", func(t *testing.T) {
		if got := run(t, serverkit.Deadline{SlowAfter: time.Hour}, 0); len(got) != 0 {
			t.Fatalf("a fast RPC was reported: %v", got)
		}
	})
	t.Run("off", func(t *testing.T) {
		if got := run(t, serverkit.Deadline{SlowAfter: -1}, 50*time.Millisecond); len(got) != 0 {
			t.Fatalf("a negative SlowAfter still reported: %v", got)
		}
	})
}

// With no OnSlow the watchdog logs at WARN through the chain's logger, so the
// report lands where the request's other log lines do.
func TestDeadline_WatchdogLogsThroughTheChainLogger(t *testing.T) {
	var (
		mu  sync.Mutex
		buf bytes.Buffer
	)
	logger := slog.New(slog.NewTextHandler(&lockedWriter{mu: &mu, w: &buf}, nil))
	chain, err := serverkit.NewChain(serverkit.ChainDeps{
		Auth:     serverkit.AuthFor(serverkit.AudienceUser, recorder{mu: &sync.Mutex{}, got: new([]string)}),
		Logger:   logger,
		Deadline: &serverkit.Deadline{SlowAfter: 10 * time.Millisecond, SlowRepeat: time.Hour},
	})
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle(deadlineUnary, connect.NewUnaryHandler(deadlineUnary,
		func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			time.Sleep(100 * time.Millisecond)
			return nil, connect.NewError(connect.CodeInternal, errors.New("boom"))
		}, chain.HandlerOptions()...))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := connect.NewClient[emptypb.Empty, emptypb.Empty](srv.Client(), srv.URL+deadlineUnary)
	_, _ = client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{}))

	mu.Lock()
	out := buf.String()
	mu.Unlock()
	for _, want := range []string{
		`level=WARN msg="rpc still in flight" procedure=` + deadlineUnary,
		`level=WARN msg="rpc completed slowly" procedure=` + deadlineUnary,
		"failed=true",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("chain log missing %q:\n%s", want, out)
		}
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// A Deadline that would silently do something else is a NewChain error.
func TestDeadline_Validation(t *testing.T) {
	auth := serverkit.AuthFor(serverkit.AudienceUser, recorder{mu: &sync.Mutex{}, got: new([]string)})
	for name, tc := range map[string]struct {
		deadline connect.Interceptor
		want     string
	}{
		"no leading slash":  {serverkit.Deadline{Procedures: map[string]time.Duration{"pkg.v1.Svc/Do": time.Second}}, `"pkg.v1.Svc/Do" is not a procedure`},
		"no method":         {serverkit.Deadline{Procedures: map[string]time.Duration{"/pkg.v1.Svc/": time.Second}}, "is not a procedure"},
		"negative repeat":   {serverkit.Deadline{SlowRepeat: -time.Second}, "SlowRepeat"},
		"nil pointer":       {(*serverkit.Deadline)(nil), "nil *serverkit.Deadline"},
		"pointer validated": {&serverkit.Deadline{Procedures: map[string]time.Duration{"Do": time.Second}}, "is not a procedure"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := serverkit.NewChain(serverkit.ChainDeps{Auth: auth, Deadline: tc.deadline}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NewChain = %v; want an error containing %q", err, tc.want)
			}
		})
	}
}
