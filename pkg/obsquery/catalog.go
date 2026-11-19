package obsquery

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/observe/metrics"
)

// CatalogKind is the table a metric is stored in. 1-3 equal MetricType's, and
// all five equal the control plane's MetricType wire enum.
type CatalogKind int

// The kinds.
const (
	CatalogGauge        CatalogKind = 1
	CatalogSum          CatalogKind = 2
	CatalogHistogram    CatalogKind = 3
	CatalogExpHistogram CatalogKind = 4
	CatalogSummary      CatalogKind = 5
)

var catalogKinds = []CatalogKind{CatalogGauge, CatalogSum, CatalogHistogram, CatalogExpHistogram, CatalogSummary}

func (k CatalogKind) String() string {
	switch k {
	case CatalogGauge:
		return "gauge"
	case CatalogSum:
		return "sum"
	case CatalogHistogram:
		return "histogram"
	case CatalogExpHistogram:
		return "exp_histogram"
	case CatalogSummary:
		return "summary"
	}
	return "unknown"
}

func (k CatalogKind) table() string {
	switch k {
	case CatalogGauge:
		return "otel_metrics_gauge"
	case CatalogSum:
		return "otel_metrics_sum"
	case CatalogHistogram:
		return "otel_metrics_histogram"
	case CatalogExpHistogram:
		return "otel_metrics_exponential_histogram"
	case CatalogSummary:
		return "otel_metrics_summary"
	}
	return ""
}

// Queryable reports whether the evaluator can run the kind: all five. A
// summary supports a narrower set of aggregations (see ResolveAgg); asking for
// another is refused with the reason.
func (k CatalogKind) Queryable() bool {
	return k.table() != ""
}

// hasTemporality: the kinds whose table stores AggregationTemporality.
func (k CatalogKind) hasTemporality() bool {
	return k == CatalogSum || k == CatalogHistogram || k == CatalogExpHistogram
}

// CatalogWindow is the longest range the catalog and attribute scans read: a
// 30-day scan finds no new names and costs 30 times as much.
const CatalogWindow = 24 * time.Hour

// builtinMetricPrefixes mark forge's and the Go runtime's own metrics.
var builtinMetricPrefixes = []string{"rpc.", "go.", "forge."}

// IsBuiltinMetric reports whether name is a runtime or forge built-in metric
// (the rpc., go. and forge. prefixes) as opposed to an application's own. The
// console mirrors this rule in TypeScript; this is the one Go source to copy.
func IsBuiltinMetric(name string) bool {
	for _, p := range builtinMetricPrefixes {
		if strings.HasPrefix(name, p) && len(name) > len(p) {
			return true
		}
	}
	return false
}

// CatalogRange clamps the scope's range to the last CatalogWindow of it.
func CatalogRange(s Scope) Scope {
	if s.End.Sub(s.Start) > CatalogWindow {
		s.Start = s.End.Add(-CatalogWindow)
	}
	return s
}

// MetricCatalog is a ListMetrics request: every metric with data in the last
// 24 hours of the scope's range, one entry per (name, kind).
type MetricCatalog struct {
	Scope        Scope
	NameContains string
	Limit        int
}

const maxNameContains = 128

// Compile builds the catalog statement over all five metric tables. Only
// RESOURCE-level scope filters narrow it: any other key is valid for one
// metric only, and is checked when that metric is queried. Columns: name,
// kind (UInt8), unit, description, mono (UInt8), temporality (Int32), services
// (Array(String), at most 10), service_count (UInt64), last_seen (DateTime),
// series (UInt64). It fetches Limit+1 rows so ParseCatalog can tell the list
// was cut.
func (q MetricCatalog) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeMetrics); err != nil {
		return Stmt{}, err
	}
	if q.Limit <= 0 || q.Limit > ListMetricsMax {
		return Stmt{}, invalid("limit must be 1-%d", ListMetricsMax)
	}
	if len(q.NameContains) > maxNameContains {
		return Stmt{}, invalid("name_contains is longer than %d characters", maxNameContains)
	}
	sc := CatalogRange(q.Scope)
	b := newBuilder(sc)
	res, err := b.resourceConds(SigMetrics, sc)
	if err != nil {
		return Stmt{}, err
	}
	conds := append([]string{b.rangeCondSec("TimeUnix", sc)}, res...)
	if q.NameContains != "" {
		conds = append(conds, "positionCaseInsensitive(MetricName, "+b.str(q.NameContains)+") > 0")
	}
	w := where(conds)
	parts := make([]string, 0, len(catalogKinds))
	for _, k := range catalogKinds {
		mono, temp := "toUInt8(0)", "toInt32(0)"
		if k == CatalogSum {
			mono = "toUInt8(max(IsMonotonic))"
		}
		if k.hasTemporality() {
			temp = "toInt32(max(AggregationTemporality))"
		}
		parts = append(parts, "SELECT MetricName, toUInt8("+strconv.Itoa(int(k))+") AS kind, any(MetricUnit) AS unit, any(MetricDescription) AS descr, "+
			mono+" AS mono, "+temp+" AS temporality, groupUniqArray(10)(ServiceName) AS services, uniq(ServiceName) AS service_count, "+
			"max(TimeUnix) AS last_seen, uniqCombined(cityHash64(ResourceAttributes, Attributes)) AS series\n"+
			"FROM "+b.table(sc, k.table())+"\n"+w+"\nGROUP BY MetricName")
	}
	return b.stmt("SELECT * FROM (\n" + strings.Join(parts, "\nUNION ALL\n") + "\n) ORDER BY MetricName, kind LIMIT " + strconv.Itoa(q.Limit+1)), nil
}

// CatalogEntry is one metric as the explorer lists it.
type CatalogEntry struct {
	Name        string
	Kind        CatalogKind
	Unit        string
	Description string
	// Monotonic: a sum that is a counter, not an up-down counter.
	Monotonic   bool
	Temporality Temporality
	// Services are up to 10 service.name values that emit it; ServiceCount is
	// how many do.
	Services     []string
	ServiceCount int
	// LastSeen is the newest data point in the catalog range; zero for a
	// declared metric never seen.
	LastSeen       time.Time
	SeriesEstimate int64
	// Queryable is false for a kind the evaluator cannot run.
	Queryable bool
	Builtin   bool
	// Declared: the release's metrics schema lists it. Seen: it has data in
	// the range.
	Declared bool
	Seen     bool
	// Stability is the declaration's "stable", "experimental" or
	// "deprecated"; empty when not declared.
	Stability          string
	DeclaredAttributes []metrics.Attribute
}

// ParseCatalog reads MetricCatalog's rows. limit is the request's Limit: a
// row beyond it reports truncated instead of being returned. The slice is
// never nil. The caller closes rows.
func ParseCatalog(rows Rows, limit int) (entries []CatalogEntry, truncated bool, err error) {
	entries = []CatalogEntry{}
	for rows.Next() {
		var (
			name, unit, desc string
			kind, mono       uint8
			temp             int32
			svcs             []string
			nsvc, series     uint64
			last             time.Time
		)
		if err := rows.Scan(&name, &kind, &unit, &desc, &mono, &temp, &svcs, &nsvc, &last, &series); err != nil {
			return nil, false, err
		}
		if len(entries) == limit {
			return entries, true, nil
		}
		k := CatalogKind(kind)
		t := Temporality(temp)
		if t != TemporalityDelta && t != TemporalityCumulative {
			t = TemporalityUnknown
		}
		sorted := append([]string{}, svcs...)
		sort.Strings(sorted)
		entries = append(entries, CatalogEntry{
			Name: name, Kind: k, Unit: unit, Description: desc, Monotonic: mono != 0, Temporality: t,
			Services: sorted, ServiceCount: int(nsvc), LastSeen: last, SeriesEstimate: int64(series),
			Queryable: k.Queryable(), Builtin: IsBuiltinMetric(name), Seen: true,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return entries, false, nil
}

// MergeDeclared joins the catalog with the release's declared metrics schemas.
// It is pure and never modifies its inputs.
//
//   - declared and seen: the declaration's description and stability, its
//     attributes and Declared = true; the observed unit, kind and numbers stay,
//     because they describe the data actually stored;
//   - declared, not seen: listed with Seen = false, so a typo'd or never-called
//     metric is visible;
//   - seen, not declared: unchanged (infrastructure and third-party metrics);
//   - deprecated declarations are kept and flagged through Stability.
//
// A metric declared by several schemas takes the first. The result is ordered
// by name, then kind.
func MergeDeclared(observed []CatalogEntry, declared []metrics.Schema) []CatalogEntry {
	decl := map[string]metrics.Spec{}
	var order []string
	for _, sch := range declared {
		for _, sp := range sch.Metrics {
			if _, dup := decl[sp.Name]; !dup {
				decl[sp.Name] = sp
				order = append(order, sp.Name)
			}
		}
	}
	out := make([]CatalogEntry, 0, len(observed)+len(order))
	seen := map[string]bool{}
	for _, e := range observed {
		e.Services = append([]string(nil), e.Services...)
		e.Builtin = IsBuiltinMetric(e.Name)
		if sp, ok := decl[e.Name]; ok {
			seen[e.Name] = true
			e.Declared = true
			if sp.Description != "" {
				e.Description = sp.Description
			}
			e.Stability = string(sp.Stability)
			e.DeclaredAttributes = copyAttributes(sp.Attributes)
		}
		out = append(out, e)
	}
	for _, name := range order {
		if seen[name] {
			continue
		}
		sp := decl[name]
		k, mono := kindOfDeclared(sp.Type)
		out = append(out, CatalogEntry{
			Name: name, Kind: k, Unit: sp.Unit, Description: sp.Description, Monotonic: mono,
			Queryable: k.Queryable(), Builtin: IsBuiltinMetric(name), Declared: true,
			Stability: string(sp.Stability), DeclaredAttributes: copyAttributes(sp.Attributes),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

func kindOfDeclared(t metrics.Type) (k CatalogKind, monotonic bool) {
	switch t {
	case metrics.Counter:
		return CatalogSum, true
	case metrics.UpDownCounter:
		return CatalogSum, false
	case metrics.Histogram:
		return CatalogHistogram, false
	}
	return CatalogGauge, false
}

func copyAttributes(in []metrics.Attribute) []metrics.Attribute {
	if len(in) == 0 {
		return nil
	}
	out := make([]metrics.Attribute, len(in))
	for i, a := range in {
		out[i] = metrics.Attribute{Key: a.Key, Values: append([]string(nil), a.Values...)}
	}
	return out
}

// MetricAttributes is a GetMetric request's attribute scan: every attribute key
// a metric's data points and its resources carry, with an estimate of how many
// values each takes.
type MetricAttributes struct {
	Scope Scope
	Name  string
	Kind  CatalogKind
}

// Compile builds the attribute statement over the last CatalogWindow of the
// scope's range. Columns: key (String), resource (UInt8: 1 for a resource
// attribute), distinct (UInt64, uniqCombined). Resource keys come back only
// when they vary. At most MaxDeclaredKeys rows: data-point keys first, then
// the most varied.
func (q MetricAttributes) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeMetrics); err != nil {
		return Stmt{}, err
	}
	if q.Name == "" || len(q.Name) > 256 {
		return Stmt{}, invalid("metric_name must be 1-256 characters")
	}
	if q.Kind.table() == "" {
		return Stmt{}, invalid("unknown metric kind")
	}
	sc := CatalogRange(q.Scope)
	b := newBuilder(sc)
	res, err := b.resourceConds(SigMetrics, sc)
	if err != nil {
		return Stmt{}, err
	}
	conds := append([]string{b.rangeCondSec("TimeUnix", sc), "MetricName = " + b.str(q.Name)}, res...)
	from, w := b.table(sc, q.Kind.table()), where(conds)
	dp := "SELECT k, toUInt8(0) AS resource, uniqCombined(v) AS distinct_values FROM " + from +
		"\nARRAY JOIN mapKeys(Attributes) AS k, mapValues(Attributes) AS v\n" + w + "\nGROUP BY k"
	rs := "SELECT k, toUInt8(1) AS resource, uniqCombined(v) AS distinct_values FROM " + from +
		"\nARRAY JOIN mapKeys(ResourceAttributes) AS k, mapValues(ResourceAttributes) AS v\n" + w + "\nGROUP BY k HAVING distinct_values > 1"
	return b.stmt("SELECT k, resource, distinct_values FROM (\n" + dp + "\nUNION ALL\n" + rs +
		"\n) ORDER BY resource, distinct_values DESC, k LIMIT " + strconv.Itoa(MaxDeclaredKeys)), nil
}

// maxBreakdownDistinct: an open key with more distinct values than this is
// not a useful breakdown.
const maxBreakdownDistinct = 50

// AttributeStat is one attribute key of a metric.
type AttributeStat struct {
	Key      string
	Resource bool
	// DistinctEstimate is the estimated number of values over the range.
	DistinctEstimate int64
	// Enumerated is the declared closed value set; empty when open.
	Enumerated []string
	// HighCardinality: grouping by the key (with the rest of the panel's
	// group-by) could exceed MaxMetricSeries.
	HighCardinality bool
}

// ParseAttributes reads MetricAttributes' rows into at most MaxDeclaredKeys
// stats, the most useful breakdown first: enumerated keys, then keys with 2 to
// 50 distinct values, then keys with one value, then the rest. declared (may be
// nil) supplies the closed value sets. otherGroupCard is the product of the
// distinct counts of the panel's other group-by keys (0 or 1 for none).
func ParseAttributes(rows Rows, declared *metrics.Spec, otherGroupCard int64) ([]AttributeStat, error) {
	enum := map[string][]string{}
	if declared != nil {
		for _, a := range declared.Attributes {
			if len(a.Values) > 0 {
				enum[a.Key] = append([]string(nil), a.Values...)
			}
		}
	}
	if otherGroupCard < 1 {
		otherGroupCard = 1
	}
	out := []AttributeStat{}
	for rows.Next() {
		var key string
		var res uint8
		var d uint64
		if err := rows.Scan(&key, &res, &d); err != nil {
			return nil, err
		}
		if len(out) == MaxDeclaredKeys {
			break
		}
		s := AttributeStat{Key: key, Resource: res != 0, DistinctEstimate: int64(d)}
		if res == 0 {
			s.Enumerated = enum[key]
		}
		s.HighCardinality = float64(d)*float64(otherGroupCard) > MaxMetricSeries ||
			(d > maxBreakdownDistinct && len(s.Enumerated) == 0)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rank := func(s AttributeStat) int {
		switch {
		case len(s.Enumerated) > 0:
			return 0
		case s.DistinctEstimate >= 2 && s.DistinctEstimate <= maxBreakdownDistinct:
			return 1
		case s.DistinctEstimate < 2:
			return 2
		}
		return 3
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := rank(out[i]), rank(out[j])
		if ri != rj {
			return ri < rj
		}
		if ri <= 1 && out[i].DistinctEstimate != out[j].DistinctEstimate {
			return out[i].DistinctEstimate > out[j].DistinctEstimate
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

// SuggestedBreakdowns lists the keys of ParseAttributes' result worth grouping
// by, best first: enumerated keys, then keys with 2 to 50 distinct values,
// leaving out any that could exceed the series cap.
func SuggestedBreakdowns(stats []AttributeStat) []string {
	out := []string{}
	for _, s := range stats {
		if s.HighCardinality {
			continue
		}
		if len(s.Enumerated) > 0 || (s.DistinctEstimate >= 2 && s.DistinctEstimate <= maxBreakdownDistinct) {
			out = append(out, s.Key)
		}
	}
	return out
}

// DeclaredKeys lists the attribute keys a metric's own data points carry,
// the extension to the key allowlist for metric filters and group-bys. It is
// scoped by environment and service only (the filters are what it enables).
// Column: key.
type DeclaredKeys struct {
	Scope Scope
	Name  string
	Type  MetricType
}

// MaxDeclaredKeys bounds the keys one metric may declare.
const MaxDeclaredKeys = 200

// Compile builds the declared-keys statement.
func (q DeclaredKeys) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeMetrics); err != nil {
		return Stmt{}, err
	}
	if q.Type.table() == "" {
		return Stmt{}, invalid("unknown metric type")
	}
	b := newBuilder(q.Scope)
	conds := append([]string{b.rangeCondSec("TimeUnix", q.Scope), "MetricName = " + b.str(q.Name)}, b.scopeBase(q.Scope)...)
	return b.stmt("SELECT arrayJoin(mapKeys(Attributes)) AS k FROM " + b.table(q.Scope, q.Type.table()) + " " + where(conds) +
		" GROUP BY k ORDER BY k LIMIT " + strconv.Itoa(MaxDeclaredKeys)), nil
}
