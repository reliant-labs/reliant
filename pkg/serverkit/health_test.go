package serverkit_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/reliant-labs/forge/pkg/authn"
	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/serverkit"
)

// passAuth admits every request: the health rpc's own auth exemption is the
// project's (middleware UnauthenticatedProcedures), not serverkit's.
type passAuth struct{}

func (passAuth) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc { return next }
func (passAuth) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}
func (passAuth) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func passAuthenticator() serverkit.Authenticator {
	return serverkit.AuthWithCaller(serverkit.AudienceUser, passAuth{}, func(context.Context) (*authn.Claims, bool) { return nil, false })
}

// A listener serves grpc.health.v1.Health/Check through its chain, answering
// the process's readiness. A call carrying env up's probe header is traced as
// a server span marked forge.probe=true and records no rpc.server.* metric; a
// monitor's call (no header) is real traffic, unmarked and counted.
func TestListenerServesTheHealthRPC(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	trace, err := otelconnect.NewInterceptor(otelconnect.WithTracerProvider(tp), otelconnect.WithoutMetrics())
	if err != nil {
		t.Fatal(err)
	}
	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("t")
	chain, err := serverkit.NewChain(serverkit.ChainDeps{
		Auth:  passAuthenticator(),
		Trace: trace,
		Meter: meter,
	})
	if err != nil {
		t.Fatal(err)
	}
	var ready atomic.Bool
	ready.Store(true)
	l, err := serverkit.NewListener(serverkit.ListenerDeps{
		Config: serverkit.ListenerConfig{Addr: "127.0.0.1:0"},
		Chain:  chain,
		Routes: serverkit.RoutesFunc(func(*http.ServeMux, ...connect.HandlerOption) []string { return nil }),
		// /readyz on the Front: the health rpc asks it.
		Front: map[string]http.Handler{serverkit.DefaultReadinessPath: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if !ready.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := l.Listen(ctx); err != nil {
		t.Fatal(err)
	}
	go func() { _ = l.Run(ctx) }()
	t.Cleanup(func() { _ = l.Close(context.Background()) })

	client := connect.NewClient[healthpb.HealthCheckRequest, healthpb.HealthCheckResponse](
		http.DefaultClient, "http://"+l.Addr().String()+serverkit.HealthCheckProcedure, connect.WithProtoJSON())
	check := func(service string, probe bool) (healthpb.HealthCheckResponse_ServingStatus, error) {
		req := connect.NewRequest(&healthpb.HealthCheckRequest{Service: service})
		if probe {
			req.Header().Set(observe.HeaderHealthProbe, "env-up")
		}
		resp, err := client.CallUnary(context.Background(), req)
		if err != nil {
			return 0, err
		}
		return resp.Msg.GetStatus(), nil
	}

	if st, err := check("", true); err != nil || st != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("ready process: %v %v, want SERVING", st, err)
	}
	if _, err := check("acme.v1.Shop", true); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("a named service: %v, want NotFound (serverkit claims no per-service health)", err)
	}
	ready.Store(false)
	if st, err := check("", false); err != nil || st != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Errorf("not ready: %v %v, want NOT_SERVING", st, err)
	}

	var marked, unmarked int
	for _, s := range spans.Ended() {
		if s.Name() != "grpc.health.v1.Health/Check" {
			continue
		}
		isProbe := false
		for _, kv := range s.Attributes() {
			if kv.Key == attribute.Key(release.ProbeAttr) && kv.Value.Emit() == "true" {
				isProbe = true
			}
		}
		if isProbe {
			marked++
		} else {
			unmarked++
		}
	}
	if marked != 2 || unmarked != 1 {
		t.Errorf("marked %d, unmarked %d; want env up's 2 probes marked and the monitor's call not", marked, unmarked)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	var calls int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if sum, ok := m.Data.(metricdata.Sum[int64]); ok && m.Name == "rpc.server.calls" {
				for _, dp := range sum.DataPoints {
					calls += dp.Value
				}
			}
		}
	}
	if calls != 1 {
		t.Errorf("rpc.server.calls = %d, want 1: the monitor's call counted, the 2 probes not", calls)
	}
}

// serverkit.Run serves the health rpc for a Server mounted on Mux, both when
// Handler is the Mux (the generated default) and when Handler is a swap that
// does not route to it (a REST transcoder serving only the project's
// services). Run hands its listener an already-mounted handler, so this is
// the path `forge env up` probes in a generated app.
func TestRunServesTheHealthRPC(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — sends SIGTERM.
	for _, tc := range []struct {
		name    string
		handler func(mux *http.ServeMux) http.Handler
	}{
		{"handler is the mux", func(*http.ServeMux) http.Handler { return nil }},
		{"handler does not route to the mux", func(*http.ServeMux) http.Handler { return http.NotFoundHandler() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr := freeAddr(t)
			mux := http.NewServeMux()
			errCh, _ := runInBackground(t, baseConfig(addr), serverkit.Server{Mux: mux, Handler: tc.handler(mux)})
			waitReady(t, addr, 2*time.Second)

			client := connect.NewClient[healthpb.HealthCheckRequest, healthpb.HealthCheckResponse](
				http.DefaultClient, "http://"+addr+serverkit.HealthCheckProcedure)
			req := connect.NewRequest(&healthpb.HealthCheckRequest{})
			req.Header().Set(observe.HeaderHealthProbe, "env-up")
			resp, err := client.CallUnary(context.Background(), req)
			if err != nil || resp.Msg.GetStatus() != healthpb.HealthCheckResponse_SERVING {
				t.Errorf("health rpc through Run: %v %v, want SERVING", resp, err)
			}

			http.DefaultTransport.(*http.Transport).CloseIdleConnections()
			if err := shutdownAndWait(t, errCh, 5*time.Second); err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
		})
	}
}

// With no readiness to ask, the health rpc says UNKNOWN rather than SERVING.
func TestHealthRPCWithoutReadinessIsUnknown(t *testing.T) {
	chain, err := serverkit.NewChain(serverkit.ChainDeps{Auth: passAuthenticator()})
	if err != nil {
		t.Fatal(err)
	}
	l, err := serverkit.NewListener(serverkit.ListenerDeps{
		Config: serverkit.ListenerConfig{Addr: "127.0.0.1:0"},
		Chain:  chain,
		Routes: serverkit.RoutesFunc(func(*http.ServeMux, ...connect.HandlerOption) []string { return nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := l.Listen(ctx); err != nil {
		t.Fatal(err)
	}
	go func() { _ = l.Run(ctx) }()
	t.Cleanup(func() { _ = l.Close(context.Background()) })
	client := connect.NewClient[healthpb.HealthCheckRequest, healthpb.HealthCheckResponse](
		http.DefaultClient, "http://"+l.Addr().String()+serverkit.HealthCheckProcedure)
	resp, err := client.CallUnary(context.Background(), connect.NewRequest(&healthpb.HealthCheckRequest{}))
	if err != nil || resp.Msg.GetStatus() != healthpb.HealthCheckResponse_UNKNOWN {
		t.Errorf("got %v %v, want UNKNOWN", resp, err)
	}
}

// A project that mounts its own health service keeps it: serverkit does not
// panic on the duplicate pattern, and the project's handler answers.
func TestListenerKeepsAProjectsOwnHealthService(t *testing.T) {
	chain, err := serverkit.NewChain(serverkit.ChainDeps{Auth: passAuthenticator()})
	if err != nil {
		t.Fatal(err)
	}
	own := errors.New("the project's own")
	l, err := serverkit.NewListener(serverkit.ListenerDeps{
		Config: serverkit.ListenerConfig{Addr: "127.0.0.1:0"},
		Chain:  chain,
		Routes: serverkit.RoutesFunc(func(mux *http.ServeMux, opts ...connect.HandlerOption) []string {
			mux.Handle(serverkit.HealthCheckProcedure, connect.NewUnaryHandler(serverkit.HealthCheckProcedure,
				func(context.Context, *connect.Request[healthpb.HealthCheckRequest]) (*connect.Response[healthpb.HealthCheckResponse], error) {
					return nil, connect.NewError(connect.CodeUnavailable, own)
				}, opts...))
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := l.Listen(ctx); err != nil {
		t.Fatal(err)
	}
	go func() { _ = l.Run(ctx) }()
	t.Cleanup(func() { _ = l.Close(context.Background()) })
	client := connect.NewClient[healthpb.HealthCheckRequest, healthpb.HealthCheckResponse](
		http.DefaultClient, "http://"+l.Addr().String()+serverkit.HealthCheckProcedure)
	if _, err := client.CallUnary(context.Background(), connect.NewRequest(&healthpb.HealthCheckRequest{})); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("got %v, want the project's own handler's Unavailable", err)
	}
}
