package profilequery

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/pprof/profile"
)

const rows = `{"frames":["main.main","main.work","crypto/sha256.Sum256"],"unit":"nanoseconds","value":"7000"}
{"frames":["main.main","main.idle"],"unit":"nanoseconds","value":3000}
{"frames":[],"unit":"nanoseconds","value":5}
{"frames":["main.main","zero"],"unit":"nanoseconds","value":0}
`

func parsed(t *testing.T) Profile {
	t.Helper()
	p, err := Parse(strings.NewReader(rows), Query{Service: "api", SampleType: "cpu", From: time.Unix(1000, 0), To: time.Unix(4600, 0)})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseKeepsRealStacksSortedByValue(t *testing.T) {
	p := parsed(t)
	if len(p.Stacks) != 2 || p.Total() != 10000 || p.Unit != "nanoseconds" {
		t.Fatalf("stacks=%d total=%d unit=%q, want 2/10000/nanoseconds (quoted 64-bit values, empty and zero stacks dropped)", len(p.Stacks), p.Total(), p.Unit)
	}
	if p.Stacks[0].Value != 7000 || p.Stacks[0].Frames[0] != "main.main" {
		t.Errorf("first stack = %+v, want the heaviest, root first", p.Stacks[0])
	}
}

func TestFolded(t *testing.T) {
	var b bytes.Buffer
	if err := parsed(t).Folded(&b); err != nil {
		t.Fatal(err)
	}
	want := "main.main;main.work;crypto/sha256.Sum256 7000\nmain.main;main.idle 3000\n"
	if b.String() != want {
		t.Errorf("folded =\n%s\nwant\n%s", b.String(), want)
	}
}

func TestPprofOpensAndKeepsRootToLeafOrder(t *testing.T) {
	var b bytes.Buffer
	if err := parsed(t).Pprof(&b); err != nil {
		t.Fatal(err)
	}
	if zr, err := gzip.NewReader(bytes.NewReader(b.Bytes())); err != nil {
		t.Fatalf("pprof output is not gzip: %v", err)
	} else {
		zr.Close()
	}
	pp, err := profile.Parse(bytes.NewReader(b.Bytes()))
	if err != nil {
		t.Fatalf("go tool pprof could not parse it: %v", err)
	}
	if got := pp.SampleType[0]; got.Type != "cpu" || got.Unit != "nanoseconds" {
		t.Errorf("sample type = %+v", got)
	}
	if len(pp.Sample) != 2 {
		t.Fatalf("samples = %d, want 2", len(pp.Sample))
	}
	s := pp.Sample[0]
	if s.Value[0] != 7000 || s.Location[0].Line[0].Function.Name != "crypto/sha256.Sum256" || s.Location[len(s.Location)-1].Line[0].Function.Name != "main.main" {
		t.Errorf("sample 0 = value %d, leaf %q, root %q; pprof stacks run leaf first", s.Value[0], s.Location[0].Line[0].Function.Name, s.Location[len(s.Location)-1].Line[0].Function.Name)
	}
	// A function shared by two stacks is one Function, not two.
	names := map[string]int{}
	for _, f := range pp.Function {
		names[f.Name]++
	}
	if names["main.main"] != 1 {
		t.Errorf("main.main appears %d times in the function table", names["main.main"])
	}
}

func TestSpeedscopeIsTheDocumentedFormat(t *testing.T) {
	var b bytes.Buffer
	if err := parsed(t).Speedscope(&b); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema string `json:"$schema"`
		Shared struct {
			Frames []struct{ Name string } `json:"frames"`
		} `json:"shared"`
		Profiles []struct {
			Type     string  `json:"type"`
			Unit     string  `json:"unit"`
			EndValue int64   `json:"endValue"`
			Samples  [][]int `json:"samples"`
			Weights  []int64 `json:"weights"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(b.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Schema, "speedscope") || len(doc.Profiles) != 1 {
		t.Fatalf("not a speedscope file: %s", b.String())
	}
	pr := doc.Profiles[0]
	if pr.Type != "sampled" || pr.Unit != "nanoseconds" || pr.EndValue != 10000 || len(pr.Samples) != 2 || len(pr.Weights) != 2 {
		t.Errorf("profile = %+v", pr)
	}
	for _, st := range pr.Samples {
		for _, i := range st {
			if i < 0 || i >= len(doc.Shared.Frames) {
				t.Fatalf("sample references frame %d of %d", i, len(doc.Shared.Frames))
			}
		}
	}
	if doc.Shared.Frames[pr.Samples[0][0]].Name != "main.main" {
		t.Error("speedscope stacks must run root first")
	}
}

func TestSQLBindsParametersAndNeverInterpolates(t *testing.T) {
	q := Query{Service: "x'; DROP TABLE t;--", SampleType: "cpu", From: time.Unix(10, 0), To: time.Unix(20, 0)}
	sql, params := q.SQL()
	if strings.Contains(sql, "DROP") || params["svc"] != q.Service || params["from"] != "10" || params["to"] != "20" {
		t.Errorf("range query must bind its inputs: sql=%q params=%v", sql, params)
	}
	if !strings.Contains(sql, "profile_samples_1m") || !strings.Contains(sql, "profile_stacks") {
		t.Error("a range query reads the aggregate tier")
	}
	tsql, tparams := Query{TraceID: "abc", SampleType: "cpu"}.SQL()
	if !strings.Contains(tsql, "otel_profiles") || tparams["trace"] != "abc" {
		t.Errorf("a trace query reads the raw tier: %q %v", tsql, tparams)
	}
}

// The live heap and the goroutines are gauges: a range of them is each
// instance's newest snapshot, read from the raw tier, never a sum. With the
// default 2h heap cadence, the default 1h window summed over the per-minute
// tier held no heap at all for half of every day.
func TestGaugeTypesReadTheNewestSnapshot(t *testing.T) {
	to := time.Unix(100_000, 0)
	for _, typ := range []string{"inuse_space", "inuse_objects", "goroutine"} {
		if !IsSnapshot(typ) {
			t.Errorf("%s is not read as a snapshot", typ)
		}
		sql, p := Query{Service: "api", SampleType: typ, Env: "prod", From: to.Add(-time.Hour), To: to}.SQL()
		if sql != SnapshotSQL {
			t.Fatalf("%s reads %q", typ, sql)
		}
		if p["since"] != fmt.Sprint(to.Add(-SnapshotLookback).Unix()) || p["width"] != "3600" || p["from"] != "96400" || p["to"] != "100000" || p["env"] != "prod" {
			t.Errorf("%s params = %v", typ, p)
		}
		for name := range p {
			if !strings.Contains(sql, "{"+name+":") {
				t.Errorf("param %s is bound but unused", name)
			}
		}
	}
	for _, want := range []string{
		"FROM default.otel_profiles", "argMax(ProfileId, Timestamp)", "service.instance.id",
		// An instance is its environment and its id: with no Env filter, one
		// instance id reused in two environments keeps each one's newest snapshot.
		"(ResourceAttributes['deployment.environment.name'], ResourceAttributes['service.instance.id'], ProfileId) IN (",
		"GROUP BY environment, instance",
		// The window is the range itself whenever the range holds a snapshot, so
		// an instance whose newest snapshot predates it is not mixed in; only an
		// empty range falls back to the newest snapshot before it.
		"if(max(Timestamp) >= toDateTime({from:UInt32}), toDateTime({from:UInt32}),",
	} {
		if !strings.Contains(SnapshotSQL, want) {
			t.Errorf("SnapshotSQL lacks %q", want)
		}
	}
	for _, typ := range []string{"cpu", "alloc_space", "alloc_objects", "contentions", "delay"} {
		if IsSnapshot(typ) {
			t.Errorf("%s is accumulated over time, not a snapshot", typ)
		}
		if sql, _ := (Query{Service: "api", SampleType: typ, From: to.Add(-time.Hour), To: to}).SQL(); sql != RangeSQL {
			t.Errorf("%s does not read the range", typ)
		}
	}
	if sql, _ := (Query{SampleType: "inuse_space", TraceID: "abc"}).SQL(); sql != TraceSQL {
		t.Error("a trace's heap samples read the trace's rows")
	}
}
