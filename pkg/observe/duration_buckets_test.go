package observe

import (
	"context"
	"slices"
	"testing"

	"connectrpc.com/connect"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/reliant-labs/forge/pkg/observe/metrics"
)

// rpc.server.duration is recorded in seconds. With the SDK's default
// boundaries (sized for milliseconds: 0, 5, 10, 25 ... 10000) every request
// from 5ms to 5s landed in one bucket, so each quantile read off it was a
// fraction of 5: hosted dashboards showed p95 = 4.75s for a service whose real
// p95 was 2.1s. The seconds histograms forge declares must carry seconds
// boundaries, so a quantile lands in a bucket that is a fraction of a second
// wide.
func TestRPCServerDurationHasSecondsBuckets(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	ic := MetricsInterceptor(mp.Meter("test"))
	unary := ic.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, nil
	})
	if _, err := unary(context.Background(), connect.NewRequest(&struct{}{})); err != nil {
		t.Fatal(err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	var h *metricdata.Histogram[float64]
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "rpc.server.duration" {
				d, ok := m.Data.(metricdata.Histogram[float64])
				if !ok {
					t.Fatalf("rpc.server.duration is %T, want a float64 histogram", m.Data)
				}
				h = &d
			}
		}
	}
	if h == nil || len(h.DataPoints) != 1 {
		t.Fatalf("rpc.server.duration not recorded: %+v", h)
	}
	dp := h.DataPoints[0]
	if !slices.Equal(dp.Bounds, metrics.SecondsBuckets()) {
		t.Fatalf("bounds = %v, want metrics.SecondsBuckets %v", dp.Bounds, metrics.SecondsBuckets())
	}
	// Where a value lands is pinned with a recorded value, not a timed call:
	// metrics.TestSecondsHistogramsGetSecondsBuckets.
}

// Every built-in seconds histogram gets the seconds boundaries; one that
// names its own keeps them; other units keep the SDK default.
func TestSecondsHistogramsDefaultToSecondsBuckets(t *testing.T) {
	for _, name := range []string{"rpc.server.duration", "forge.server.drain.duration"} {
		s, ok := metrics.Default().Lookup(name)
		if !ok {
			t.Fatalf("%s is not declared", name)
		}
		if !slices.Equal(s.Buckets, metrics.SecondsBuckets()) {
			t.Errorf("%s buckets = %v, want SecondsBuckets", name, s.Buckets)
		}
	}
	if s, _ := metrics.Default().Lookup("go.sql.query_timing"); len(s.Buckets) != 0 {
		t.Errorf("go.sql.query_timing (ms) got buckets %v; only seconds histograms default", s.Buckets)
	}
}
