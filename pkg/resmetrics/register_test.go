package resmetrics

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/reliant-labs/forge/pkg/observe/metrics"
)

// resourceMetricNames is every metric this package emits, pinned. The names
// follow OpenTelemetry semantic conventions that are still in development, so
// a rename upstream must be a deliberate edit here, in pkg/observe/metrics and
// in every dashboard that reads the old name, never a silent drift.
var resourceMetricNames = []string{
	"forge.cgroup.cpu.throttled_periods",
	"forge.cgroup.cpu.throttled_time",
	"forge.cgroup.memory.limit",
	"forge.cgroup.memory.oom_kills",
	"forge.cgroup.memory.working_set",
	"forge.cgroup.pids.count",
	"forge.cgroup.pids.limit",
	"forge.cgroup.pressure.avg10",
	"forge.cgroup.pressure.stall_time",
	"forge.process.file_descriptor.limit",
	"forge.process.file_descriptor.utilization",
	"forge.process.handle.count",
	"forge.process.memory.go_gap",
	"forge.resource.scope",
	"forge.system.file_descriptor.count",
	"forge.system.file_descriptor.limit",
	"forge.system.process.count",
	"forge.system.process.limit",
	"process.context_switches",
	"process.cpu.time",
	"process.cpu.utilization",
	"process.disk.io",
	"process.memory.usage",
	"process.memory.virtual",
	"process.paging.faults",
	"process.thread.count",
	"process.unix.file_descriptor.count",
	"process.uptime",
	"system.cpu.load_average.15m",
	"system.cpu.load_average.1m",
	"system.cpu.load_average.5m",
	"system.cpu.logical.count",
	"system.cpu.time",
	"system.cpu.utilization",
	"system.filesystem.usage",
	"system.filesystem.utilization",
	"system.linux.memory.available",
	"system.memory.limit",
	"system.memory.usage",
	"system.paging.usage",
}

var resourcePrefixes = []string{"process.", "system.", "forge.process.", "forge.system.", "forge.cgroup.", "forge.resource."}

func isResourceMetric(name string) bool {
	for _, p := range resourcePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// collect registers s on a fresh provider and returns one collection.
func collect(t *testing.T, s *Sampler) []metricdata.Metrics {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	if _, err := Register(mp.Meter("test"), s); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	var out []metricdata.Metrics
	for _, sm := range rm.ScopeMetrics {
		out = append(out, sm.Metrics...)
	}
	return out
}

// fullCollection is a collection in which every metric has a value: a
// complete fixture, a memory.max and pids.max to report, and a second sample
// so the utilizations exist.
func fullCollection(t *testing.T) []metricdata.Metrics {
	t.Helper()
	f := newFixture(t)
	f.writeCgroup(map[string]string{"pids.max": "100\n"})
	s := f.start(Options{})
	f.writeProc(map[string]string{"stat": "cpu  1600 100 400 10000 50 20 30 10 0 0\nbtime 1700000000\n"})
	f.advance(time.Minute)
	return collect(t, s)
}

// TestMetricNamesArePinned fails on drift in any direction: a metric emitted
// but not pinned, pinned but not emitted, or not declared in
// pkg/observe/metrics with the instrument kind it is emitted as.
func TestMetricNamesArePinned(t *testing.T) {
	got := map[string]metricdata.Metrics{}
	for _, m := range fullCollection(t) {
		got[m.Name] = m
	}
	// The handle count is windows's, read natively; the fixture is a /proc
	// tree (TestNativeOnlyFields covers it).
	want := slices.DeleteFunc(slices.Clone(resourceMetricNames), func(n string) bool { return n == "forge.process.handle.count" })
	if !volumesSupported {
		want = slices.DeleteFunc(want, func(n string) bool { return strings.HasPrefix(n, "system.filesystem.") })
	}
	for _, name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("%s is pinned but was not emitted", name)
		}
	}
	for name, m := range got {
		if !slices.Contains(resourceMetricNames, name) {
			t.Errorf("%s was emitted but is not pinned in resourceMetricNames", name)
			continue
		}
		if !metrics.IsBuiltin(name) {
			t.Errorf("%s is not declared in pkg/observe/metrics", name)
			continue
		}
		spec := metrics.BuiltinSpec(name)
		if kind := kindOf(m.Data); kind != spec.Type {
			t.Errorf("%s is emitted as a %s, declared a %s", name, kind, spec.Type)
		}
		if m.Unit != spec.Unit {
			t.Errorf("%s is emitted in %q, declared %q", name, m.Unit, spec.Unit)
		}
	}

	var declared []string
	for _, s := range metrics.Default().Declared() {
		if isResourceMetric(s.Name) {
			declared = append(declared, s.Name)
		}
	}
	slices.Sort(declared)
	if !slices.Equal(declared, resourceMetricNames) {
		t.Errorf("the resource metrics declared in pkg/observe/metrics differ from the pinned list:\ndeclared %v\npinned   %v", declared, resourceMetricNames)
	}
}

func kindOf(data metricdata.Aggregation) metrics.Type {
	switch d := data.(type) {
	case metricdata.Sum[int64]:
		return sumKind(d.IsMonotonic)
	case metricdata.Sum[float64]:
		return sumKind(d.IsMonotonic)
	case metricdata.Gauge[int64], metricdata.Gauge[float64]:
		return metrics.Gauge
	}
	return "unknown"
}

func sumKind(monotonic bool) metrics.Type {
	if monotonic {
		return metrics.Counter
	}
	return metrics.UpDownCounter
}

// forbiddenAttribute names what may never be on a resource datapoint: what
// identifies a person, a command or a machine. service.instance.id, on the
// resource, is the only per-instance key.
func forbiddenAttribute(key string) bool {
	for _, p := range []string{"process.command", "process.executable", "process.owner", "process.pid", "process.parent_pid", "process.user", "user.", "host.name", "host.id", "host.ip", "enduser."} {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return strings.Contains(key, "path") || strings.Contains(key, "mountpoint") || strings.Contains(key, "cmdline")
}

// TestAttributesAreDeclaredAndPrivate holds every datapoint to its declaration:
// each attribute key is declared, each value is in the key's closed set, and
// no key names a command, an executable, a user, a pid, a host or a path.
func TestAttributesAreDeclaredAndPrivate(t *testing.T) {
	for _, s := range metrics.Default().Declared() {
		if !isResourceMetric(s.Name) {
			continue
		}
		for _, a := range s.Attributes {
			if forbiddenAttribute(a.Key) {
				t.Errorf("%s declares the identifying attribute %q", s.Name, a.Key)
			}
			if len(a.Values) == 0 {
				t.Errorf("%s declares %q with no closed value set: every resource attribute must be bounded", s.Name, a.Key)
			}
		}
	}

	points := 0
	for _, m := range fullCollection(t) {
		spec := metrics.BuiltinSpec(m.Name)
		for _, set := range attributeSets(m.Data) {
			points++
			for _, kv := range set.ToSlice() {
				key, val := string(kv.Key), kv.Value.Emit()
				if forbiddenAttribute(key) {
					t.Errorf("%s carries the identifying attribute %s=%q", m.Name, key, val)
				}
				i := slices.IndexFunc(spec.Attributes, func(a metrics.Attribute) bool { return a.Key == key })
				if i < 0 {
					t.Errorf("%s carries %s, which its declaration does not list", m.Name, key)
					continue
				}
				if !slices.Contains(spec.Attributes[i].Values, val) {
					t.Errorf("%s carries %s=%q, outside the declared set %v", m.Name, key, val, spec.Attributes[i].Values)
				}
			}
		}
	}
	if points == 0 {
		t.Fatal("the collection had no datapoints; this test would pass vacuously")
	}
}

func attributeSets(data metricdata.Aggregation) []attribute.Set {
	var out []attribute.Set
	switch d := data.(type) {
	case metricdata.Sum[int64]:
		for _, p := range d.DataPoints {
			out = append(out, p.Attributes)
		}
	case metricdata.Sum[float64]:
		for _, p := range d.DataPoints {
			out = append(out, p.Attributes)
		}
	case metricdata.Gauge[int64]:
		for _, p := range d.DataPoints {
			out = append(out, p.Attributes)
		}
	case metricdata.Gauge[float64]:
		for _, p := range d.DataPoints {
			out = append(out, p.Attributes)
		}
	}
	return out
}

// A group that is off registers no instruments, and a failed reader's metric
// is absent from the collection rather than reported as zero.
func TestRegisterFollowsGroupsAndFailedReaders(t *testing.T) {
	f := newFixture(t)
	f.removeProc("self/io")
	names := func(ms []metricdata.Metrics) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.Name)
		}
		return out
	}

	got := names(collect(t, f.start(Options{Groups: GroupProcess})))
	if slices.Contains(got, "process.disk.io") {
		t.Error("process.disk.io was reported with /proc/self/io unreadable")
	}
	if !slices.Contains(got, "process.cpu.time") {
		t.Error("a failed /proc/self/io took process.cpu.time with it")
	}
	for _, n := range got {
		if !strings.HasPrefix(n, "process.") && !strings.HasPrefix(n, "forge.process.") {
			t.Errorf("GroupProcess emitted %s", n)
		}
	}

	got = names(collect(t, f.start(Options{Groups: GroupHost, Scope: ScopeCgroup})))
	for _, n := range got {
		if strings.HasPrefix(n, "process.") || n == "system.cpu.time" || n == "system.memory.usage" || strings.HasPrefix(n, "system.cpu.load_average") {
			t.Errorf("GroupHost in cgroup scope emitted %s", n)
		}
	}
	if !slices.Contains(got, "forge.cgroup.memory.working_set") || !slices.Contains(got, "forge.resource.scope") {
		t.Errorf("cgroup scope lost the container's metrics: %v", got)
	}
}

// The fields only a native reader sets: windows's handle count, and a host
// CPU without iowait or steal (darwin, windows), whose two unknown modes are
// left out rather than reported as zero.
func TestNativeOnlyFields(t *testing.T) {
	f := newFixture(t)
	s := f.start(Options{})
	f.writeProc(map[string]string{"stat": "cpu  1600 100 400 10000 50 20 30 10 0 0\nbtime 1700000000\n"})
	f.advance(time.Minute)
	snap := s.Latest() // a second sample, so the utilizations exist
	handles := int64(42)
	snap.Process.Handles = &handles
	snap.Host.CPU.NoIOWaitSteal = true

	got := map[string]metricdata.Metrics{}
	for _, m := range collect(t, s) { // the cached sample: the clock has not moved
		got[m.Name] = m
	}
	if m, ok := got["forge.process.handle.count"]; !ok {
		t.Error("forge.process.handle.count was not emitted from Handles")
	} else if d := m.Data.(metricdata.Sum[int64]); len(d.DataPoints) != 1 || d.DataPoints[0].Value != 42 {
		t.Errorf("forge.process.handle.count = %+v, want 42", d.DataPoints)
	}
	for _, name := range []string{"system.cpu.time", "system.cpu.utilization"} {
		var modes []string
		for _, set := range attributeSets(got[name].Data) {
			v, _ := set.Value("cpu.mode")
			modes = append(modes, v.AsString())
		}
		slices.Sort(modes)
		if want := []string{"idle", "system", "user"}; !slices.Equal(modes, want) {
			t.Errorf("%s modes = %v, want %v: iowait and steal are unknown here", name, modes, want)
		}
	}
}
