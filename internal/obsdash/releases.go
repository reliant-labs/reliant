package obsdash

import (
	"fmt"

	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
	"github.com/reliant-labs/forge/pkg/release"
)

// ReleasesID is the id of the dashboard BuildReleases returns, so its file is
// "<project>-releases_gen.json".
const ReleasesID = "releases"

// fieldVersion is the service.version every workload reports, rendered by KCL
// from the release the workload was deployed from (OTEL_RESOURCE_ATTRIBUTES).
const fieldVersion = "resource.attr.service.version"

// issueFirstSeenDays bounds how far back an issue's first event is looked for
// by "New issues by release": an issue is new in the range when its first
// event in the issueFirstSeenDays before the end of the range falls in it.
// It is the longest range a dashboard may open on (dashspec.MaxDefaultRange),
// so an issue the range's first release inherited is not read as its own.
const issueFirstSeenDays = 30

// The sections of the Releases dashboard, in the order its panels run: the
// latest release's verdict first (what a team opens the dashboard for after
// shipping), then how the team ships, how far each release has rolled out,
// and every signal split by release.
var releasesSections = []Section{
	{ID: "latest", Title: "Latest release",
		Description: "Each environment's running release against the one it replaced, judged by the release-health rule forge obs check applies: the same windows, thresholds, findings and verdict."},
	{ID: "delivery", Title: "Delivery",
		Description: "How often and how safely the team ships, from the release ledger: the four DORA figures over the range, and every promotion and rollback across environments."},
	{ID: "adoption", Title: "Adoption",
		Description: "How far each release has rolled out: the share of backend requests and of browser sessions each version served."},
	{ID: "health", Title: "Health by release",
		Description: "Each signal split by the release that served it. A regression is the version whose line departs at its promotion marker."},
}

// BuildReleases is the "Releases" dashboard a team opens after shipping:
// whether the last release was good, how fast and how safely it ships, how far
// a rollout has got, and how each release behaved against the others.
//
// The verdict is release health's, never a second one: the "Latest release"
// panel is drawn by the console from GetReleaseHealth, which is
// pkg/releasehealth over the env's recorded thresholds, so the dashboard, a
// release's Health tab and `forge obs check` say the same thing. The ledger
// panels (DORA and the promotion timeline) come from the release ledger the
// control plane keeps. Neither is telemetry, so both are delivery-kind panels
// with no query (dashspec delivery.go).
//
// The telemetry panels count what release health counts: weighted server
// spans, errors that are not cancellations, latency without streams
// (obsquery's span expressions). Columns are named for people; versions are
// the service.version each workload reports. Like Workloads and Product
// usage, the dashboard ships in the spec and the release bundle only: its sql
// panels read the hosted browser and issue tables.
func BuildReleases(project string) Dashboard {
	d := Dashboard{
		ID:    ReleasesID,
		Title: project + ": releases",
		Group: GroupReleases,
		Description: "Whether the last release was good, how often and how safely the team ships, how far each rollout has got, and how every release behaved against the others. " +
			"The verdict is release health's (forge obs check); promotions, rollbacks and DORA come from the release ledger; deploy markers are drawn on every chart.",
		Filters: []Filter{
			// The delivery panels are per environment and ignore the service
			// selection (dashspec delivery.go), so the filter stays dashboard-wide.
			{ID: "service", Label: "Service", Field: FieldService},
			{ID: "env", Label: "Environment", Field: FieldEnv},
		},
		Sections: releasesSections,
		// A week holds several releases and their rollouts; an hour holds one
		// flat line per version.
		DefaultRange: "7d",
	}
	byVersion := []string{fieldVersion}
	server := []Predicate{{Field: "span.kind", Op: OpEq, Value: "server"}}
	share := func(id, title, desc, sql string) Panel {
		return Panel{Section: "adoption", ID: id, Title: title, Description: desc, Kind: KindSQL, SQL: sql, Unit: UnitPercent,
			Display: &dashspec.Display{Style: dashspec.StyleArea, Stack: true, TopN: 8}, Width: 12, Height: 8}
	}
	requests := requestsByVersionPanel()
	requests.Section = "health"
	requests.Description = "Throughput: server requests per bucket, one line per version."

	d.Panels = []Panel{
		{Section: "latest", ID: "release-health", Title: "Latest release by environment",
			Description: "The running release in each environment against the one before it: error rate, p95 latency and throughput before and after the promotion, " +
				"new and regressed issues, and the verdict (good, regressed, or not enough data yet). Open a release for its full Health tab.",
			Kind: KindReleaseHealth, Width: 24, Height: 10},

		{Section: "delivery", ID: "delivery", Title: "Delivery performance",
			Description: "Deployment frequency, lead time (commit to promotion), change failure rate and time to restore over the range, from the release ledger. " +
				"A change fails when a rollback follows it within 24 hours.",
			Kind: KindDelivery, Width: 24, Height: 5},
		{Section: "delivery", ID: "promotions", Title: "Promotions",
			Description: "Every promotion and rollback across environments in the range, newest first: the release, its commit, who promoted it and when.",
			Kind:        KindPromotions, Width: 24, Height: 10},

		share("backend-version-share", "Backend traffic by version",
			"The share of server requests each service.version served, per bucket: a rollout is one band replacing another.", backendVersionShareSQL),
		share("browser-version-share", "Browser sessions by version",
			"The share of browser sessions started on each frontend version, per bucket: how fast visitors pick up a new build.", browserVersionShareSQL),

		{Section: "health", ID: "error-rate-by-version", Title: "Error rate by version",
			Description: "Server errors (not cancellations) over all requests, one line per version.",
			Kind:        KindLine, Signal: Traces, Where: server, Ratio: true,
			Series:  []Series{{Label: "errors", Agg: AggCount, Where: []Predicate{errorSpans}}, {Label: "requests", Agg: AggCount}},
			GroupBy: byVersion, Unit: UnitPercent, Width: 12, Height: 7},
		{Section: "health", ID: "p95-by-version", Title: "p95 latency by version",
			Description: "The 95th percentile of server request time, one line per version.",
			Kind:        KindLine, Signal: Traces, Where: server,
			Series:  []Series{{Label: "p95", Agg: AggQuantile, Quantile: 0.95, Field: "span.duration"}},
			GroupBy: byVersion, Unit: UnitMillis, Width: 12, Height: 7},
		requests,
		{Section: "health", ID: "new-issues-by-release", Title: "New issues by release",
			Description: fmt.Sprintf("Server and browser issues first seen in the range, by the version that first raised them, counting those with at least %d events "+
				"(release health's new-issue bar). First seen is the first event in the %d days before the end of the range.", newIssueMinEvents, issueFirstSeenDays),
			Kind: KindSQL, SQL: newIssuesByReleaseSQL, Display: &dashspec.Display{Style: dashspec.StyleBars, Colors: &dashspec.SeriesColors{"New issues": dashspec.ColorDanger}},
			Width: 12, Height: 7},
		{Section: "health", ID: "version-comparison", Title: "Releases side by side",
			Description: "One row per backend version seen in the range, newest first: when it was first and last seen, its requests, error rate and latency.",
			Kind:        KindSQL, SQL: versionComparisonSQL, Width: 24, Height: 8},
		{Section: "health", ID: "crash-free-sessions-by-version", Title: "Crash-free sessions by version",
			Description: "Browser sessions started in the range on each frontend version, newest first, and the share with no browser error " +
				"(an uncaught exception, an unhandled rejection, or an exception the app recorded or logged).",
			Kind: KindSQL, SQL: crashFreeSessionsSQL, Width: 24, Height: 7},
	}
	return d
}

// requestsByVersionPanel is throughput split by release, which the per-process
// dashboards draw too.
func requestsByVersionPanel() Panel {
	return Panel{ID: "request-rate-by-version", Title: "Requests by version",
		Kind: KindLine, Signal: Traces, Where: []Predicate{{Field: "span.kind", Op: OpEq, Value: "server"}},
		Series: []Series{{Label: "requests", Agg: AggCount}}, GroupBy: []string{fieldVersion}, Unit: UnitCount, Width: 12, Height: 7}
}

// newIssueMinEvents is release health's default new-issue bar: an issue with
// fewer events in a release's window is not a finding, so it is not counted
// against a release here either.
var newIssueMinEvents = release.DefaultReleaseHealth().NewIssueMinEvents

// The scopes of the telemetry panels: the dashboard's range, environments and
// services. reliant.env.id is the environment's id, {env_ids}.
const (
	releasesRange      = "Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)}"
	releasesEnvScope   = "(empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, ResourceAttributes['reliant.env.id']))"
	releasesSvcScope   = "(empty({services:Array(String)}) OR has({services:Array(String)}, ServiceName))"
	releasesTraceScope = releasesRange + " AND " + releasesEnvScope + " AND " + releasesSvcScope
	// releasesVersion is a span's release, with a name for the spans of a
	// workload that reports none.
	releasesVersion = "if(ResourceAttributes['service.version'] = '', '(unversioned)', ResourceAttributes['service.version'])"
)

// The weighted request and error counts, as release health counts them.
var (
	weightedRequests = "sum(" + obsquery.SpanWeightSQL + ")"
	weightedErrors   = "sumIf(" + obsquery.SpanWeightSQL + ", " + obsquery.SpanErrorSQL + ")"
)

// backendVersionShareSQL is each bucket's server requests split by version, as
// fractions of the bucket (unit percent), one series per version.
var backendVersionShareSQL = `SELECT t, version AS "Version", round(n / sum(n) OVER (PARTITION BY t), 4) AS "Share of requests"
FROM (
    SELECT toStartOfInterval(Timestamp, toIntervalSecond({step:UInt32})) AS t, ` + releasesVersion + ` AS version,
           ` + weightedRequests + ` AS n
    FROM otel_traces
    WHERE ` + obsquery.SpanServerSQL + ` AND ` + releasesTraceScope + `
    GROUP BY t, version
)
ORDER BY t, version`

// browserSessions is one row per browser session started in the range: the
// session store keeps a row per session per day, so a session's figures are
// merged across its rows first (the same merge the console's session list
// reads). The day bound is the range's days and the one before, so a session
// that began before midnight is whole.
const browserSessions = `SELECT ServiceName, SessionId, min(Start) AS started,
           if(max(ServiceVersion) = '', '(unversioned)', max(ServiceVersion)) AS version, max(EnvId) AS env_id, sum(Errors) AS errors
    FROM rum_sessions
    WHERE Day >= subtractDays(toDate({from:DateTime64(9)}), 1) AND Day <= toDate({to:DateTime64(9)}) AND ` + releasesSvcScope + `
    GROUP BY ServiceName, SessionId
    HAVING started >= {from:DateTime64(9)} AND started < {to:DateTime64(9)}
       AND (empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, env_id))`

// browserVersionShareSQL is each bucket's new browser sessions split by the
// frontend version they ran, as fractions of the bucket.
var browserVersionShareSQL = `SELECT t, version AS "Version", round(n / sum(n) OVER (PARTITION BY t), 4) AS "Share of sessions"
FROM (
    SELECT toStartOfInterval(started, toIntervalSecond({step:UInt32})) AS t, version, count() AS n
    FROM (
    ` + browserSessions + `
    )
    GROUP BY t, version
)
ORDER BY t, version`

// crashFreeSessionsSQL is one row per frontend version, newest first: its
// sessions, those that hit a browser error, and the share that did not.
var crashFreeSessionsSQL = `SELECT version AS "Version",
       formatDateTime(min(started), '%Y-%m-%d %H:%i') AS "First session (UTC)",
       count() AS "Sessions",
       countIf(errors > 0) AS "Sessions with an error",
       round(100 * countIf(errors = 0) / count(), 2) AS "Crash-free sessions (%)"
FROM (
    ` + browserSessions + `
)
GROUP BY version
ORDER BY min(started) DESC, version
LIMIT 20`

// versionComparisonSQL is one row per backend version, newest first, over the
// same weighted server spans as release health: requests, the error rate and
// the latency quantiles (streams left out).
var versionComparisonSQL = `SELECT ` + releasesVersion + ` AS "Version",
       formatDateTime(min(Timestamp), '%Y-%m-%d %H:%i') AS "First seen (UTC)",
       formatDateTime(max(Timestamp), '%Y-%m-%d %H:%i') AS "Last seen (UTC)",
       toUInt64(round(` + weightedRequests + `)) AS "Requests",
       round(100 * ` + weightedErrors + ` / nullIf(` + weightedRequests + `, 0), 2) AS "Error rate (%)",
       round(quantileExactWeightedIf(0.5)(Duration / 1000000, ` + obsquery.SpanWeightIntSQL + `, NOT ` + obsquery.SpanStreamSQL + `), 1) AS "p50 (ms)",
       round(quantileExactWeightedIf(0.95)(Duration / 1000000, ` + obsquery.SpanWeightIntSQL + `, NOT ` + obsquery.SpanStreamSQL + `), 1) AS "p95 (ms)",
       round(quantileExactWeightedIf(0.99)(Duration / 1000000, ` + obsquery.SpanWeightIntSQL + `, NOT ` + obsquery.SpanStreamSQL + `), 1) AS "p99 (ms)"
FROM otel_traces
WHERE ` + obsquery.SpanServerSQL + ` AND ` + releasesTraceScope + `
GROUP BY ` + releasesVersion + `
ORDER BY min(Timestamp) DESC, "Version"
LIMIT 20`

// issueLookback is an issue table's read: the issueFirstSeenDays before the
// end of the range, in the dashboard's environments and services. t qualifies
// the columns ("e." in a join), or is "".
func issueLookback(t string) string {
	return fmt.Sprintf("%[1]sTimestamp >= {to:DateTime64(9)} - INTERVAL %[2]d DAY AND %[1]sTimestamp < {to:DateTime64(9)}", t, issueFirstSeenDays) +
		" AND (empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, " + t + "EnvId))" +
		" AND (empty({services:Array(String)}) OR has({services:Array(String)}, " + t + "ServiceName))"
}

// newIssuesByReleaseSQL counts, per version, the issues whose first event in
// the lookback fell in the range and was raised by that version, and that
// reached the new-issue bar in the range. A server issue is its sfp1
// fingerprint (server_error_events); a browser issue is the fingerprint of its
// symbolicated stack (error_stacks, keyed by the build's raw stack), so the
// same error in two builds is one issue, not a new one per release.
var newIssuesByReleaseSQL = `SELECT first_version AS "Version", count() AS "New issues"
FROM (
    SELECT ServiceName, Fingerprint, min(Timestamp) AS first_seen,
           argMin(if(ServiceVersion = '', '(unversioned)', ServiceVersion), Timestamp) AS first_version,
           countIf(Timestamp >= {from:DateTime64(9)}) AS events
    FROM (
        SELECT ServiceName, Fingerprint, Timestamp, ServiceVersion
        FROM server_error_events
        WHERE Fingerprint != '' AND ` + issueLookback("") + `
        UNION ALL
        SELECT e.ServiceName AS ServiceName, s.fp AS Fingerprint, e.Timestamp AS Timestamp, e.ServiceVersion AS ServiceVersion
        FROM error_events AS e
        INNER JOIN (
            SELECT ServiceName, ServiceVersion, RawStackHash, argMax(Fingerprint, UpdatedAt) AS fp
            FROM error_stacks
            GROUP BY ServiceName, ServiceVersion, RawStackHash
        ) AS s ON e.ServiceName = s.ServiceName AND e.ServiceVersion = s.ServiceVersion AND e.RawStackHash = s.RawStackHash
        WHERE s.fp != '' AND ` + issueLookback("e.") + `
    )
    GROUP BY ServiceName, Fingerprint
)
WHERE first_seen >= {from:DateTime64(9)} AND events >= ` + fmt.Sprint(newIssueMinEvents) + `
GROUP BY first_version
ORDER BY min(first_seen), first_version`
