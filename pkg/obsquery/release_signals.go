package obsquery

import "math"

// Release-health signals beside server spans (pkg/releasehealth
// ServiceHealth.Signal): what a release is judged on when a service sends no
// server spans of its own.
//
//   - Ingress RED: the hosting platform's ingress measures what it answers for
//     each workload and writes it into the tenant (the control plane's
//     workspace-proxy, internal/proxy/ingress_red.go) as delta metrics:
//     IngressRequestsMetric{status_class} (a delta sum counting every
//     request it answered for) and the IngressDurationMetric histogram
//     (seconds to the response headers, of the requests it timed), resource service.name = the
//     workload or static site, reliant.source = IngressSource. A static site,
//     or an app without OpenTelemetry, still has these.
//   - Browser RED: what a frontend's own pages report through forge's browser
//     telemetry: page views, page loads and their duration, JavaScript errors,
//     and the fetch/XHR requests the page made.
const (
	IngressRequestsMetric = "reliant.ingress.requests"
	IngressDurationMetric = "reliant.ingress.duration"
	IngressStatusClass    = "status_class"
	IngressSource         = "ingress"
)

// IngressRED builds the per-service request statement over the platform's
// ingress metrics. Columns are RED's: ServiceName, requests, errors, q
// (p50,p95,p99 in ms), source. Requests and errors are the request
// COUNTER's (IngressRequestsMetric, errors its 5xx class): it counts every
// request the ingress answered for, protocol upgrades, 1xx answers and
// abandoned requests included, none of which the duration histogram times,
// so a rate read from the histogram would understate them. The quantiles are
// the histogram's, estimated from its buckets, each bucket's requests placed
// at its midpoint (the last bound for the overflow bucket); a service whose
// requests were never timed in the window has none ([]). GroupBy is refused:
// ingress carries no span attributes to split by.
func (q Overview) IngressRED() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeOverview); err != nil {
		return Stmt{}, err
	}
	if len(q.GroupBy) > 0 {
		return Stmt{}, invalid("ingress RED takes no group_by")
	}
	b := newBuilder(q.Scope)
	sc, err := b.scopeConds(SigMetrics, q.Scope)
	if err != nil {
		return Stmt{}, err
	}
	source := b.str(IngressSource)
	counted := append([]string{
		b.rangeCondSec("TimeUnix", q.Scope),
		"MetricName = " + b.str(IngressRequestsMetric),
		"ResourceAttributes['reliant.source'] = " + source,
	}, sc...)
	timed := append([]string{
		b.rangeCondSec("TimeUnix", q.Scope),
		"MetricName = " + b.str(IngressDurationMetric),
		"ResourceAttributes['reliant.source'] = " + source,
		"length(ExplicitBounds) > 0",
		"length(BucketCounts) = length(ExplicitBounds) + 1",
	}, sc...)
	mid := "arrayMap(i -> if(i > length(ExplicitBounds), ExplicitBounds[length(ExplicitBounds)], " +
		"(if(i = 1, 0, ExplicitBounds[i - 1]) + ExplicitBounds[i]) / 2), arrayEnumerate(BucketCounts))"
	return b.stmt("SELECT c.ServiceName AS ServiceName, c.requests AS requests, c.errors AS errors, h.q AS q, " + source + " AS source\n" +
		"FROM (\n" +
		"  SELECT ServiceName, toUInt64(sum(Value)) AS requests, toUInt64(sumIf(Value, Attributes['" + IngressStatusClass + "'] = '5xx')) AS errors\n" +
		"  FROM " + b.table(q.Scope, "otel_metrics_sum") + "\n  " + where(counted) + "\n" +
		"  GROUP BY ServiceName\n" +
		") AS c\n" +
		"LEFT JOIN (\n" +
		"  SELECT ServiceName, quantilesExactWeighted(0.5, 0.95, 0.99)(tupleElement(bucket, 1) * 1000, tupleElement(bucket, 2)) AS q\n" +
		"  FROM (\n" +
		"    SELECT ServiceName, arrayZip(" + mid + ", BucketCounts) AS buckets\n" +
		"    FROM " + b.table(q.Scope, "otel_metrics_histogram") + "\n    " + where(timed) + "\n" +
		"  )\n  ARRAY JOIN buckets AS bucket\n" +
		"  WHERE tupleElement(bucket, 2) > 0\n" +
		"  GROUP BY ServiceName\n" +
		") AS h ON h.ServiceName = c.ServiceName\n" +
		"WHERE c.requests > 0\n" +
		"ORDER BY requests DESC, ServiceName\nLIMIT 200"), nil
}

// The browser SDK's span shapes BrowserRED reads (forge's browser telemetry,
// @hyperdx/browser): a navigation is documentLoad (a full page load) or
// routeChange (a client-side one); an error is a span of the errors or
// record-exception scope with an error status (a console.error of a plain
// string has none, and is no error); a request is a fetch or XHR span.
const (
	browserViewExpr    = "SpanName IN ('documentLoad', 'routeChange')"
	browserLoadExpr    = "SpanName = 'documentLoad'"
	browserErrorExpr   = "(ScopeName IN ('errors', 'record-exception') AND " + spanErrorExpr + ")"
	browserRequestExpr = "SpanAttributes['component'] IN ('fetch', 'xml-http-request')"
	browserReqStatus   = "toUInt16OrZero(if(SpanAttributes['http.response.status_code'] != '', SpanAttributes['http.response.status_code'], SpanAttributes['http.status_code']))"
	// A failed request: the server answered 5xx, or nothing answered (no
	// status, an error status). A 4xx is the page's own business.
	browserFailedExpr = "(" + browserReqStatus + " >= 500 OR (" + browserReqStatus + " = 0 AND " + spanErrorExpr + "))"
)

// BrowserRED builds the per-frontend statement over browser telemetry.
// Columns: ServiceName, views, loads, js_errors, requests, failed, load_q
// (p50,p95,p99 of documentLoad in ms; empty when there was none). Browser
// spans are never sampled, so the counts are plain counts. Parse it with
// ScanBrowserRED.
func (q Overview) BrowserRED() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeOverview); err != nil {
		return Stmt{}, err
	}
	if len(q.GroupBy) > 0 {
		return Stmt{}, invalid("browser RED takes no group_by")
	}
	b := newBuilder(q.Scope)
	sc, err := b.scopeConds(SigTraces, q.Scope)
	if err != nil {
		return Stmt{}, err
	}
	conds := append([]string{
		b.rangeCond("Timestamp", q.Scope),
		"(" + browserViewExpr + " OR " + browserErrorExpr + " OR " + browserRequestExpr + ")",
	}, sc...)
	return b.stmt("SELECT ServiceName, countIf(" + browserViewExpr + ") AS views, countIf(" + browserLoadExpr + ") AS loads,\n" +
		"       countIf(" + browserErrorExpr + ") AS js_errors, countIf(" + browserRequestExpr + ") AS requests,\n" +
		"       countIf(" + browserRequestExpr + " AND " + browserFailedExpr + ") AS failed,\n" +
		"       if(loads = 0, CAST([], 'Array(Float64)'), quantilesExactIf(0.5, 0.95, 0.99)(Duration / 1000000, " + browserLoadExpr + ")) AS load_q\n" +
		"FROM " + b.table(q.Scope, "otel_traces") + "\n" + where(conds) + "\n" +
		"GROUP BY ServiceName\n" +
		"ORDER BY views DESC, ServiceName\nLIMIT 200"), nil
}

// BrowserFigures are one frontend's browser figures over one window: the
// fields of pkg/releasehealth.BrowserStats, in its order, so one converts to
// the other.
type BrowserFigures struct {
	PageViews      int64   `json:"page_views"`
	PageLoads      int64   `json:"page_loads"`
	JSErrors       int64   `json:"js_errors"`
	Requests       int64   `json:"requests"`
	FailedRequests int64   `json:"failed_requests"`
	PageLoadP95Ms  float64 `json:"page_load_p95_ms"`
}

// ScanBrowserRED reads BrowserRED's rows, by service. The caller closes rows.
func ScanBrowserRED(rows Rows) (map[string]BrowserFigures, error) {
	out := map[string]BrowserFigures{}
	for rows.Next() {
		var (
			svc                              string
			views, loads, errs, reqs, failed uint64
			q                                []float64
		)
		if err := rows.Scan(&svc, &views, &loads, &errs, &reqs, &failed, &q); err != nil {
			return nil, err
		}
		f := BrowserFigures{PageViews: int64(views), PageLoads: int64(loads), JSErrors: int64(errs), Requests: int64(reqs), FailedRequests: int64(failed)}
		if len(q) == 3 && !math.IsNaN(q[1]) && !math.IsInf(q[1], 0) {
			f.PageLoadP95Ms = q[1]
		}
		out[svc] = f
	}
	return out, rows.Err()
}
