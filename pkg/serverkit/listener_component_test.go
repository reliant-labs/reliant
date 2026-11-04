package serverkit_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

func loopback() serverkit.ListenerConfig { return serverkit.ListenerConfig{Addr: "127.0.0.1:0"} }

// startListener binds l and runs it until the test ends.
func startListener(t *testing.T, l *serverkit.Listener) (base string, done <-chan error) {
	t.Helper()
	if err := l.Listen(context.Background()); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		errCh <- l.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-finished
	})
	return "http://" + l.Addr().String(), errCh
}

// A listener refuses to start when it mounts a service declared for another
// audience — in either direction — so a daemon credential can never reach a
// user RPC, even through a hand-written route set.
func TestNewListener_AudienceCheck(t *testing.T) {
	registerAudienceServices(t)
	cases := map[string]struct {
		audience string
		services []string
		want     string // "" = accepted
	}{
		"user chain, user service":     {serverkit.AudienceUser, []string{fixtureUserService}, ""},
		"daemon chain, daemon service": {audienceDaemon, []string{fixtureDaemonService}, ""},
		"daemon chain, user service":   {audienceDaemon, []string{fixtureDaemonService, fixtureUserService}, fixtureUserService + ` (audience "user")`},
		"user chain, daemon service":   {serverkit.AudienceUser, []string{fixtureDaemonService}, fixtureDaemonService + ` (audience "daemon")`},
		"unregistered service":         {serverkit.AudienceUser, []string{"serverkit.composition.v1.Nowhere"}, "not in the proto registry"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			l, err := serverkit.NewListener(serverkit.ListenerDeps{
				Config: loopback(),
				Chain:  chainFor(t, tc.audience, "t"),
				Routes: pingRoutes(tc.services...),
			})
			if tc.want == "" {
				if err != nil {
					t.Fatalf("NewListener: %v", err)
				}
				if l.Audience() != tc.audience {
					t.Fatalf("Audience = %q, want %q", l.Audience(), tc.audience)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NewListener err = %v, want one naming %q", err, tc.want)
			}
		})
	}
}

func TestNewListener_RequiredDeps(t *testing.T) {
	chain := chainFor(t, serverkit.AudienceUser, "t")
	for name, tc := range map[string]struct {
		deps serverkit.ListenerDeps
		want string
	}{
		"no chain":       {serverkit.ListenerDeps{Config: loopback(), Routes: pingRoutes()}, "Chain is required"},
		"no routes":      {serverkit.ListenerDeps{Config: loopback(), Chain: chain}, "Routes is required"},
		"no addr":        {serverkit.ListenerDeps{Chain: chain, Routes: pingRoutes()}, "Addr is required"},
		"front claims /": {serverkit.ListenerDeps{Config: loopback(), Chain: chain, Routes: pingRoutes(), Front: map[string]http.Handler{"/": http.NotFoundHandler()}}, `may not claim "/"`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := serverkit.NewListener(tc.deps); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NewListener err = %v, want %q", err, tc.want)
			}
		})
	}
}

// Two listeners in one process: each authenticates its own audience through
// its own chain, serves only its own routes, and drains on its own — draining
// the daemon listener leaves the user listener serving.
func TestListener_TwoListenersOneProcess(t *testing.T) {
	registerAudienceServices(t)
	user, err := serverkit.NewListener(serverkit.ListenerDeps{
		Config: loopback(),
		Chain:  chainFor(t, serverkit.AudienceUser, "user-token"),
		Routes: pingRoutes(fixtureUserService),
		Front: map[string]http.Handler{"/healthz": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})},
	})
	if err != nil {
		t.Fatalf("user listener: %v", err)
	}
	daemon, err := serverkit.NewListener(serverkit.ListenerDeps{
		Config: loopback(),
		Chain:  chainFor(t, audienceDaemon, "daemon-token"),
		Routes: pingRoutes(fixtureDaemonService),
	})
	if err != nil {
		t.Fatalf("daemon listener: %v", err)
	}
	if user.Ready(context.Background()) == nil {
		t.Fatal("an unbound listener reports ready")
	}
	userBase, _ := startListener(t, user)
	daemonBase, daemonDone := startListener(t, daemon)
	if user.Streams() == daemon.Streams() {
		t.Fatal("two listeners share one Streams registry")
	}

	if err := ping(userBase, fixtureUserService, "user-token"); err != nil {
		t.Fatalf("user Ping on the user listener: %v", err)
	}
	if err := ping(daemonBase, fixtureDaemonService, "daemon-token"); err != nil {
		t.Fatalf("daemon Ping on the daemon listener: %v", err)
	}
	if err := ping(daemonBase, fixtureDaemonService, "user-token"); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("a user credential on the daemon listener: %v, want Unauthenticated", err)
	}
	if err := ping(daemonBase, fixtureUserService, "daemon-token"); connect.CodeOf(err) != connect.CodeNotFound && connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("the user service on the daemon listener: %v, want it absent", err)
	}
	// Front routes bypass the chain: no credential needed.
	resp, err := http.Get(userBase + "/healthz") //nolint:noctx // test probe
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("front /healthz: %v %v", resp, err)
	}
	_ = resp.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := daemon.Drain(ctx); err != nil {
		t.Fatalf("daemon Drain: %v", err)
	}
	select {
	case err := <-daemonDone:
		if err != nil {
			t.Fatalf("daemon Run after Drain: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon Run did not return after Drain")
	}
	if daemon.Ready(ctx) == nil {
		t.Fatal("a drained listener reports ready")
	}
	if err := user.Ready(ctx); err != nil {
		t.Fatalf("user listener after the daemon drained: %v", err)
	}
	if err := ping(userBase, fixtureUserService, "user-token"); err != nil {
		t.Fatalf("user Ping after the daemon drained: %v", err)
	}
	if err := daemon.Close(ctx); err != nil {
		t.Fatalf("Close after Drain: %v", err)
	}
}

// Drain ends this listener's long-lived streams soft-then-hard; a stream on
// another listener is untouched.
func TestListener_DrainEndsOnlyItsOwnStreams(t *testing.T) {
	chain := chainFor(t, serverkit.AudienceUser, "t")
	ended := make(chan string, 2)
	started := make(chan struct{}, 2)
	stream := func(name string) serverkit.RoutesFunc {
		return func(mux *http.ServeMux, _ ...connect.HandlerOption) []string {
			mux.Handle("/watch", serverkit.LongLived(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				http.NewResponseController(w).Flush() //nolint:errcheck // test
				started <- struct{}{}
				<-r.Context().Done()
				if serverkit.Ended(r.Context()) {
					ended <- name
				}
			})))
			return nil
		}
	}
	a, err := serverkit.NewListener(serverkit.ListenerDeps{Config: loopback(), Chain: chain, Routes: stream("a")})
	if err != nil {
		t.Fatal(err)
	}
	b, err := serverkit.NewListener(serverkit.ListenerDeps{Config: loopback(), Chain: chain, Routes: stream("b")})
	if err != nil {
		t.Fatal(err)
	}
	aBase, _ := startListener(t, a)
	bBase, _ := startListener(t, b)
	for _, base := range []string{aBase, bBase} {
		resp, err := http.Get(base + "/watch") //nolint:noctx // held open by the test
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		<-started
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	select {
	case name := <-ended:
		if name != "a" {
			t.Fatalf("drain of listener a ended %q's stream", name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("drain did not end the listener's own stream")
	}
	if n := b.Streams().Active(); n != 1 {
		t.Fatalf("listener b holds %d streams after a drained, want 1", n)
	}
	if r := a.Streams().Report(); r.Ended != 1 {
		t.Fatalf("listener a report = %+v, want one stream ended", r)
	}
}

// Cancelling Run's context without a Drain stops the listener hard: Run
// returns, and the address is released.
func TestListener_RunCtxCancelIsHardStop(t *testing.T) {
	l, err := serverkit.NewListener(serverkit.ListenerDeps{Config: loopback(), Chain: chainFor(t, serverkit.AudienceUser, "t"), Routes: pingRoutes()})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Listen(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return when its context ended")
	}
	if l.Ready(context.Background()) == nil {
		t.Fatal("a stopped listener reports ready")
	}
}

// A second Run is refused rather than serving the same listener twice.
func TestListener_RunTwice(t *testing.T) {
	l, err := serverkit.NewListener(serverkit.ListenerDeps{Config: loopback(), Chain: chainFor(t, serverkit.AudienceUser, "t"), Routes: pingRoutes()})
	if err != nil {
		t.Fatal(err)
	}
	base, _ := startListener(t, l)
	// A response means the first Run is serving.
	resp, err := http.Get(base + "/nothing-here") //nolint:noctx // test probe
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if err := l.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "Run called twice") {
		t.Fatalf("second Run = %v", err)
	}
}

// serverkit.Run over a Server whose Chain admits daemons refuses to serve the
// user service its handler mounted: the boot check covers the generated
// serve pipeline, not only hand-built listeners.
func TestRun_ChainAudienceMismatchFailsBoot(t *testing.T) {
	registerAudienceServices(t)
	srv := serverkit.Server{Mux: http.NewServeMux(), Chain: chainFor(t, audienceDaemon, "t")}
	srv.RecordMounted(pingRoutes(fixtureUserService).Register(srv.Mux, srv.Chain.HandlerOptions()...))
	err := serverkit.Run(context.Background(), baseConfig(freeAddr(t)), srv)
	if err == nil || !strings.Contains(err.Error(), fixtureUserService) {
		t.Fatalf("Run = %v, want the audience check naming %s", err, fixtureUserService)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("Run failed for the wrong reason")
	}
}

// A listener on a DEFAULT chain ends an open server stream when it drains:
// the chain's default stream-drain slot binds it to the listener's Streams,
// so the client gets a clean end with the x-forge-drained trailer instead of
// the stream holding the drain for the whole budget.
func TestListener_DefaultChainEndsStreamsOnDrain(t *testing.T) {
	const procedure = "/serverkit.composition.v1.Watch/Watch"
	inHandler := make(chan struct{})
	routes := serverkit.RoutesFunc(func(mux *http.ServeMux, opts ...connect.HandlerOption) []string {
		mux.Handle(procedure, connect.NewServerStreamHandler(procedure,
			func(ctx context.Context, _ *connect.Request[emptypb.Empty], stream *connect.ServerStream[wrapperspb.StringValue]) error {
				if err := stream.Send(wrapperspb.String("first")); err != nil {
					return err
				}
				close(inHandler)
				<-ctx.Done()
				return ctx.Err()
			}, opts...))
		return nil
	})
	l, err := serverkit.NewListener(serverkit.ListenerDeps{Config: loopback(), Chain: chainFor(t, serverkit.AudienceUser, "t"), Routes: routes})
	if err != nil {
		t.Fatal(err)
	}
	base, _ := startListener(t, l)

	client := connect.NewClient[emptypb.Empty, wrapperspb.StringValue](http.DefaultClient, base+procedure)
	req := connect.NewRequest(&emptypb.Empty{})
	req.Header().Set("Authorization", "Bearer t")
	stream, err := client.CallServerStream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close() //nolint:errcheck // test
	if !stream.Receive() {
		t.Fatalf("first message: %v", stream.Err())
	}
	<-inHandler

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	drained := make(chan error, 1)
	go func() { drained <- l.Drain(ctx) }()
	if stream.Receive() {
		t.Fatalf("unexpected second message %q", stream.Msg().GetValue())
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("drained stream ended with %v, want a clean end", err)
	}
	if got := stream.ResponseTrailer().Get(serverkit.DrainedTrailer); got != "true" {
		t.Fatalf("trailer %s = %q, want true", serverkit.DrainedTrailer, got)
	}
	if err := <-drained; err != nil {
		t.Fatalf("Drain: %v", err)
	}
}
