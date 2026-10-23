package observe

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/pprof/profile"
	"go.opentelemetry.io/collector/pdata/pprofile"
	"go.opentelemetry.io/collector/pdata/pprofile/pprofileotlp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	testTraceHex = "0102030405060708090a0b0c0d0e0f10"
	testSpanHex  = "1112131415161718"
)

// goCPUProfile builds what runtime/pprof writes for CPU: cpu/nanoseconds beside
// the redundant samples/count, with two samples, one labelled with a span.
func goCPUProfile() *profile.Profile {
	fn := &profile.Function{ID: 1, Name: "main.work", Filename: "main.go"}
	fn2 := &profile.Function{ID: 2, Name: "main.main", Filename: "main.go"}
	l1 := &profile.Location{ID: 1, Line: []profile.Line{{Function: fn, Line: 10}}}
	l2 := &profile.Location{ID: 2, Line: []profile.Line{{Function: fn2, Line: 5}}}
	return &profile.Profile{
		SampleType:    []*profile.ValueType{{Type: "samples", Unit: "count"}, {Type: "cpu", Unit: "nanoseconds"}},
		PeriodType:    &profile.ValueType{Type: "cpu", Unit: "nanoseconds"},
		Period:        10_000_000,
		TimeNanos:     time.Now().UnixNano(),
		DurationNanos: int64(10 * time.Second),
		Function:      []*profile.Function{fn, fn2},
		Location:      []*profile.Location{l1, l2},
		Sample: []*profile.Sample{
			{Location: []*profile.Location{l1, l2}, Value: []int64{3, 30_000_000},
				Label: map[string][]string{"trace_id": {testTraceHex}, "span_id": {testSpanHex}, "route": {"a", "b"}}},
			{Location: []*profile.Location{l2}, Value: []int64{1, 10_000_000}},
		},
	}
}

func encode(t *testing.T, p *profile.Profile) *bytes.Reader {
	t.Helper()
	var b bytes.Buffer
	if err := p.Write(&b); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(b.Bytes())
}

func testResource() *resource.Resource {
	return resource.NewSchemaless(attribute.String("service.name", "shop-api"), attribute.String("deployment.environment.name", "dev"))
}

func TestPprofToOTLPDropsRedundantSampleType(t *testing.T) {
	pd, err := pprofToOTLP(encode(t, goCPUProfile()), testResource())
	if err != nil {
		t.Fatal(err)
	}
	profs := pd.ResourceProfiles().At(0).ScopeProfiles().At(0).Profiles()
	if profs.Len() != 1 {
		t.Fatalf("OTLP profiles = %d, want 1 (cpu only; the samples/count twin is dropped)", profs.Len())
	}
	strs := pd.Dictionary().StringTable()
	if got := strs.At(int(profs.At(0).SampleType().TypeStrindex())); got != "cpu" {
		t.Errorf("remaining sample type = %q, want cpu", got)
	}
	if got := profs.At(0).Samples().At(0).Values().At(0); got != 30_000_000 {
		t.Errorf("cpu value = %d, want 30000000 (the cpu column, not the count)", got)
	}
}

func TestPprofToOTLPKeepsASingleSampleTypeProfile(t *testing.T) {
	p := goCPUProfile()
	p.SampleType = []*profile.ValueType{{Type: "samples", Unit: "count"}}
	for _, s := range p.Sample {
		s.Value = s.Value[:1]
	}
	pd, err := pprofToOTLP(encode(t, p), nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := pd.ResourceProfiles().At(0).ScopeProfiles().At(0).Profiles().Len(); n != 1 {
		t.Errorf("profiles = %d, want 1: dropping must never empty a profile", n)
	}
}

func TestPprofToOTLPSetsAUniqueProfileID(t *testing.T) {
	a, err := pprofToOTLP(encode(t, goCPUProfile()), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := pprofToOTLP(encode(t, goCPUProfile()), nil)
	if err != nil {
		t.Fatal(err)
	}
	ida := a.ResourceProfiles().At(0).ScopeProfiles().At(0).Profiles().At(0).ProfileID()
	idb := b.ResourceProfiles().At(0).ScopeProfiles().At(0).Profiles().At(0).ProfileID()
	if ida.IsEmpty() || idb.IsEmpty() {
		t.Fatal("profile ID is empty: the translator leaves it unset and per-profile queries key on it")
	}
	if ida == idb {
		t.Error("two profiles share an ID")
	}
}

func TestPprofToOTLPLiftsTraceLabelsIntoLinks(t *testing.T) {
	pd, err := pprofToOTLP(encode(t, goCPUProfile()), nil)
	if err != nil {
		t.Fatal(err)
	}
	profs := pd.ResourceProfiles().At(0).ScopeProfiles().At(0).Profiles()
	samples := profs.At(0).Samples()
	links := pd.Dictionary().LinkTable()

	labelled, plain := samples.At(0), samples.At(1)
	if labelled.LinkIndex() <= 0 || int(labelled.LinkIndex()) >= links.Len() {
		t.Fatalf("labelled sample link index = %d (table %d): the span label did not become a link", labelled.LinkIndex(), links.Len())
	}
	l := links.At(int(labelled.LinkIndex()))
	tid, sid := l.TraceID(), l.SpanID()
	if hex.EncodeToString(tid[:]) != testTraceHex || hex.EncodeToString(sid[:]) != testSpanHex {
		t.Errorf("link = %x/%x, want %s/%s", tid, sid, testTraceHex, testSpanHex)
	}
	if plain.LinkIndex() != 0 {
		t.Errorf("unlabelled sample link index = %d, want 0 (no link)", plain.LinkIndex())
	}
	// The ids must not ALSO linger as sample attributes.
	strs, attrs := pd.Dictionary().StringTable(), pd.Dictionary().AttributeTable()
	for _, ai := range labelled.AttributeIndices().AsRaw() {
		if k := strs.At(int(attrs.At(int(ai)).KeyStrindex())); k == "trace_id" || k == "span_id" {
			t.Errorf("sample still carries attribute %q after becoming a link", k)
		}
	}
}

func TestPprofToOTLPIgnoresMalformedTraceLabels(t *testing.T) {
	for name, labels := range map[string]map[string][]string{
		"short trace":   {"trace_id": {"abcd"}, "span_id": {testSpanHex}},
		"non hex":       {"trace_id": {strings.Repeat("zz", 16)}, "span_id": {testSpanHex}},
		"zero trace":    {"trace_id": {strings.Repeat("00", 16)}, "span_id": {testSpanHex}},
		"missing span":  {"trace_id": {testTraceHex}},
		"empty strings": {"trace_id": {""}, "span_id": {""}},
	} {
		t.Run(name, func(t *testing.T) {
			p := goCPUProfile()
			p.Sample[0].Label = labels
			pd, err := pprofToOTLP(encode(t, p), nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := pd.ResourceProfiles().At(0).ScopeProfiles().At(0).Profiles().At(0).Samples().At(0).LinkIndex(); got != 0 {
				t.Errorf("link index = %d, want 0", got)
			}
		})
	}
}

func TestPprofToOTLPCarriesTheResource(t *testing.T) {
	pd, err := pprofToOTLP(encode(t, goCPUProfile()), testResource())
	if err != nil {
		t.Fatal(err)
	}
	attrs := pd.ResourceProfiles().At(0).Resource().Attributes()
	if v, _ := attrs.Get("service.name"); v.Str() != "shop-api" {
		t.Errorf("service.name = %q, want shop-api", v.Str())
	}
	if v, _ := attrs.Get("deployment.environment.name"); v.Str() != "dev" {
		t.Errorf("deployment.environment.name = %q, want dev", v.Str())
	}
}

func TestPprofToOTLPEmptyAndGarbage(t *testing.T) {
	empty := goCPUProfile()
	empty.Sample = nil
	pd, err := pprofToOTLP(encode(t, empty), nil)
	if err != nil || pd.SampleCount() != 0 {
		t.Errorf("empty profile: err=%v samples=%d, want nil/0", err, pd.SampleCount())
	}
	if _, err := pprofToOTLP(strings.NewReader("not a profile"), nil); err == nil {
		t.Error("garbage input must be an error")
	}
}

func TestPprofToOTLPRoundTripsThroughTheWire(t *testing.T) {
	pd, err := pprofToOTLP(encode(t, goCPUProfile()), testResource())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := pprofileotlp.NewExportRequestFromProfiles(pd).MarshalProto()
	if err != nil {
		t.Fatal(err)
	}
	back := pprofileotlp.NewExportRequest()
	if err := back.UnmarshalProto(raw); err != nil {
		t.Fatal(err)
	}
	if back.Profiles().SampleCount() != pd.SampleCount() {
		t.Errorf("samples after the wire = %d, want %d", back.Profiles().SampleCount(), pd.SampleCount())
	}
}

// ── environment resolution ───────────────────────────────────────────────

func clearProfilesEnv(t *testing.T) {
	t.Helper()
	clearOTelEnv(t)
	for _, k := range []string{EnvProfilesExporter, EnvOTLPProfilesEndpoint, "OTEL_EXPORTER_OTLP_PROFILES_PROTOCOL",
		"OTEL_EXPORTER_OTLP_PROFILES_HEADERS", "OTEL_EXPORTER_OTLP_COMPRESSION", "OTEL_EXPORTER_OTLP_PROFILES_COMPRESSION",
		"OTEL_EXPORTER_OTLP_TIMEOUT", "OTEL_EXPORTER_OTLP_PROFILES_TIMEOUT", EnvProfiles, EnvProfilesHeapInterval} {
		t.Setenv(k, "")
	}
}

func TestProfilesAreOnByDefaultWithAnEndpoint(t *testing.T) {
	clearProfilesEnv(t)
	if exportEnabled(signalProfiles, Config{}) {
		t.Error("profiles on with no endpoint anywhere")
	}
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")
	if !exportEnabled(signalProfiles, Config{}) {
		t.Error("profiles off with an endpoint: they are on by default")
	}
	if !exportEnabled(signalProfiles, Config{OTLPEndpoint: "collector:4318"}) {
		t.Error("profiles off with a typed endpoint")
	}
}

func TestProfilesCanBeSwitchedOff(t *testing.T) {
	clearProfilesEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")

	if exportEnabled(signalProfiles, Config{ProfilesDisabled: true}) {
		t.Error("Config.ProfilesDisabled did not turn profiles off")
	}
	t.Setenv(EnvProfilesExporter, "none")
	if exportEnabled(signalProfiles, Config{}) {
		t.Error("OTEL_PROFILES_EXPORTER=none did not turn profiles off")
	}
	t.Setenv(EnvProfilesExporter, "NONE")
	if exportEnabled(signalProfiles, Config{}) {
		t.Error("OTEL_PROFILES_EXPORTER=NONE (any case) did not turn profiles off")
	}
	// Turning profiles off leaves the other signals alone.
	if !exportEnabled(signalTraces, Config{}) {
		t.Error("switching profiles off disabled traces")
	}
	t.Setenv(EnvProfilesExporter, "")
	t.Setenv(EnvProfiles, "none")
	if exportEnabled(signalProfiles, Config{}) {
		t.Error("FORGE_PROFILES=none did not turn profiles off")
	}
	t.Setenv(EnvProfiles, "")
	if exportEnabled(signalProfiles, Config{Profiles: ProfilesConfig{Types: []ProfileType{}}}) {
		t.Error("an empty Config.Profiles.Types did not turn profiles off")
	}
	t.Setenv("OTEL_SDK_DISABLED", "true")
	if exportEnabled(signalProfiles, Config{}) {
		t.Error("OTEL_SDK_DISABLED=true must disable profiles too")
	}
}

func TestProfilesDoNotChangeWhatTracesDo(t *testing.T) {
	clearProfilesEnv(t)
	t.Setenv(EnvOTLPProfilesEndpoint, "http://profiles.internal:4318/v1development/profiles")
	// A profiles-only endpoint is an endpoint for profiles, not for traces.
	if exportEnabled(signalTraces, Config{}) {
		t.Error("a profiles endpoint turned traces on")
	}
	if !exportEnabled(signalProfiles, Config{}) {
		t.Error("a profiles endpoint did not turn profiles on")
	}
}

func TestResolveProfilesTarget(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		cfg     Config
		url     string
		proto   string
		headers map[string]string
		wantErr bool
	}{
		{name: "base endpoint gains the profiles path",
			env: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:4318"},
			url: "http://127.0.0.1:4318/v1development/profiles", proto: ProtocolHTTPProtobuf},
		{name: "trailing slash",
			env: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4318/"},
			url: "http://collector:4318/v1development/profiles", proto: ProtocolHTTPProtobuf},
		{name: "bare host:port",
			cfg: Config{OTLPEndpoint: "collector:4318"},
			url: "http://collector:4318/v1development/profiles", proto: ProtocolHTTPProtobuf},
		{name: "typed beats env base",
			env: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://env:4318"}, cfg: Config{OTLPEndpoint: "http://typed:4318"},
			url: "http://typed:4318/v1development/profiles", proto: ProtocolHTTPProtobuf},
		{name: "profiles endpoint is a full URL used as given",
			env: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://traces:4318", EnvOTLPProfilesEndpoint: "http://profiles:4318/custom/path"},
			url: "http://profiles:4318/custom/path", proto: ProtocolHTTPProtobuf},
		{name: "grpc dials host:port",
			env: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4317", "OTEL_EXPORTER_OTLP_PROTOCOL": "grpc"},
			url: "collector:4317", proto: ProtocolGRPC},
		{name: "headers merge, per-signal wins",
			env: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318", "OTEL_EXPORTER_OTLP_HEADERS": "authorization=base,x-a=1",
				"OTEL_EXPORTER_OTLP_PROFILES_HEADERS": "authorization=profiles"},
			url: "http://c:4318/v1development/profiles", proto: ProtocolHTTPProtobuf,
			headers: map[string]string{"authorization": "profiles", "x-a": "1"}},
		{name: "no endpoint", wantErr: true},
		{name: "unsupported protocol",
			env:     map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318", "OTEL_EXPORTER_OTLP_PROTOCOL": "http/json"},
			wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearProfilesEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := resolveProfilesTarget(tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.URL != tc.url || got.Protocol != tc.proto {
				t.Errorf("target = %q %q, want %q %q", got.URL, got.Protocol, tc.url, tc.proto)
			}
			for k, v := range tc.headers {
				if got.Headers[k] != v {
					t.Errorf("header %s = %q, want %q", k, got.Headers[k], v)
				}
			}
		})
	}
}

// ── Setup wiring ─────────────────────────────────────────────────────────

func TestSetupWithNoEndpointStartsNoProfiler(t *testing.T) {
	clearProfilesEnv(t)
	profilingActive.Store(false)
	shutdown, _, err := Setup(context.Background(), Config{ServiceName: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })
	if profilingActive.Load() {
		t.Error("a profiler is running with no OTLP endpoint")
	}
}

func TestSetupStartsAndStopsTheProfiler(t *testing.T) {
	clearProfilesEnv(t)
	// Unreachable on purpose: nothing may block or fail.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	shutdown, _, err := Setup(context.Background(), Config{ServiceName: "svc"})
	if err != nil {
		t.Fatalf("Setup must not fail because of profiling: %v", err)
	}
	if !profilingActive.Load() {
		t.Fatal("no profiler running although an endpoint is set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := shutdown(ctx); err != nil {
		t.Errorf("shutdown: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("shutdown took %s while the profiler was idle", time.Since(start))
	}
	if profilingActive.Load() {
		t.Error("profiler still marked active after shutdown")
	}
}

func TestSetupWithProfilesOffStartsNoProfiler(t *testing.T) {
	clearProfilesEnv(t)
	profilingActive.Store(false)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	t.Setenv(EnvProfilesExporter, "none")
	shutdown, _, err := Setup(context.Background(), Config{ServiceName: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })
	if profilingActive.Load() {
		t.Error("OTEL_PROFILES_EXPORTER=none still started a profiler")
	}
}

// ── the loop ─────────────────────────────────────────────────────────────

// testProfiler is a profiler whose clock and collector are fakes, capturing the
// default profile set.
func testProfiler(probe func(context.Context) error, send func(context.Context, pprofile.Profiles) error) *profiler {
	settings, _ := resolveProfileSettings(ProfilesConfig{})
	return &profiler{
		res:      testResource(),
		interval: time.Minute,
		window:   10 * time.Millisecond,
		settings: settings,
		now:      time.Now,
		probe:    probe,
		send:     send,
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		sleep: func(ctx context.Context, d time.Duration) bool {
			if d > 50*time.Millisecond {
				d = 0
			}
			return sleepCtx(ctx, d)
		},
	}
}

func TestCycleSkipsEverythingWhenTheCollectorIsUnreachable(t *testing.T) {
	var sent atomic.Int32
	p := testProfiler(
		func(context.Context) error { return errors.New("connection refused") },
		func(context.Context, pprofile.Profiles) error { sent.Add(1); return nil },
	)
	start := time.Now()
	err := p.safeCycle(context.Background())
	if err == nil {
		t.Fatal("an unreachable collector must be reported as a failed cycle")
	}
	if sent.Load() != 0 {
		t.Error("sent profiles to an unreachable collector")
	}
	if time.Since(start) > p.window {
		t.Errorf("an unreachable collector still cost %s: the CPU window must be skipped", time.Since(start))
	}
}

// recordingSender records the sample types of every profile sent.
type recordingSender struct {
	mu    sync.Mutex
	types map[string]int
	ids   []pprofile.ProfileID
}

func (r *recordingSender) send(_ context.Context, pd pprofile.Profiles) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.types == nil {
		r.types = map[string]int{}
	}
	strs := pd.Dictionary().StringTable()
	profs := pd.ResourceProfiles().At(0).ScopeProfiles().At(0).Profiles()
	for i := 0; i < profs.Len(); i++ {
		r.types[strs.At(int(profs.At(i).SampleType().TypeStrindex()))]++
		r.ids = append(r.ids, profs.At(i).ProfileID())
	}
	return nil
}

func (r *recordingSender) snapshot() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]int{}
	for k, v := range r.types {
		out[k] = v
	}
	return out
}

// busyCPU gives the CPU profiler something to see until the test ends.
func busyCPU(t *testing.T) {
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				_ = strings.Repeat("x", 1<<10)
			}
		}
	}()
	t.Cleanup(func() { close(stop) })
}

func TestCycleSendsTheDefaultProfiles(t *testing.T) {
	rec := &recordingSender{}
	p := testProfiler(func(context.Context) error { return nil }, rec.send)
	busyCPU(t)
	p.window = 300 * time.Millisecond
	p.sleep = sleepCtx

	if err := p.safeCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	types := rec.snapshot()
	if types["samples"] > 0 {
		t.Error("the redundant samples type was exported")
	}
	for _, want := range []string{"cpu", "inuse_space", "inuse_objects"} {
		if types[want] == 0 {
			t.Errorf("default sample type %q never exported; got %v", want, types)
		}
	}
	// Cumulative allocation counters and goroutine stacks are opt-in: they
	// were 97.7% and 0.6% of production's stored profile rows.
	for _, off := range []string{"alloc_space", "alloc_objects", "goroutine"} {
		if types[off] > 0 {
			t.Errorf("opt-in sample type %q exported by default; got %v", off, types)
		}
	}
	for _, id := range rec.ids {
		if id.IsEmpty() {
			t.Error("exported a profile with an empty profile ID")
		}
	}
}

func TestCycleSendsTheOptInProfilesWhenSelected(t *testing.T) {
	clearProfilesEnv(t)
	t.Setenv(EnvProfiles, "alloc,goroutine")
	rec := &recordingSender{}
	p := testProfiler(func(context.Context) error { return nil }, rec.send)
	p.settings, _ = resolveProfileSettings(ProfilesConfig{})

	if err := p.safeCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	types := rec.snapshot()
	for _, want := range []string{"alloc_space", "alloc_objects", "goroutine"} {
		if types[want] == 0 {
			t.Errorf("selected sample type %q never exported; got %v", want, types)
		}
	}
	// The selection replaces the default set: no CPU, and alloc without heap
	// exports the cumulative types alone.
	for _, off := range []string{"cpu", "inuse_space", "inuse_objects"} {
		if types[off] > 0 {
			t.Errorf("unselected sample type %q exported; got %v", off, types)
		}
	}
}

func TestCycleSurvivesAPanic(t *testing.T) {
	p := testProfiler(func(context.Context) error { panic("boom") }, nil)
	err := p.safeCycle(context.Background())
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("a panicking cycle must come back as an error, got %v", err)
	}
}

func TestCycleSkipsCPUWhenAnotherProfileIsRunning(t *testing.T) {
	var cpu bytes.Buffer
	if err := pprof.StartCPUProfile(&cpu); err != nil {
		t.Skipf("cannot start a competing CPU profile: %v", err)
	}
	defer pprof.StopCPUProfile()

	var sent atomic.Int32
	p := testProfiler(func(context.Context) error { return nil },
		func(context.Context, pprofile.Profiles) error { sent.Add(1); return nil })
	if err := p.safeCycle(context.Background()); err != nil {
		t.Fatalf("a busy CPU profiler is not a failure: %v", err)
	}
	if sent.Load() == 0 {
		t.Error("heap and goroutine profiles should still be sent")
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	p := testProfiler(nil, nil)
	within := func(got, center time.Duration) bool {
		lo, hi := time.Duration(float64(center)*(1-profilerJitter)), time.Duration(float64(center)*(1+profilerJitter))
		return got >= lo && got <= hi
	}
	for failures, center := range map[int]time.Duration{0: time.Minute, 1: 2 * time.Minute, 2: 4 * time.Minute, 3: 8 * time.Minute, 4: profilerMaxBackoff, 30: profilerMaxBackoff} {
		p.failures = failures
		for i := 0; i < 50; i++ {
			if got := p.nextWait(); !within(got, center) {
				t.Fatalf("after %d failures wait = %s, want %s ±%d%%", failures, got, center, int(profilerJitter*100))
			}
		}
	}
}

func TestRunStopsOnCancelAndRecoversAfterFailures(t *testing.T) {
	var probes atomic.Int32
	var sent atomic.Int32
	p := testProfiler(
		func(context.Context) error {
			if probes.Add(1) <= 2 {
				return errors.New("down")
			}
			return nil
		},
		func(context.Context, pprofile.Profiles) error { sent.Add(1); return nil },
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.run(ctx); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for sent.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if sent.Load() == 0 {
		t.Fatal("the loop never recovered after the collector came back")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after cancel")
	}
}

func TestTCPProbeAgainstClosedAndOpenPorts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	open := tcpProbe(profilesTarget{Protocol: ProtocolHTTPProtobuf, URL: "http://" + addr + "/v1development/profiles"})
	if err := open(context.Background()); err != nil {
		t.Errorf("probe of a listening port: %v", err)
	}
	ln.Close()
	if err := open(context.Background()); err == nil {
		t.Error("probe of a closed port succeeded")
	}
}

func TestHTTPSenderPostsGzippedProtobufWithHeaders(t *testing.T) {
	var gotPath, gotAuth, gotEnc, gotCT string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotEnc, gotCT = r.URL.Path, r.Header.Get("authorization"), r.Header.Get("Content-Encoding"), r.Header.Get("Content-Type")
		zr, err := gzip.NewReader(r.Body)
		if err == nil {
			gotBody, _ = io.ReadAll(zr)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	send, closeFn, err := newProfilesSender(profilesTarget{
		Protocol: ProtocolHTTPProtobuf, URL: srv.URL + "/v1development/profiles",
		Headers: map[string]string{"authorization": "key"}, Timeout: 5 * time.Second, Gzip: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	pd, err := pprofToOTLP(encode(t, goCPUProfile()), testResource())
	if err != nil {
		t.Fatal(err)
	}
	if err := send(context.Background(), pd); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1development/profiles" || gotAuth != "key" || gotEnc != "gzip" || gotCT != "application/x-protobuf" {
		t.Errorf("request = %s auth=%q enc=%q ct=%q", gotPath, gotAuth, gotEnc, gotCT)
	}
	req := pprofileotlp.NewExportRequest()
	if err := req.UnmarshalProto(gotBody); err != nil {
		t.Fatalf("server received a body that is not OTLP profiles: %v", err)
	}
	if req.Profiles().SampleCount() != pd.SampleCount() {
		t.Errorf("received %d samples, sent %d", req.Profiles().SampleCount(), pd.SampleCount())
	}
}

func TestHTTPSenderNamesTheFeatureGateOn404(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	send, _, err := newProfilesSender(profilesTarget{Protocol: ProtocolHTTPProtobuf, URL: srv.URL + "/v1development/profiles", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	pd, _ := pprofToOTLP(encode(t, goCPUProfile()), nil)
	err = send(context.Background(), pd)
	if err == nil || !strings.Contains(err.Error(), "profilesSupport") {
		t.Errorf("a 404 should point at the collector feature gate, got %v", err)
	}
}

// ── labels → links ───────────────────────────────────────────────────────

func TestSpanProfileLabelsFollowTheActiveSpan(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background())
	ctx, span := tp.Tracer("t").Start(context.Background(), "op")
	defer span.End()

	profilingActive.Store(true)
	defer profilingActive.Store(false)

	var gotTrace, gotSpan string
	withSpanProfileLabels(ctx, func(ctx context.Context) {
		gotTrace, _ = pprof.Label(ctx, "trace_id")
		gotSpan, _ = pprof.Label(ctx, "span_id")
	})
	if gotTrace != span.SpanContext().TraceID().String() || gotSpan != span.SpanContext().SpanID().String() {
		t.Errorf("labels = %q/%q, want %s/%s", gotTrace, gotSpan, span.SpanContext().TraceID(), span.SpanContext().SpanID())
	}
}

func TestSpanProfileLabelsAreSkippedWhenProfilingIsOffOrThereIsNoSpan(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background())
	ctx, span := tp.Tracer("t").Start(context.Background(), "op")
	defer span.End()

	check := func(name string, ctx context.Context) {
		t.Helper()
		ran := false
		withSpanProfileLabels(ctx, func(ctx context.Context) {
			ran = true
			if v, ok := pprof.Label(ctx, "trace_id"); ok {
				t.Errorf("%s: unexpected trace_id label %q", name, v)
			}
		})
		if !ran {
			t.Errorf("%s: the wrapped function never ran", name)
		}
	}
	profilingActive.Store(false)
	check("profiling off", ctx)
	profilingActive.Store(true)
	defer profilingActive.Store(false)
	check("no span", context.Background())
	check("invalid span", trace.ContextWithSpanContext(context.Background(), trace.SpanContext{}))
}

func TestTracingInterceptorLabelsTheHandler(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background())
	profilingActive.Store(true)
	defer profilingActive.Store(false)

	var gotTrace string
	next := connect.UnaryFunc(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		gotTrace, _ = pprof.Label(ctx, "trace_id")
		return connect.NewResponse(&structpb.Struct{}), nil
	})
	wrapped := TracingInterceptor(tp.Tracer("t")).WrapUnary(next)
	if _, err := wrapped(context.Background(), connect.NewRequest(&structpb.Struct{})); err != nil {
		t.Fatal(err)
	}
	if len(gotTrace) != 32 {
		t.Errorf("handler saw trace_id label %q, want the 32-hex trace id of the RPC's span", gotTrace)
	}
}

func TestProfileLabelsMiddlewareLabelsHTTPHandlers(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background())
	profilingActive.Store(true)
	defer profilingActive.Store(false)

	var gotSpan string
	h := ProfileLabelsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSpan, _ = pprof.Label(r.Context(), "span_id")
	}))
	ctx, span := tp.Tracer("t").Start(context.Background(), "op")
	defer span.End()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx))
	if gotSpan != span.SpanContext().SpanID().String() {
		t.Errorf("span_id label = %q, want %s", gotSpan, span.SpanContext().SpanID())
	}
}

// A profile captured around a labelled goroutine must come out with that
// goroutine's samples linked, end to end through runtime/pprof's own encoder.
func TestRealCPUProfileLinksLabelledWork(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: burns CPU for 1s under the real profiler; runs in task test")
	}
	var buf bytes.Buffer
	if err := pprof.StartCPUProfile(&buf); err != nil {
		t.Skip(err)
	}
	deadline := time.Now().Add(time.Second)
	pprof.Do(context.Background(), pprof.Labels("trace_id", testTraceHex, "span_id", testSpanHex), func(context.Context) {
		x := 0
		for time.Now().Before(deadline) {
			for i := 0; i < 1e6; i++ {
				x += i * i
			}
		}
		_ = x
	})
	pprof.StopCPUProfile()

	pd, err := pprofToOTLP(&buf, testResource())
	if err != nil {
		t.Fatal(err)
	}
	profs := pd.ResourceProfiles().At(0).ScopeProfiles().At(0).Profiles()
	if profs.Len() != 1 {
		t.Fatalf("real Go CPU profile became %d OTLP profiles, want 1", profs.Len())
	}
	linked := 0
	for i := 0; i < profs.At(0).Samples().Len(); i++ {
		if profs.At(0).Samples().At(i).LinkIndex() > 0 {
			linked++
		}
	}
	if linked == 0 {
		t.Error("no CPU sample was linked to the labelled span")
	}
}

// The span a profile sample links to is opened by otelconnect (Deps.Trace), not
// by TracingInterceptor, so the labels come from the interceptor Chain places
// directly inside it. Without it the producer had no trace_id/span_id label to
// turn into an OTLP link.
func TestChainLabelsTheHandlerWithTheTraceSpan(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background())
	profilingActive.Store(true)
	defer profilingActive.Store(false)

	var span trace.Span
	opensSpan := connectInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			ctx, span = tp.Tracer("t").Start(ctx, "rpc")
			return next(ctx, req)
		}
	})

	var gotTrace, gotSpan string
	handler := connect.UnaryFunc(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		gotTrace, _ = pprof.Label(ctx, "trace_id")
		gotSpan, _ = pprof.Label(ctx, "span_id")
		return nil, nil
	})
	chain := Chain(Deps{Trace: opensSpan})
	for i := len(chain) - 1; i >= 0; i-- {
		handler = chain[i].WrapUnary(handler)
	}
	_, _ = handler(context.Background(), connect.NewRequest(&structpb.Struct{}))

	if span == nil || gotTrace != span.SpanContext().TraceID().String() || gotSpan != span.SpanContext().SpanID().String() {
		t.Errorf("handler labels = %q/%q, want the trace span's ids", gotTrace, gotSpan)
	}
}

type connectInterceptorFunc func(connect.UnaryFunc) connect.UnaryFunc

func (f connectInterceptorFunc) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc { return f(next) }
func (connectInterceptorFunc) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}
func (connectInterceptorFunc) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
