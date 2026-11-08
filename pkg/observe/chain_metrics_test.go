package observe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/forge/pkg/observe/metrics"
)

// TestChainCountsRPCsOnTheGlobalMeterProvider pins that a Chain built with no
// Meter — what serverkit.NewChain builds, and so every forge server — records
// the RPC metrics on the provider observe.Setup installs. It once passed a nil
// meter to MetricsInterceptor, which made it a no-op: rpc.server.calls never
// existed, and every generated dashboard and RPC alert read nothing.
//
// It also pins the names against the declared built-ins, the set the
// generated dashboards are checked against (internal/obsdash), so the
// dashboards, the declaration and what a server emits cannot drift apart.
func TestChainCountsRPCsOnTheGlobalMeterProvider(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
		_ = mp.Shutdown(context.Background())
	})

	const procedure = "/test.v1.PingService/Ping"
	fail := false
	handler := connect.NewUnaryHandler(procedure,
		func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			if fail {
				return nil, connect.NewError(connect.CodeInternal, errors.New("boom"))
			}
			return connect.NewResponse(&emptypb.Empty{}), nil
		},
		connect.WithInterceptors(Chain(Deps{})...),
	)
	mux := http.NewServeMux()
	mux.Handle(procedure, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := connect.NewClient[emptypb.Empty, emptypb.Empty](srv.Client(), srv.URL+procedure)
	if _, err := client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{})); err != nil {
		t.Fatalf("call: %v", err)
	}
	fail = true
	if _, err := client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{})); err == nil {
		t.Fatal("failing call returned no error")
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]metricdata.Aggregation{}
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != chainMeterScope {
			continue
		}
		for _, m := range sm.Metrics {
			got[m.Name] = m.Data
		}
	}
	var names []string
	for n := range got {
		names = append(names, n)
	}
	sort.Strings(names)
	want := []string{"rpc.server.calls", "rpc.server.duration", "rpc.server.errors"}
	if len(names) != len(want) {
		t.Fatalf("metrics recorded under %s = %v, want %v", chainMeterScope, names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("metrics recorded under %s = %v, want %v", chainMeterScope, names, want)
		}
		if !metrics.IsBuiltin(names[i]) {
			t.Errorf("%s is emitted but not declared in metrics.builtinSpecs", names[i])
		}
	}

	calls, ok := got["rpc.server.calls"].(metricdata.Sum[int64])
	if !ok || len(calls.DataPoints) != 1 || calls.DataPoints[0].Value != 2 {
		t.Errorf("rpc.server.calls = %+v, want one series counting 2 calls", got["rpc.server.calls"])
	}
	errs, ok := got["rpc.server.errors"].(metricdata.Sum[int64])
	if !ok || len(errs.DataPoints) != 1 || errs.DataPoints[0].Value != 1 {
		t.Fatalf("rpc.server.errors = %+v, want one series counting 1 error", got["rpc.server.errors"])
	}
	if v, _ := errs.DataPoints[0].Attributes.Value(ErrorClassKey); v.AsString() != "server" {
		t.Errorf("rpc.server.errors %s = %q, want server", ErrorClassKey, v.AsString())
	}
}
