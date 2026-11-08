package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	collectorlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

// The workload's resource as the platform renders it: the name in
// OTEL_SERVICE_NAME, the rest in OTEL_RESOURCE_ATTRIBUTES.
func setResourceEnv(t *testing.T) {
	t.Helper()
	t.Setenv("OTEL_SERVICE_NAME", "reliant-api-server")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.environment.name=prod,service.namespace=reliant,service.version=v1.7.58")
}

var wantLineResource = map[string]string{
	"service.name":                "reliant-api-server",
	"service.version":             "v1.7.58",
	"service.namespace":           "reliant",
	"deployment.environment.name": "prod",
	"service.instance.id":         "pod-7d7b89c69d-x2k4q",
}

// lineResources decodes every JSON line in buf and returns each one's
// LogKeyResource object (nil for a line without one).
func lineResources(t *testing.T, buf *bytes.Buffer) []map[string]string {
	t.Helper()
	var out []map[string]string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("not a JSON line: %q: %v", line, err)
		}
		var res map[string]string
		if raw, ok := rec[LogKeyResource]; ok {
			if err := json.Unmarshal(raw, &res); err != nil {
				t.Fatalf("%q is not an object of strings in %q: %v", LogKeyResource, line, err)
			}
		}
		out = append(out, res)
	}
	return out
}

// identity is the LogResourceKeys subset of an OTLP resource.
func identity(r *resourcepb.Resource) map[string]string {
	out := map[string]string{}
	for _, kv := range r.GetAttributes() {
		for _, k := range LogResourceKeys {
			if kv.GetKey() == k {
				out[k] = kv.GetValue().GetStringValue()
			}
		}
	}
	return out
}

// The contract: a log line read from stdout carries the same service.name,
// service.version, service.namespace, deployment.environment.name and
// service.instance.id as the traces, metrics and OTLP logs the same process
// exports — read off the wire, not off resourceFromConfig.
func TestLogLinesCarryTheResourceEverySignalExports(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)
	setResourceEnv(t)
	rcv := newOTLPReceiver(t)

	cfg := Config{
		ServiceName:      "compiled-in-name", // OTEL_SERVICE_NAME wins, on every signal
		ServiceVersion:   "v1.7.58",
		InstanceID:       wantLineResource["service.instance.id"],
		OTLPEndpoint:     rcv.URL,
		OTLPLogs:         true,
		ProfilesDisabled: true,
	}
	shutdown, _, err := Setup(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	logger := slog.New(NewLogHandler(slog.NewJSONHandler(&buf, nil), cfg))
	emitSpan(context.Background())
	counter, err := otel.Meter("observe-test").Int64Counter("observe.test.count")
	if err != nil {
		t.Fatal(err)
	}
	counter.Add(context.Background(), 1)
	logger.Info("served")
	flush(t, shutdown)

	lines := lineResources(t, &buf)
	if len(lines) != 1 || !maps.Equal(lines[0], wantLineResource) {
		t.Fatalf("stdout line resource = %v, want %v", lines, wantLineResource)
	}

	signals := map[string]func([]byte) ([]*resourcepb.Resource, error){
		"/v1/traces": func(b []byte) ([]*resourcepb.Resource, error) {
			var req collectortrace.ExportTraceServiceRequest
			err := proto.Unmarshal(b, &req)
			var out []*resourcepb.Resource
			for _, rs := range req.GetResourceSpans() {
				out = append(out, rs.GetResource())
			}
			return out, err
		},
		"/v1/metrics": func(b []byte) ([]*resourcepb.Resource, error) {
			var req collectormetrics.ExportMetricsServiceRequest
			err := proto.Unmarshal(b, &req)
			var out []*resourcepb.Resource
			for _, rm := range req.GetResourceMetrics() {
				out = append(out, rm.GetResource())
			}
			return out, err
		},
		"/v1/logs": func(b []byte) ([]*resourcepb.Resource, error) {
			var req collectorlogs.ExportLogsServiceRequest
			err := proto.Unmarshal(b, &req)
			var out []*resourcepb.Resource
			for _, rl := range req.GetResourceLogs() {
				out = append(out, rl.GetResource())
			}
			return out, err
		},
	}
	for path, decode := range signals {
		reqs := rcv.requests(path)
		if len(reqs) == 0 {
			t.Errorf("%s: nothing exported", path)
			continue
		}
		for _, q := range reqs {
			resources, err := decode(q.Body)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			for _, r := range resources {
				if got := identity(r); !maps.Equal(got, lines[0]) {
					t.Errorf("%s resource %v differs from the stdout line's %v", path, got, lines[0])
				}
			}
		}
	}
}

// A logger is usually built before Setup (a process logs its own startup).
// Until Setup runs, its lines carry the resource the same Config and
// environment describe; afterwards Setup's own. The group stays at the top
// level of the line whatever the caller added, before or after the switch.
func TestLogResourceBeforeAndAfterSetup(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)
	setResourceEnv(t)
	processResource.Store(nil)

	cfg := Config{ServiceVersion: "v1.7.58", InstanceID: wantLineResource["service.instance.id"]}
	var buf bytes.Buffer
	base := slog.New(NewLogHandler(slog.NewJSONHandler(&buf, nil), cfg))
	scoped := base.With("component", "billing").WithGroup("req")

	base.Info("before setup")
	scoped.Info("before setup, scoped", "id", 7)

	// Setup with a different instance id stands in for "Setup's resource,
	// not the logger's own": the lines must follow Setup.
	setupCfg := cfg
	setupCfg.InstanceID = "after-setup"
	shutdown, _, err := Setup(context.Background(), setupCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	base.Info("after setup")
	scoped.Info("after setup, scoped", "id", 8)

	after := maps.Clone(wantLineResource)
	after["service.instance.id"] = "after-setup"
	want := []map[string]string{wantLineResource, wantLineResource, after, after}
	got := lineResources(t, &buf)
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(got), len(want), buf.String())
	}
	for i := range want {
		if !maps.Equal(got[i], want[i]) {
			t.Errorf("line %d resource = %v, want %v", i, got[i], want[i])
		}
	}
	// The caller's attributes and group survived the switch, after the resource.
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	for _, i := range []int{1, 3} {
		if !strings.Contains(lines[i], `"component":"billing","req":{"id":`) {
			t.Errorf("line %d lost the caller's attrs or group: %s", i, lines[i])
		}
		if strings.Index(lines[i], `"resource":`) > strings.Index(lines[i], `"component":`) {
			t.Errorf("line %d: the resource is not ahead of the caller's attrs: %s", i, lines[i])
		}
	}
}

// Keys with no value are omitted; a resource with none of the keys writes no
// field at all.
func TestLogResourceOmitsUnsetKeys(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)
	processResource.Store(nil)

	var buf bytes.Buffer
	slog.New(NewLogHandler(slog.NewJSONHandler(&buf, nil), Config{ServiceName: "svc", ServiceVersion: "dev"})).Info("x")
	got := lineResources(t, &buf)
	if want := map[string]string{"service.name": "svc"}; len(got) != 1 || !maps.Equal(got[0], want) {
		t.Errorf("resource = %v, want %v (dev is no version; nothing else is set)", got, want)
	}
}

// Text output is for a human at a terminal: no resource on it.
func TestLogResourceLeavesTextLinesAlone(t *testing.T) {
	clearOTelEnv(t)
	setResourceEnv(t)
	var buf bytes.Buffer
	slog.New(NewLogHandler(slog.NewTextHandler(&buf, nil), Config{})).Info("hello")
	if strings.Contains(buf.String(), "resource") {
		t.Errorf("a text line carries the resource: %s", buf.String())
	}
}

// The resource is encoded once per handler, not once per line: a stamped
// handler allocates no more per record than the bare JSON handler does.
func TestLogResourceCostsNoPerLineAllocation(t *testing.T) {
	// Both handlers write through slog's pooled buffers, and the race
	// detector drops sync.Pool entries at random: under -race this failed 16
	// of main's last 40 runs ("1.0 per line, the bare JSON handler 0.0")
	// with no change to either handler, and never without it. The -short
	// tiers (pull request, main's test-short-warm, Windows) run it unraced.
	if raceEnabled {
		t.Skip("allocation counts are not deterministic under the race detector")
	}
	clearOTelEnv(t)
	snapshotGlobals(t)
	setResourceEnv(t)
	processResource.Store(nil)

	bare := slog.NewJSONHandler(discard{}, nil)
	stamped := newResourceHandler(slog.NewJSONHandler(discard{}, nil), Config{InstanceID: "pod"})
	rec := slog.NewRecord(time.Time{}, slog.LevelInfo, "served", 0)
	rec.AddAttrs(slog.String("procedure", "/x.v1.X/Get"))
	ctx := context.Background()
	_ = stamped.Handle(ctx, rec) // the first line builds and caches the stamped handler

	base := testing.AllocsPerRun(200, func() { _ = bare.Handle(ctx, rec) })
	got := testing.AllocsPerRun(200, func() { _ = stamped.Handle(ctx, rec) })
	if got > base {
		t.Errorf("stamped handler allocates %.1f per line, the bare JSON handler %.1f", got, base)
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
