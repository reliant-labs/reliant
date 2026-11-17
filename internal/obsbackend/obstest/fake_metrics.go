package obstest

// FakeMetrics is an obsbackend.Runner that answers the statements a metrics
// read compiles (catalog, lookup, attributes, declared keys, series, exemplars)
// from canned rows, so the real obsquery evaluator runs without a ClickHouse.
// Rows are in the cells ClickHouse's JSONCompact decodes to: json.Number,
// string, []any.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// FakeAttr is one attribute key of a fake metric.
type FakeAttr struct {
	Key      string
	Resource bool
	Distinct int
}

// FakePoint is one row of a series statement: the bucket, the group's label
// values (in group-by order) and the value.
type FakePoint struct {
	Bucket time.Time
	Labels []string
	Value  float64
}

// FakeExemplar is one exemplar row.
type FakeExemplar struct {
	Time    time.Time
	Value   float64
	TraceID string
	SpanID  string
	Attrs   map[string]string
}

// FakeMetric is one metric stored as one kind.
type FakeMetric struct {
	Kind         obsquery.CatalogKind
	Unit         string
	Description  string
	Monotonic    bool
	Temporality  int
	Services     []string
	Series       int
	IntervalSec  int
	Attrs        []FakeAttr
	DeclaredKeys []string
	// Points answer the series statement of a line panel; Reduced answer a
	// reduction (a statement compiled with Reduce).
	Points  []FakePoint
	Reduced []FakePoint
	// Total is the series_total column; 0 is the number of distinct groups.
	Total     int
	Exemplars []FakeExemplar
	LastSeen  time.Time
}

// FakeMetrics is the Runner. Safe for concurrent use.
type FakeMetrics struct {
	Metrics map[string]FakeMetric
	// StoredTraces are the trace ids the stored-trace probe finds.
	StoredTraces []string
	// Err fails every statement.
	Err error

	mu  sync.Mutex
	ran []obsquery.Stmt
}

// Ran is every statement that reached the runner, in order.
func (f *FakeMetrics) Ran() []obsquery.Stmt {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]obsquery.Stmt(nil), f.ran...)
}

var nameContainsRe = regexp.MustCompile(`positionCaseInsensitive\(MetricName, \{(p\d+):String\}\)`)

func num(v any) json.Number { return json.Number(fmt.Sprint(v)) }

func chTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }

func strs(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// metricOf is the metric a statement names, by its bound parameters.
func (f *FakeMetrics) metricOf(st obsquery.Stmt) (string, FakeMetric, bool) {
	for _, v := range st.Params {
		if m, ok := f.Metrics[v]; ok {
			return v, m, true
		}
	}
	return "", FakeMetric{}, false
}

// Run implements obsbackend.Runner.
func (f *FakeMetrics) Run(_ context.Context, st obsquery.Stmt, _ obsbackend.Limits) (obsbackend.Result, error) {
	f.mu.Lock()
	f.ran = append(f.ran, st)
	f.mu.Unlock()
	if f.Err != nil {
		return obsbackend.Result{}, f.Err
	}
	sql := st.SQL
	switch {
	case strings.Contains(sql, "AS last_seen"):
		return f.catalog(st), nil
	case strings.Contains(sql, "AS interval"):
		name, m, ok := f.metricOf(st)
		if !ok {
			return obsbackend.Result{}, nil
		}
		_ = name
		interval := m.IntervalSec
		if interval == 0 {
			interval = 60
		}
		series := m.Series
		if series == 0 {
			series = 1
		}
		return obsbackend.Result{Rows: [][]any{{num(int(m.Kind)), m.Unit, num(b2i(m.Monotonic)), num(m.Temporality), num(0), num(series), num(100),
			chTime(time.Unix(0, 0)), chTime(time.Unix(0, 0)), num(interval)}}}, nil
	case strings.Contains(sql, "distinct_values"):
		_, m, ok := f.metricOf(st)
		if !ok {
			return obsbackend.Result{}, nil
		}
		var rows [][]any
		for _, a := range m.Attrs {
			rows = append(rows, []any{a.Key, num(b2i(a.Resource)), num(a.Distinct)})
		}
		return obsbackend.Result{Rows: rows}, nil
	case strings.Contains(sql, "arrayJoin(mapKeys(Attributes))"):
		_, m, ok := f.metricOf(st)
		if !ok {
			return obsbackend.Result{}, nil
		}
		var rows [][]any
		for _, k := range m.DeclaredKeys {
			rows = append(rows, []any{k})
		}
		return obsbackend.Result{Rows: rows}, nil
	case strings.Contains(sql, "`Exemplars.TimeUnix`"):
		_, m, ok := f.metricOf(st)
		if !ok {
			return obsbackend.Result{}, nil
		}
		var rows [][]any
		for _, e := range m.Exemplars {
			attrs := map[string]any{}
			for k, v := range e.Attrs {
				attrs[k] = v
			}
			rows = append(rows, []any{chTime(e.Time), num(e.Value), e.TraceID, e.SpanID, attrs})
		}
		return obsbackend.Result{Rows: rows}, nil
	case strings.Contains(sql, "SELECT DISTINCT TraceId"):
		var rows [][]any
		for _, id := range f.StoredTraces {
			rows = append(rows, []any{id})
		}
		return obsbackend.Result{Rows: rows}, nil
	}
	_, m, ok := f.metricOf(st)
	if !ok {
		return obsbackend.Result{}, nil
	}
	pts := m.Points
	if len(m.Reduced) > 0 && !strings.Contains(sql, "toStartOfInterval(") {
		pts = m.Reduced
	}
	total := m.Total
	if total == 0 {
		seen := map[string]bool{}
		for _, p := range pts {
			seen[strings.Join(p.Labels, "\x00")] = true
		}
		total = len(seen)
	}
	rows := make([][]any, len(pts))
	for i, p := range pts {
		rows[i] = []any{chTime(p.Bucket), strs(p.Labels), num(p.Value), num(total)}
	}
	return obsbackend.Result{Rows: rows}, nil
}

func (f *FakeMetrics) catalog(st obsquery.Stmt) obsbackend.Result {
	contains := ""
	if m := nameContainsRe.FindStringSubmatch(st.SQL); m != nil {
		contains = strings.ToLower(st.Params[m[1]])
	}
	var rows [][]any
	for _, name := range sortedNames(f.Metrics) {
		m := f.Metrics[name]
		if contains != "" && !strings.Contains(strings.ToLower(name), contains) {
			continue
		}
		last := m.LastSeen
		series := m.Series
		if series == 0 {
			series = 1
		}
		rows = append(rows, []any{name, num(int(m.Kind)), m.Unit, m.Description, num(b2i(m.Monotonic)), num(m.Temporality),
			strs(m.Services), num(len(m.Services)), chTime(last), num(series)})
	}
	return obsbackend.Result{Rows: rows}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func sortedNames(m map[string]FakeMetric) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
