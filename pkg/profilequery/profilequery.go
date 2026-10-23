// Package profilequery reads continuous profiles back out of ClickHouse and
// writes them in formats other tools open.
//
// It is the reusable half of `forge obs profile`: nothing here shells out or
// touches a terminal. Stacks come from the tables docker-compose.yml's
// `profiles-schema` job creates (see profiles-schema.sql in a scaffolded
// project); Stack is the interchange value, and Pprof, Speedscope and Folded
// write it for `go tool pprof`, speedscope.app (and an embedded speedscope view)
// and flamegraph.pl respectively.
package profilequery

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/google/pprof/profile"
)

// Stack is one distinct call stack and the value it accumulated in the queried
// range. Frames run ROOT to LEAF, the order flamegraph tools expect.
type Stack struct {
	Frames []string
	Value  int64
}

// Profile is an aggregated profile: the stacks of one service and sample type
// over a time range, or of one trace.
type Profile struct {
	Service    string
	SampleType string // cpu, alloc_space, inuse_space, goroutine, ...
	Unit       string // nanoseconds, bytes, count
	From, To   time.Time
	Stacks     []Stack
}

// Total is the sum of every stack's value.
func (p Profile) Total() int64 {
	var n int64
	for _, s := range p.Stacks {
		n += s.Value
	}
	return n
}

// Query selects what to read. SQL turns it into a statement with named
// parameters; callers pass Params to the client, never string-concatenate.
type Query struct {
	Service    string
	SampleType string
	// Env filters on deployment.environment.name when non-empty (range queries only).
	Env      string
	From, To time.Time
	// TraceID, when set, reads the RAW tier for that trace instead of the
	// per-minute aggregate: the samples whose goroutine was serving the trace's
	// spans. Raw rows live 72h; the aggregate 30d.
	TraceID string
	// SpanID narrows a trace query to one span.
	SpanID string
}

// RangeSQL is the aggregated-flamegraph statement: stacks of one service and
// sample type between {from} and {to}, from the per-minute table joined to the
// stack dictionary. Output columns: frames (root to leaf), unit, value.
const RangeSQL = `SELECT arrayReverse(s.FunctionNames) AS frames, any(a.SampleUnit) AS unit, sum(a.Value) AS value
FROM default.profile_samples_1m AS a
INNER JOIN (SELECT ServiceName, StackHash, FunctionNames FROM default.profile_stacks FINAL WHERE ServiceName = {svc:String}) AS s
    ON a.ServiceName = s.ServiceName AND a.StackHash = s.StackHash
WHERE a.ServiceName = {svc:String}
  AND a.SampleType = {type:String}
  AND a.Minute >= toDateTime({from:UInt32}) AND a.Minute < toDateTime({to:UInt32})
  AND ({env:String} = '' OR a.Env = {env:String})
GROUP BY frames
HAVING value > 0
ORDER BY value DESC
FORMAT JSONEachRow`

// TraceSQL reads the raw tier for the samples linked to a trace (and optionally
// a span). Output columns match RangeSQL.
const TraceSQL = `SELECT arrayReverse(FunctionNames) AS frames, any(SampleUnit) AS unit, sum(arraySum(Values)) AS value
FROM default.otel_profiles
WHERE TraceId = {trace:String}
  AND ({span:String} = '' OR SpanId = {span:String})
  AND ({svc:String} = '' OR ServiceName = {svc:String})
  AND SampleType = {type:String}
GROUP BY frames
HAVING value > 0
ORDER BY value DESC
FORMAT JSONEachRow`

// ServicesSQL lists the services that sent profiles in a range, for when the
// caller did not name one.
const ServicesSQL = `SELECT ServiceName AS service, count() AS rows
FROM default.profile_samples_1m
WHERE Minute >= toDateTime({from:UInt32}) AND Minute < toDateTime({to:UInt32})
GROUP BY service ORDER BY rows DESC
FORMAT JSONEachRow`

// SQL returns the statement for q and the parameter values it binds.
func (q Query) SQL() (string, map[string]string) {
	if q.TraceID != "" {
		return TraceSQL, map[string]string{"trace": q.TraceID, "span": q.SpanID, "svc": q.Service, "type": q.SampleType}
	}
	return RangeSQL, map[string]string{
		"svc": q.Service, "type": q.SampleType, "env": q.Env,
		"from": fmt.Sprint(q.From.Unix()), "to": fmt.Sprint(q.To.Unix()),
	}
}

// Parse reads the JSONEachRow output of RangeSQL or TraceSQL.
func Parse(r io.Reader, q Query) (Profile, error) {
	p := Profile{Service: q.Service, SampleType: q.SampleType, From: q.From, To: q.To}
	dec := json.NewDecoder(r)
	for dec.More() {
		var row struct {
			Frames []string    `json:"frames"`
			Unit   string      `json:"unit"`
			Value  json.Number `json:"value"`
		}
		if err := dec.Decode(&row); err != nil {
			return Profile{}, fmt.Errorf("decode ClickHouse row: %w", err)
		}
		v, err := row.Value.Int64()
		if err != nil {
			f, ferr := row.Value.Float64()
			if ferr != nil {
				return Profile{}, fmt.Errorf("decode value %q: %w", row.Value, err)
			}
			v = int64(f)
		}
		if len(row.Frames) == 0 || v <= 0 {
			continue
		}
		if p.Unit == "" {
			p.Unit = row.Unit
		}
		p.Stacks = append(p.Stacks, Stack{Frames: row.Frames, Value: v})
	}
	if p.Unit == "" {
		p.Unit = defaultUnit(q.SampleType)
	}
	sort.SliceStable(p.Stacks, func(i, j int) bool { return p.Stacks[i].Value > p.Stacks[j].Value })
	return p, nil
}

func defaultUnit(sampleType string) string {
	switch {
	case sampleType == "cpu":
		return "nanoseconds"
	case strings.HasSuffix(sampleType, "_space"):
		return "bytes"
	}
	return "count"
}

// Folded writes the collapsed-stack format: "root;child;leaf value" per line.
// flamegraph.pl, speedscope and d3-flamegraph all read it.
func (p Profile) Folded(w io.Writer) error {
	for _, s := range p.Stacks {
		if _, err := fmt.Fprintf(w, "%s %d\n", strings.Join(s.Frames, ";"), s.Value); err != nil {
			return err
		}
	}
	return nil
}

// Pprof writes a gzip-compressed pprof profile that `go tool pprof` opens.
// Only function names survive the trip through ClickHouse's stack dictionary,
// so there are no line numbers or addresses; -top, -tree, -peek and -http
// flamegraphs all work.
func (p Profile) Pprof(w io.Writer) error {
	pp := &profile.Profile{
		SampleType: []*profile.ValueType{{Type: p.SampleType, Unit: p.Unit}},
		PeriodType: &profile.ValueType{Type: p.SampleType, Unit: p.Unit},
		Period:     1,
		TimeNanos:  p.From.UnixNano(),
	}
	if !p.To.IsZero() && p.To.After(p.From) {
		pp.DurationNanos = p.To.Sub(p.From).Nanoseconds()
	}
	pp.Comments = []string{fmt.Sprintf("forge obs profile: service=%s type=%s", p.Service, p.SampleType)}

	funcs := map[string]*profile.Function{}
	locs := map[string]*profile.Location{}
	loc := func(name string) *profile.Location {
		if l, ok := locs[name]; ok {
			return l
		}
		f := &profile.Function{ID: uint64(len(pp.Function) + 1), Name: name, SystemName: name}
		pp.Function = append(pp.Function, f)
		funcs[name] = f
		l := &profile.Location{ID: uint64(len(pp.Location) + 1), Line: []profile.Line{{Function: f}}}
		pp.Location = append(pp.Location, l)
		locs[name] = l
		return l
	}
	for _, s := range p.Stacks {
		smp := &profile.Sample{Value: []int64{s.Value}}
		for i := len(s.Frames) - 1; i >= 0; i-- { // pprof wants leaf first
			smp.Location = append(smp.Location, loc(s.Frames[i]))
		}
		pp.Sample = append(pp.Sample, smp)
	}
	if err := pp.CheckValid(); err != nil {
		return fmt.Errorf("build pprof: %w", err)
	}
	zw := gzip.NewWriter(w)
	if err := pp.WriteUncompressed(zw); err != nil {
		return err
	}
	return zw.Close()
}

// Speedscope writes speedscope's file format (a single "sampled" profile), the
// shape an embedded speedscope view loads directly.
func (p Profile) Speedscope(w io.Writer) error {
	type frame struct {
		Name string `json:"name"`
	}
	type sampled struct {
		Type       string  `json:"type"`
		Name       string  `json:"name"`
		Unit       string  `json:"unit"`
		StartValue int64   `json:"startValue"`
		EndValue   int64   `json:"endValue"`
		Samples    [][]int `json:"samples"`
		Weights    []int64 `json:"weights"`
	}
	frames := []frame{}
	idx := map[string]int{}
	prof := sampled{Type: "sampled", Name: fmt.Sprintf("%s %s", p.Service, p.SampleType), Unit: speedscopeUnit(p.Unit),
		Samples: [][]int{}, Weights: []int64{}}
	for _, s := range p.Stacks {
		st := make([]int, len(s.Frames))
		for i, f := range s.Frames {
			n, ok := idx[f]
			if !ok {
				n = len(frames)
				idx[f] = n
				frames = append(frames, frame{Name: f})
			}
			st[i] = n
		}
		prof.Samples = append(prof.Samples, st)
		prof.Weights = append(prof.Weights, s.Value)
		prof.EndValue += s.Value
	}
	doc := map[string]any{
		"$schema":            "https://www.speedscope.app/file-format-schema.json",
		"name":               prof.Name,
		"exporter":           "forge obs profile",
		"activeProfileIndex": 0,
		"shared":             map[string]any{"frames": frames},
		"profiles":           []sampled{prof},
	}
	enc := json.NewEncoder(w)
	return enc.Encode(doc)
}

func speedscopeUnit(u string) string {
	switch u {
	case "nanoseconds", "microseconds", "milliseconds", "seconds", "bytes":
		return u
	}
	return "none"
}
