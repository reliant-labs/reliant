package obsquery

import (
	"math"
	"strconv"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// Release probes: the control plane's prober sends steady synthetic requests
// to the targets an env declares (forge.Probe), and release health judges a
// release on them when real traffic is too thin. Two populations follow, and
// they are never mixed:
//
//   - probe: the prober's OWN measurement, client-side, as delta metrics
//     (release.ProbeMetricRequests, release.ProbeMetricDuration; resource
//     service.name = the target, reliant.source = release.ProbeSource). Only
//     the client sees a timeout, a TLS or DNS failure, or a 5xx the edge served
//     without reaching the workload, and a static site has no spans at all.
//     ProbeRED, ProbeREDByPath and ProbeFailures read it.
//   - real: everything else. A workload stamps release.ProbeAttr (true) on the
//     server span and the log records of a request the platform edge verified
//     as a probe. SpanNotProbeSQL and LogNotProbeSQL keep them out of every
//     real-traffic figure this package computes: RED, REDByEndpoint, Trend, the
//     overview log counts and the server issues. A row that predates the
//     attribute reads as real. Trace and log SEARCH still finds probe rows
//     (filter span.attr.forge.probe to narrow), since a probe's trace is how a
//     probe failure is debugged.

// The real-traffic filters: a ClickHouse boolean expression over one row,
// safe to splice into a statement (none takes a parameter). An otel_traces
// statement over request spans ANDs SpanNotProbeSQL; an otel_logs statement
// over error records ANDs LogNotProbeSQL.
const (
	// SpanNotProbeSQL keeps the spans of real requests: those a workload did
	// not mark as a platform release probe.
	SpanNotProbeSQL = "SpanAttributes['" + release.ProbeAttr + "'] != 'true'"
	// LogNotProbeSQL keeps the log records of real requests.
	LogNotProbeSQL = "LogAttributes['" + release.ProbeAttr + "'] != 'true'"
)

// histogramMidSQL places each bucket of an OTel explicit-bounds histogram row
// at its midpoint (the last bound for the overflow bucket), the value
// quantilesExactWeighted weighs by the bucket's count. IngressRED and the
// probe statements read their histograms (one bucket layout,
// proxy.IngressBuckets) with it, so their quantiles compare.
const histogramMidSQL = "arrayMap(i -> if(i > length(ExplicitBounds), ExplicitBounds[length(ExplicitBounds)], " +
	"(if(i = 1, 0, ExplicitBounds[i - 1]) + ExplicitBounds[i]) / 2), arrayEnumerate(BucketCounts))"

// The probe outcome and attribute columns, as SQL over a probe metric row.
const (
	probePathCol    = "Attributes['" + release.ProbeAttrPath + "']"
	probeOutcomeCol = "Attributes['" + release.ProbeAttrOutcome + "']"
	probeTimedExpr  = probeOutcomeCol + " IN ('" + release.ProbeOutcomeOK + "', '" + release.ProbeOutcomeFailed + "')"
)

// MaxProbeREDRows bounds ProbeRED's and ProbeREDByPath's rows. The control
// plane caps an org at 25 targets of at most 5 paths each, far below it.
const MaxProbeREDRows = 200

// MaxProbeFailures is how many failure groups ProbeFailures returns for one
// target, most failures first: enough to name what broke in a finding.
const MaxProbeFailures = 5

// ProbeRED builds the per-target statement over the prober's metrics.
// Columns: ServiceName (the target), ok, failed, throttled, degraded (request
// counts by outcome, from release.ProbeMetricRequests), q (p50,p95,p99 in ms
// of the ok and failed requests, from release.ProbeMetricDuration, estimated
// as IngressRED estimates its quantiles; [] when none was timed), source.
// A target with no probe request in the window has no row. GroupBy is
// refused: the prober's metrics carry no span attributes to split by. Parse
// it with ScanProbeRED.
func (q Overview) ProbeRED() (Stmt, error) { return q.probeRED(false) }

// ProbeREDByPath is ProbeRED split by the path probed. Columns: ServiceName,
// path, then ProbeRED's. Parse it with ScanProbeREDByPath.
func (q Overview) ProbeREDByPath() (Stmt, error) { return q.probeRED(true) }

func (q Overview) probeRED(byPath bool) (Stmt, error) {
	if err := q.Scope.validate(MaxRangeOverview); err != nil {
		return Stmt{}, err
	}
	if len(q.GroupBy) > 0 {
		return Stmt{}, invalid("probe RED takes no group_by")
	}
	b := newBuilder(q.Scope)
	sc, err := b.scopeConds(SigMetrics, q.Scope)
	if err != nil {
		return Stmt{}, err
	}
	source := b.str(release.ProbeSource)
	counted := append([]string{
		b.rangeCondSec("TimeUnix", q.Scope),
		"MetricName = " + b.str(release.ProbeMetricRequests),
		"ResourceAttributes['reliant.source'] = " + source,
	}, sc...)
	timed := append([]string{
		b.rangeCondSec("TimeUnix", q.Scope),
		"MetricName = " + b.str(release.ProbeMetricDuration),
		"ResourceAttributes['reliant.source'] = " + source,
		probeTimedExpr,
		"length(ExplicitBounds) > 0",
		"length(BucketCounts) = length(ExplicitBounds) + 1",
	}, sc...)
	keySel, keys, outKeys, join, limit := "", "ServiceName", "c.ServiceName AS ServiceName", "h.ServiceName = c.ServiceName", MaxProbeREDRows
	if byPath {
		keySel, keys, outKeys, join = ", "+probePathCol+" AS path", "ServiceName, path", "c.ServiceName AS ServiceName, c.path AS path", join+" AND h.path = c.path"
	}
	outcome := func(o string) string {
		return "toUInt64(sumIf(Value, " + probeOutcomeCol + " = '" + o + "')) AS " + o
	}
	return b.stmt("SELECT " + outKeys + ", c.ok AS ok, c.failed AS failed, c.throttled AS throttled, c.degraded AS degraded, ifNull(h.q, CAST([], 'Array(Float64)')) AS q, " + source + " AS source\n" +
		"FROM (\n" +
		"  SELECT ServiceName" + keySel + ", " + outcome(release.ProbeOutcomeOK) + ", " + outcome(release.ProbeOutcomeFailed) + ",\n" +
		"         " + outcome(release.ProbeOutcomeThrottled) + ", " + outcome(release.ProbeOutcomeDegraded) + "\n" +
		"  FROM " + b.table(q.Scope, "otel_metrics_sum") + "\n  " + where(counted) + "\n" +
		"  GROUP BY " + keys + "\n" +
		") AS c\n" +
		"LEFT JOIN (\n" +
		"  SELECT " + keys + ", quantilesExactWeighted(0.5, 0.95, 0.99)(tupleElement(bucket, 1) * 1000, tupleElement(bucket, 2)) AS q\n" +
		"  FROM (\n" +
		"    SELECT ServiceName" + keySel + ", arrayZip(" + histogramMidSQL + ", BucketCounts) AS buckets\n" +
		"    FROM " + b.table(q.Scope, "otel_metrics_histogram") + "\n    " + where(timed) + "\n" +
		"  )\n  ARRAY JOIN buckets AS bucket\n" +
		"  WHERE tupleElement(bucket, 2) > 0\n" +
		"  GROUP BY " + keys + "\n" +
		") AS h ON " + join + "\n" +
		"WHERE c.ok + c.failed + c.throttled + c.degraded > 0\n" +
		"ORDER BY " + keys + "\nLIMIT " + strconv.Itoa(limit)), nil
}

// ProbeFailures builds the statement naming what failed: the failed probe
// requests grouped by target, path, status class (empty when nothing
// answered) and error type (empty when something did), with when each group
// was first and last flushed. Degraded and throttled requests are not
// failures and are not listed. Columns: ServiceName, path, status_class,
// error_type, failures, first, last. At most MaxProbeFailures groups per
// target, most failures first. GroupBy is refused. Parse it with
// ScanProbeFailures.
func (q Overview) ProbeFailures() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeOverview); err != nil {
		return Stmt{}, err
	}
	if len(q.GroupBy) > 0 {
		return Stmt{}, invalid("probe failures take no group_by")
	}
	b := newBuilder(q.Scope)
	sc, err := b.scopeConds(SigMetrics, q.Scope)
	if err != nil {
		return Stmt{}, err
	}
	conds := append([]string{
		b.rangeCondSec("TimeUnix", q.Scope),
		"MetricName = " + b.str(release.ProbeMetricRequests),
		"ResourceAttributes['reliant.source'] = " + b.str(release.ProbeSource),
		probeOutcomeCol + " = '" + release.ProbeOutcomeFailed + "'",
	}, sc...)
	return b.stmt("SELECT ServiceName, " + probePathCol + " AS path, Attributes['" + release.ProbeAttrStatusClass + "'] AS status_class,\n" +
		"       Attributes['" + release.ProbeAttrError + "'] AS error_type, toUInt64(sum(Value)) AS failures,\n" +
		"       min(TimeUnix) AS first, max(TimeUnix) AS last\n" +
		"FROM " + b.table(q.Scope, "otel_metrics_sum") + "\n" + where(conds) + "\n" +
		"GROUP BY ServiceName, path, status_class, error_type\n" +
		"HAVING failures > 0\n" +
		"ORDER BY ServiceName, failures DESC, last DESC, path, status_class, error_type\n" +
		"LIMIT " + strconv.Itoa(MaxProbeFailures) + " BY ServiceName\n" +
		"LIMIT " + strconv.Itoa(MaxProbeREDRows*MaxProbeFailures)), nil
}

// ProbeFigures are one probe target's (or one path's) figures over one
// window: the fields of pkg/releasehealth.ProbeStats, in its order, so one
// converts to the other. Samples are the ok and failed requests; degraded
// and throttled ones are neither, and are counted apart. The latencies are
// of the samples, 0 when none was timed.
type ProbeFigures struct {
	Samples   int64   `json:"samples"`
	Failures  int64   `json:"failures"`
	Throttled int64   `json:"throttled"`
	Degraded  int64   `json:"degraded"`
	P50Ms     float64 `json:"p50_ms"`
	P95Ms     float64 `json:"p95_ms"`
}

// ProbePath keys ScanProbeREDByPath's figures.
type ProbePath struct {
	Target string `json:"target"`
	Path   string `json:"path"`
}

// ProbeFailure is one ProbeFailures group of a target: the fields of
// pkg/releasehealth.ProbeFailure, in its order, so one converts to the other.
type ProbeFailure struct {
	Path        string    `json:"path"`
	StatusClass string    `json:"status_class,omitempty"`
	ErrorType   string    `json:"error_type,omitempty"`
	First       time.Time `json:"first"`
	Last        time.Time `json:"last"`
	Count       int64     `json:"count"`
}

// ScanProbeRED reads ProbeRED's rows, by target. The caller closes rows.
func ScanProbeRED(rows Rows) (map[string]ProbeFigures, error) {
	out := map[string]ProbeFigures{}
	for rows.Next() {
		var svc string
		f, err := scanProbeFigures(rows, &svc)
		if err != nil {
			return nil, err
		}
		out[svc] = f
	}
	return out, rows.Err()
}

// ScanProbeREDByPath reads ProbeREDByPath's rows. The caller closes rows.
func ScanProbeREDByPath(rows Rows) (map[ProbePath]ProbeFigures, error) {
	out := map[ProbePath]ProbeFigures{}
	for rows.Next() {
		var k ProbePath
		f, err := scanProbeFigures(rows, &k.Target, &k.Path)
		if err != nil {
			return nil, err
		}
		out[k] = f
	}
	return out, rows.Err()
}

func scanProbeFigures(rows Rows, keys ...any) (ProbeFigures, error) {
	var (
		ok, failed, throttled, degraded uint64
		q                               []float64
		source                          string
	)
	if err := rows.Scan(append(keys, &ok, &failed, &throttled, &degraded, &q, &source)...); err != nil {
		return ProbeFigures{}, err
	}
	f := ProbeFigures{Samples: int64(ok + failed), Failures: int64(failed), Throttled: int64(throttled), Degraded: int64(degraded)}
	if len(q) == 3 {
		f.P50Ms, f.P95Ms = finiteMs(q[0]), finiteMs(q[1])
	}
	return f, nil
}

// ScanProbeFailures reads ProbeFailures' rows, by target, most failures
// first. The caller closes rows.
func ScanProbeFailures(rows Rows) (map[string][]ProbeFailure, error) {
	out := map[string][]ProbeFailure{}
	for rows.Next() {
		var (
			svc string
			f   ProbeFailure
			n   uint64
		)
		if err := rows.Scan(&svc, &f.Path, &f.StatusClass, &f.ErrorType, &n, &f.First, &f.Last); err != nil {
			return nil, err
		}
		f.Count = int64(n)
		f.First, f.Last = f.First.UTC(), f.Last.UTC()
		out[svc] = append(out[svc], f)
	}
	return out, rows.Err()
}

func finiteMs(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}
