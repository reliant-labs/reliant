package serverkit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// TestNewChain_EmitsRPCMetricsWithNoMeterWired pins the RPC RED metrics on the
// chain a scaffolded server builds (cmd-tree-serve.go.tmpl calls
// serverkit.NewChain and names no Meter). Before forge v1.0.0 that chain gave
// observe's MetricsInterceptor a nil meter, so no forge app ever stored
// rpc.server.calls / .errors / .duration and the generated overview, the RPC
// alerts and `forge obs check` read nothing (control-plane prod, 2026-10-29).
// observe's own TestChainCountsRPCsOnTheGlobalMeterProvider covers
// observe.Chain; this covers the serverkit layer above it, so a future
// ChainDeps that starts passing an explicit (nil) Meter cannot regress it.
func TestNewChain_EmitsRPCMetricsWithNoMeterWired(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
		_ = mp.Shutdown(context.Background())
	})

	chain, err := serverkit.NewChain(serverkit.ChainDeps{
		Auth:  serverkit.AuthFor(serverkit.AudienceUser, tokenAuth{token: "t"}),
		Trace: serverkit.NoTrace,
	})
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	const procedure = "/test.v1.PingService/Ping"
	mux := http.NewServeMux()
	mux.Handle(procedure, connect.NewUnaryHandler(procedure,
		func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			return connect.NewResponse(&emptypb.Empty{}), nil
		},
		chain.HandlerOptions()...,
	))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := connect.NewClient[emptypb.Empty, emptypb.Empty](srv.Client(), srv.URL+procedure)
	ok := connect.NewRequest(&emptypb.Empty{})
	ok.Header().Set("Authorization", "Bearer t")
	if _, err := client.CallUnary(context.Background(), ok); err != nil {
		t.Fatalf("authenticated call: %v", err)
	}
	// Unauthenticated: auth sits inside the metrics layer, so it is counted as an error.
	if _, err := client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated call = %v, want unauthenticated", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	seen := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			seen[m.Name] = true
		}
	}
	for _, name := range []string{"rpc.server.calls", "rpc.server.errors", "rpc.server.duration"} {
		if !seen[name] {
			t.Errorf("a scaffolded server's chain recorded no %s (recorded: %v)", name, seen)
		}
	}
}
