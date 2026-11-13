package observe

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/pprof/profile"
	pproftrans "github.com/open-telemetry/opentelemetry-collector-contrib/pkg/translator/pprof"
	"go.opentelemetry.io/collector/pdata/pprofile"
	"go.opentelemetry.io/collector/pdata/pprofile/pprofileotlp"
)

// Runtime heap shape: many historical allocation sites, few live stacks, and
// inactive zero stacks. Both bytes and objects are required, not redundant.
func heapFixture(n int) *profile.Profile {
	p := &profile.Profile{
		SampleType: []*profile.ValueType{{Type: "alloc_objects", Unit: "count"}, {Type: "alloc_space", Unit: "bytes"}, {Type: "inuse_objects", Unit: "count"}, {Type: "inuse_space", Unit: "bytes"}},
		PeriodType: &profile.ValueType{Type: "space", Unit: "bytes"}, Period: 524288,
		TimeNanos: time.Date(2026, 10, 15, 17, 0, 0, 0, time.UTC).UnixNano(),
	}
	for i := 0; i < n; i++ {
		fn := &profile.Function{ID: uint64(i + 1), Name: fmt.Sprintf("app.allocate%d", i), Filename: "worker.go"}
		loc := &profile.Location{ID: uint64(i + 1), Line: []profile.Line{{Function: fn, Line: int64(i + 1)}}}
		p.Function = append(p.Function, fn)
		p.Location = append(p.Location, loc)
		stack := make([]*profile.Location, 24)
		for j := range stack {
			stack[j] = loc
		}
		values := []int64{100, 6400, 0, 0}
		if i%100 == 0 {
			values[2], values[3] = 2, 128
		}
		if i == n-1 {
			values = []int64{0, 0, 0, 0}
		}
		p.Sample = append(p.Sample, &profile.Sample{Location: stack, Value: values})
	}
	p.Sample[0].Label = map[string][]string{"trace_id": {testTraceHex}, "span_id": {testSpanHex}}
	return p
}

func profileTotals(pd pprofile.Profiles) map[string]int64 {
	totals := map[string]int64{}
	for i := 0; i < pd.ResourceProfiles().Len(); i++ {
		scopes := pd.ResourceProfiles().At(i).ScopeProfiles()
		for j := 0; j < scopes.Len(); j++ {
			profs := scopes.At(j).Profiles()
			for k := 0; k < profs.Len(); k++ {
				p := profs.At(k)
				typ := pd.Dictionary().StringTable().At(int(p.SampleType().TypeStrindex()))
				totals[typ] += 0
				for s := 0; s < p.Samples().Len(); s++ {
					for _, v := range p.Samples().At(s).Values().AsRaw() {
						totals[typ] += v
					}
				}
			}
		}
	}
	return totals
}

func TestHeapSparseSnapshotsPreserveSemantics(t *testing.T) {
	for snapshot := 0; snapshot < 3; snapshot++ {
		p := heapFixture(100)
		p.TimeNanos += int64(snapshot) * int64(time.Minute)
		// Allocation counters stay cumulative even when live heap goes to zero.
		p.Sample[0].Value[0] += int64(snapshot)
		if snapshot == 2 {
			p.Sample[0].Value[2], p.Sample[0].Value[3] = 0, 0
		}
		pd, err := pprofToOTLP(encode(t, p), testResource())
		if err != nil {
			t.Fatal(err)
		}
		totals := profileTotals(pd)
		if len(totals) != 4 {
			t.Fatalf("lost sample types: %v", totals)
		}
		profs := pd.ResourceProfiles().At(0).ScopeProfiles().At(0).Profiles()
		for i := 0; i < profs.Len(); i++ {
			prof := profs.At(i)
			typ := pd.Dictionary().StringTable().At(int(prof.SampleType().TypeStrindex()))
			var want int64
			for st, vt := range p.SampleType {
				if vt.Type == typ {
					if got := pd.Dictionary().StringTable().At(int(prof.SampleType().UnitStrindex())); got != vt.Unit {
						t.Fatalf("%s unit %s", typ, got)
					}
					for _, s := range p.Sample {
						want += s.Value[st]
					}
				}
			}
			if totals[typ] != want {
				t.Fatalf("snapshot %d %s = %d want cumulative/gauge %d", snapshot, typ, totals[typ], want)
			}
			if int64(prof.Time()) != p.TimeNanos || prof.DurationNano() != 0 {
				t.Fatal("changed snapshot time semantics")
			}
			for s := 0; s < prof.Samples().Len(); s++ {
				sample := prof.Samples().At(s)
				if sample.Values().At(0) == 0 {
					t.Fatalf("%s retained useless zero row", typ)
				}
			}
			if prof.Samples().Len() > 0 && prof.Samples().At(0).LinkIndex() == 0 {
				t.Fatalf("lost link on %s after sparse filtering", typ)
			}
		}
	}
	// Leading zeros must not shift the link onto a different surviving stack.
	leading := goCPUProfile()
	leading.Sample[0].Value = []int64{0, 0}
	leading.Sample[1].Label = map[string][]string{"trace_id": {testTraceHex}, "span_id": {testSpanHex}}
	linked, err := pprofToOTLP(encode(t, leading), nil)
	if err != nil {
		t.Fatal(err)
	}
	survivors := linked.ResourceProfiles().At(0).ScopeProfiles().At(0).Profiles().At(0).Samples()
	if survivors.Len() != 1 || survivors.At(0).LinkIndex() == 0 || survivors.At(0).Values().At(0) != 10_000_000 {
		t.Fatal("sparse filtering shifted the surviving sample's link/value")
	}

	// Negative differential values are meaningful too; only zero is removable.
	p := goCPUProfile()
	p.Sample[0].Value[1] = -1
	pd, err := pprofToOTLP(encode(t, p), nil)
	if err != nil {
		t.Fatal(err)
	}
	if profileTotals(pd)["cpu"] != 9999999 {
		t.Fatal("discarded signed nonzero sample")
	}
}

// The default heap export keeps only the live-heap types. Their totals, units,
// time and trace link must match the full snapshot's, and the allocation-only
// stacks must not ride along in the dictionary.
func TestHeapLiveOnlyKeepsTotalsAndShrinksTheDictionary(t *testing.T) {
	p := heapFixture(300) // stacks 0, 100 and 200 are live; stack 0 carries the link
	full, err := pprofToOTLP(encode(t, p), testResource())
	if err != nil {
		t.Fatal(err)
	}
	live, err := pprofToOTLP(encode(t, p), testResource(), "alloc_objects", "alloc_space")
	if err != nil {
		t.Fatal(err)
	}
	fullTotals, liveTotals := profileTotals(full), profileTotals(live)
	if len(liveTotals) != 2 || liveTotals["inuse_objects"] != fullTotals["inuse_objects"] || liveTotals["inuse_space"] != fullTotals["inuse_space"] {
		t.Fatalf("live-only totals %v, want the full snapshot's inuse totals of %v", liveTotals, fullTotals)
	}
	if live.SampleCount() != 6 {
		t.Fatalf("live-only rows = %d, want 3 live stacks x 2 types", live.SampleCount())
	}
	profs := live.ResourceProfiles().At(0).ScopeProfiles().At(0).Profiles()
	for i := 0; i < profs.Len(); i++ {
		prof := profs.At(i)
		if int64(prof.Time()) != p.TimeNanos {
			t.Fatal("changed snapshot time")
		}
		if prof.Samples().At(0).LinkIndex() == 0 {
			t.Fatal("lost the trace link of the live stack")
		}
	}
	if got, all := live.Dictionary().FunctionTable().Len(), full.Dictionary().FunctionTable().Len(); got*10 > all {
		t.Errorf("live-only dictionary has %d functions of the full snapshot's %d: allocation-only stacks rode along", got, all)
	}
	// The cumulative types alone (alloc opted in, heap not) keep theirs too.
	alloc, err := pprofToOTLP(encode(t, p), testResource(), "inuse_objects", "inuse_space")
	if err != nil {
		t.Fatal(err)
	}
	if at := profileTotals(alloc); at["alloc_space"] != fullTotals["alloc_space"] || at["alloc_objects"] != fullTotals["alloc_objects"] || len(at) != 2 {
		t.Fatalf("alloc-only totals %v, want the full snapshot's %v", at, fullTotals)
	}
}

// This is a transport replay proof, NOT a claim of ClickHouse exactly-once.
// An ambiguous response can persist a duplicate; the sender must not regenerate
// IDs or expand rows when the same captured payload is retried.
func TestProfileRetryKeepsIdentityAndRows(t *testing.T) {
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		if len(bodies) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	send, closeFn, err := newProfilesSender(profilesTarget{Protocol: ProtocolHTTPProtobuf, URL: server.URL, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	pd, err := pprofToOTLP(encode(t, heapFixture(100)), testResource())
	if err != nil {
		t.Fatal(err)
	}
	if send(context.Background(), pd) == nil {
		t.Fatal("expected first failure")
	}
	if err := send(context.Background(), pd); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatal("retry changed captured payload/IDs")
	}
	back := pprofileotlp.NewExportRequest()
	if err := back.UnmarshalProto(bodies[1]); err != nil {
		t.Fatal(err)
	}
	if back.Profiles().SampleCount() != 200 {
		t.Fatalf("replay expanded rows: %d", back.Profiles().SampleCount())
	}
}

func TestHeapVolumeEvidence(t *testing.T) {
	if testing.Short() {
		t.Skip("60 realistic repeated snapshots and wire encoding; runs in task test")
	}
	var beforeRows, beforeBytes, sparseRows, sparseBytes, allocRows, allocBytes, defaultRows, defaultBytes int
	start := time.Date(2026, 10, 15, 17, 0, 0, 0, time.UTC)
	settings := profileSettings{cpu: true, heap: true, heapInterval: DefaultHeapInterval}
	for minute := 0; minute < 60; minute++ {
		p := heapFixture(1000 + minute*10) // growing historical stacks, ~1% live
		p.TimeNanos = start.Add(time.Duration(minute) * time.Minute).UnixNano()
		legacy, err := pproftrans.ConvertPprofToProfiles(p.Copy())
		if err != nil {
			t.Fatal(err)
		}
		rp := legacy.ResourceProfiles().At(0)
		putResourceAttributes(rp.Resource().Attributes(), testResource())
		sp := rp.ScopeProfiles().At(0)
		sp.Scope().SetName(profilesScopeName)
		for i := 0; i < sp.Profiles().Len(); i++ {
			sp.Profiles().At(i).SetProfileID(newProfileID())
		}
		b, err := pprofileotlp.NewExportRequestFromProfiles(*legacy).MarshalProto()
		if err != nil {
			t.Fatal(err)
		}
		beforeRows += legacy.SampleCount()
		beforeBytes += len(b)
		pd, err := pprofToOTLP(encode(t, p), testResource())
		if err != nil {
			t.Fatal(err)
		}
		b, err = pprofileotlp.NewExportRequestFromProfiles(pd).MarshalProto()
		if err != nil {
			t.Fatal(err)
		}
		sparseRows += pd.SampleCount()
		sparseBytes += len(b)
		if minute%15 != 0 {
			continue
		}
		// The opt-in alloc profile: every heap type at the 15m heap cadence.
		allocRows += pd.SampleCount()
		allocBytes += len(b)
		// The default: the live heap alone, at the 15m heap cadence.
		live, err := pprofToOTLP(encode(t, p), testResource(), settings.heapDrops()...)
		if err != nil {
			t.Fatal(err)
		}
		b, err = pprofileotlp.NewExportRequestFromProfiles(live).MarshalProto()
		if err != nil {
			t.Fatal(err)
		}
		defaultRows += live.SampleCount()
		defaultBytes += len(b)
	}
	t.Logf("one simulated hour: legacy rows=%d OTLP bytes=%d; previous default (sparse, every heap type every minute) rows=%d bytes=%d; alloc opted in (every heap type, 15m) rows=%d bytes=%d; default (live heap, 15m) rows=%d bytes=%d",
		beforeRows, beforeBytes, sparseRows, sparseBytes, allocRows, allocBytes, defaultRows, defaultBytes)
	if allocRows*20 >= beforeRows || allocBytes*10 >= beforeBytes {
		t.Fatal("full snapshot amplification not bounded as expected")
	}
	// ~1% of the fixture's stacks are live, so the default keeps about 1/15 of
	// 1% of the previous default's heap rows.
	if defaultRows*500 >= sparseRows {
		t.Fatalf("default heap rows %d not bounded below 0.2%% of the previous default's %d", defaultRows, sparseRows)
	}
}
