package serverkit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/serverkit"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestChainExplicitMeterProviderAndScopeCountOnce(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = provider.Shutdown(context.Background()) }()
	chain, err := serverkit.NewChain(serverkit.ChainDeps{
		Auth: serverkit.AuthFor(serverkit.AudienceUser, tokenAuth{token: "t"}), Trace: serverkit.NoTrace,
		Meter: provider.Meter("owned.rpc.scope"),
	})
	if err != nil {
		t.Fatal(err)
	}
	const procedure = "/test.v1.PingService/Ping"
	mux := http.NewServeMux()
	mux.Handle(procedure, connect.NewUnaryHandler(procedure, func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
		return connect.NewResponse(&emptypb.Empty{}), nil
	}, chain.HandlerOptions()...))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := connect.NewClient[emptypb.Empty, emptypb.Empty](srv.Client(), srv.URL+procedure)
	req := connect.NewRequest(&emptypb.Empty{})
	req.Header().Set("Authorization", "Bearer t")
	if _, err := client.CallUnary(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CallUnary(t.Context(), connect.NewRequest(&emptypb.Empty{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatal(err)
	}
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatal(err)
	}
	var calls, failures int64
	for _, scope := range metrics.ScopeMetrics {
		if scope.Scope.Name != "owned.rpc.scope" {
			t.Errorf("wrong scope %q", scope.Scope.Name)
		}
		for _, m := range scope.Metrics {
			if sum, ok := m.Data.(metricdata.Sum[int64]); ok {
				for _, p := range sum.DataPoints {
					switch m.Name {
					case "rpc.server.calls":
						calls += p.Value
					case "rpc.server.errors":
						failures += p.Value
					}
				}
			}
		}
	}
	if calls != 2 || failures != 1 {
		t.Fatalf("duplicate/missing metrics: calls=%d errors=%d", calls, failures)
	}
}
