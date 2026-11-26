package observe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/resmetrics"
)

func clearResourceEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{EnvResourceMetrics, EnvResourceInterval, envKubernetesServiceHost} {
		t.Setenv(k, "")
	}
}

func TestResolveResourceMetrics(t *testing.T) {
	for _, tc := range []struct {
		name               string
		env, interval, pod string
		cfg                Config
		wantMode           ResourceMetricsMode
		wantGroups         resmetrics.Group
		wantScope          resmetrics.Scope
		wantInterval       time.Duration
		wantWarns          int
	}{
		{name: "default", wantMode: ResourceMetricsAuto, wantGroups: resmetrics.GroupAll, wantScope: resmetrics.ScopeHost, wantInterval: 15 * time.Second},
		{name: "auto in a pod reports the cgroup", pod: "10.0.0.1", wantMode: ResourceMetricsAuto, wantGroups: resmetrics.GroupAll, wantScope: resmetrics.ScopeCgroup, wantInterval: 15 * time.Second},
		{name: "all in a pod reports the node", env: "all", pod: "10.0.0.1", wantMode: ResourceMetricsAll, wantGroups: resmetrics.GroupAll, wantScope: resmetrics.ScopeHost, wantInterval: 15 * time.Second},
		{name: "process", env: "process", wantMode: ResourceMetricsProcess, wantGroups: resmetrics.GroupProcess, wantScope: resmetrics.ScopeHost, wantInterval: 15 * time.Second},
		{name: "host in a pod is narrowed", env: "HOST", pod: "x", wantMode: ResourceMetricsHost, wantGroups: resmetrics.GroupHost, wantScope: resmetrics.ScopeCgroup, wantInterval: 15 * time.Second},
		{name: "off", env: "off", wantMode: ResourceMetricsOff, wantGroups: resmetrics.GroupAll, wantScope: resmetrics.ScopeHost, wantInterval: 15 * time.Second},
		{name: "typed config", cfg: Config{ResourceMetrics: ResourceMetricsProcess, ResourceMetricsInterval: time.Minute}, wantMode: ResourceMetricsProcess, wantGroups: resmetrics.GroupProcess, wantScope: resmetrics.ScopeHost, wantInterval: time.Minute},
		{name: "the environment wins", env: "off", interval: "5s", cfg: Config{ResourceMetrics: ResourceMetricsAll, ResourceMetricsInterval: time.Minute}, wantMode: ResourceMetricsOff, wantGroups: resmetrics.GroupAll, wantScope: resmetrics.ScopeHost, wantInterval: 5 * time.Second},
		{name: "a bad environment value is ignored", env: "sometimes", interval: "fast", cfg: Config{ResourceMetrics: ResourceMetricsProcess}, wantMode: ResourceMetricsProcess, wantGroups: resmetrics.GroupProcess, wantScope: resmetrics.ScopeHost, wantInterval: 15 * time.Second, wantWarns: 2},
		{name: "below the floor", interval: "100ms", cfg: Config{ResourceMetricsInterval: time.Millisecond}, wantMode: ResourceMetricsAuto, wantGroups: resmetrics.GroupAll, wantScope: resmetrics.ScopeHost, wantInterval: time.Second, wantWarns: 2},
		{name: "a bad typed value", cfg: Config{ResourceMetrics: "most"}, wantMode: ResourceMetricsAuto, wantGroups: resmetrics.GroupAll, wantScope: resmetrics.ScopeHost, wantInterval: 15 * time.Second, wantWarns: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvResourceMetrics, tc.env)
			t.Setenv(EnvResourceInterval, tc.interval)
			t.Setenv(envKubernetesServiceHost, tc.pod)
			got, warns := resolveResourceMetrics(tc.cfg)
			if got.mode != tc.wantMode || got.opts.Groups != tc.wantGroups || got.opts.Scope != tc.wantScope || got.opts.Interval != tc.wantInterval {
				t.Errorf("got mode %s groups %d scope %s interval %s; want %s %d %s %s",
					got.mode, got.opts.Groups, got.opts.Scope, got.opts.Interval, tc.wantMode, tc.wantGroups, tc.wantScope, tc.wantInterval)
			}
			if len(warns) != tc.wantWarns {
				t.Errorf("warnings = %v, want %d", warns, tc.wantWarns)
			}
		})
	}
}

func scrape(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		// A name collision with the default registry's collectors fails the
		// whole gather.
		t.Fatalf("/metrics = %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// Without an OTLP endpoint the Prometheus reader is the only surface, and the
// resource metrics must be on it.
func TestResourceMetricsOnMetricsWithoutExport(t *testing.T) {
	clearOTelEnv(t)
	clearResourceEnv(t)
	snapshotGlobals(t)
	shutdown, handler, err := Setup(context.Background(), Config{ServiceName: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	defer flush(t, shutdown)
	body := scrape(t, handler)
	for _, want := range []string{"process_uptime_seconds", "system_cpu_logical_count", `forge_resource_scope{`} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics lacks %s", want)
		}
	}
	s := Resources()
	if s == nil {
		t.Fatal("Resources() is nil after Setup started the sampler")
	}
	if snap := s.Latest(); snap.Process == nil || snap.Host == nil || s.Groups() != resmetrics.GroupAll {
		t.Errorf("Resources() = groups %v, snapshot %+v; want both groups", s.Groups(), snap)
	}
}

func TestResourceMetricsOff(t *testing.T) {
	clearOTelEnv(t)
	clearResourceEnv(t)
	snapshotGlobals(t)
	t.Setenv(EnvResourceMetrics, "off")
	shutdown, handler, err := Setup(context.Background(), Config{ServiceName: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	defer flush(t, shutdown)
	if body := scrape(t, handler); strings.Contains(body, "process_uptime_seconds") || strings.Contains(body, "forge_resource_scope") {
		t.Error("FORGE_OBSERVE_RESOURCE_METRICS=off still registered resource metrics")
	}
	if Resources() != nil {
		t.Error("Resources() kept a sampler with resource metrics off")
	}
}

// With export on, the resource metrics reach OTLP and /metrics alike.
func TestResourceMetricsAreExported(t *testing.T) {
	clearOTelEnv(t)
	clearResourceEnv(t)
	snapshotGlobals(t)
	rcv := newOTLPReceiver(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", rcv.URL)
	t.Setenv(EnvResourceMetrics, "process")

	shutdown, handler, err := Setup(context.Background(), Config{ServiceName: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	if body := scrape(t, handler); !strings.Contains(body, "process_uptime_seconds") || strings.Contains(body, "system_cpu_logical_count") {
		t.Error("/metrics does not carry exactly the process group")
	}
	flush(t, shutdown)
	var body strings.Builder
	for _, r := range rcv.requests("/v1/metrics") {
		body.Write(r.Body)
	}
	if !strings.Contains(body.String(), "process.uptime") {
		t.Error("process.uptime missing from the OTLP export")
	}
}
