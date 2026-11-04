package dashspec

import "time"

// SpecVersion identifies the serialized shape of Spec. Bump it on a breaking
// change so a consumer can refuse a spec it does not understand.
const SpecVersion = "forge.dashboards/v1"

// GridColumns is the width of a dashboard's grid: a panel's Width is in
// columns out of it.
const GridColumns = 24

// Signal is the telemetry table a panel reads.
type Signal string

// The signals a panel can read.
const (
	Traces  Signal = "traces"
	Metrics Signal = "metrics"
	Logs    Signal = "logs"
)

// PanelKind is how a panel is drawn.
type PanelKind string

const (
	// KindNumber is one headline value over the selected time range.
	KindNumber PanelKind = "number"
	// KindLine is a time series; with GroupBy, one line per group.
	KindLine PanelKind = "line"
	// KindTable is one row per GroupBy value, one column per Series.
	KindTable PanelKind = "table"
	// KindSearch is a log or span search (no aggregate): the rows themselves.
	KindSearch PanelKind = "search"
	// KindBar is a top-N bar chart: one bar group per GroupBy value, one bar
	// per Series within it, the Limit groups with the largest Series[0] first.
	KindBar PanelKind = "bar"
	// KindHeatmap is the distribution of one numeric field over time: time
	// buckets across, value buckets up, each cell coloured by how many events
	// fell in it (a latency heatmap is span.duration). It takes exactly one
	// series, a count with the bucketed value in Field.
	KindHeatmap PanelKind = "heatmap"
	// KindSQL is drawn from a read-only ClickHouse SELECT the panel carries in
	// SQL instead of from series. The reader runs it with the same guard and
	// limits as an alert rule's expr (see CheckSQL and the SQLParam*
	// parameters).
	KindSQL PanelKind = "sql"

	// The kinds below are drawn by the Reliant console from its own services,
	// not from a query over the panel's series, so they take no series,
	// columns or group-by. They cannot be drawn by HyperDX, whose renderer
	// skips them (see IsConsoleKind).

	// KindIssues is the grouped-errors list: new and regressed issues first,
	// then the most frequent. Served by RumService ListIssues.
	KindIssues PanelKind = "issues"
	// KindSessions is a list of browser sessions, with replay links. Served by
	// RumService ListSessions; HasError narrows it to sessions with an error.
	KindSessions PanelKind = "sessions"
	// KindFunnel is a product-event funnel over Panel.Funnel. Served from the
	// product_events table with ClickHouse windowFunnel.
	KindFunnel PanelKind = "funnel"
	// KindPostgres is drawn from a read-only SELECT the panel carries in
	// Postgres, run live against one of the project's databases (see
	// PostgresQuery). The console runs it; HyperDX cannot.
	KindPostgres PanelKind = "postgres"
	// KindText is prose, not data: the Markdown in Panel.Markdown, drawn with
	// no query (a heading over a row of panels, a runbook link, what a
	// dashboard is for). It takes no signal, series or group-by.
	KindText PanelKind = "text"
)

// IsConsoleKind reports whether k is drawn only by the Reliant console, so the
// HyperDX renderer has nothing to emit for it.
func IsConsoleKind(k PanelKind) bool {
	switch k {
	case KindIssues, KindSessions, KindFunnel, KindPostgres, KindText:
		return true
	}
	return false
}

// MaxMarkdownBytes bounds a KindText panel's Markdown.
const MaxMarkdownBytes = 10_000

// DisplayStyle is one way to draw a panel's data, where its kind has more
// than one. The query a panel runs is decided by its kind alone; a style only
// changes the picture, so a reader that does not know a style draws the
// kind's default instead and is still right about the numbers.
type DisplayStyle string

// The display styles, by the kinds that take them. The first of each kind is
// its default.
const (
	// StyleLine (default), StyleArea and StyleBars draw a line panel's time
	// series, or a sql panel's, as lines, filled areas or one bar per bucket.
	StyleLine DisplayStyle = "line"
	StyleArea DisplayStyle = "area"
	StyleBars DisplayStyle = "bars"
	// StyleBar (default), StylePie and StyleDonut draw a bar panel's groups as
	// bars or as the slices of a whole. A pie reads Series[0] alone.
	StyleBar   DisplayStyle = "bar"
	StylePie   DisplayStyle = "pie"
	StyleDonut DisplayStyle = "donut"
	// StyleStat (default) and StyleGauge draw a number panel's values as
	// figures or as dials between Display.Min and Display.Max.
	StyleStat  DisplayStyle = "stat"
	StyleGauge DisplayStyle = "gauge"
	// StyleFunnel draws a sql panel's rows as funnel steps, in the order the
	// statement returns them: the first text column names the step, the first
	// numeric column counts it, and the reader works out the conversion and
	// drop-off between steps. It is how a funnel the funnel kind cannot
	// express (one stitched across identities, say) is still drawn as one.
	StyleFunnel DisplayStyle = "funnel"
)

// displayStyles lists the styles each kind takes, its default first.
var displayStyles = map[PanelKind][]DisplayStyle{
	KindLine:   {StyleLine, StyleArea, StyleBars},
	KindBar:    {StyleBar, StylePie, StyleDonut},
	KindNumber: {StyleStat, StyleGauge},
	KindSQL:    {StyleLine, StyleArea, StyleBars, StyleFunnel},
}

// DisplayStyles returns the styles kind k takes, its default first; nil for a
// kind that is drawn one way only and takes no Display.
func DisplayStyles(k PanelKind) []DisplayStyle {
	return append([]DisplayStyle(nil), displayStyles[k]...)
}

// MaxTopN bounds Display.TopN.
const MaxTopN = 50

// Display says how a panel's data is drawn when its kind can draw it more
// than one way. Every field is optional and changes the picture only, never
// the query. Renderers that cannot honour a field ignore it (HyperDX ignores
// all of them).
type Display struct {
	// Style is one of DisplayStyles(kind). Empty is the kind's default.
	Style DisplayStyle `json:"style,omitempty"`
	// Stack stacks the series, so the top edge is their total and each band
	// or bar segment is one series' share: a line or sql panel drawn as area
	// or bars, or a bar panel drawn as bars. Only additive values (counts,
	// sums, rates) add up to a meaningful whole; the author decides.
	Stack bool `json:"stack,omitempty"`
	// Horizontal lays a bar panel's bars along the y axis, which suits long
	// group names.
	Horizontal bool `json:"horizontal,omitempty"`
	// Min and Max are a gauge's scale. Min defaults to 0; Max is required
	// unless the panel's unit is percent, whose scale is 0 to 1.
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
	// TopN is how many series a line panel (or slices a pie or donut) draws
	// before folding the rest into one "Other": summed where the values add
	// up, left out where they do not. The full list stays a click away. Zero
	// is the reader's own default; at most MaxTopN.
	TopN int `json:"topN,omitempty"`
}

// Row limits of a table or bar panel (Panel.Limit).
const (
	// MaxLimit is the most rows a panel may keep.
	MaxLimit = 200
	// DefaultBarLimit is how many bar groups a bar panel with no Limit draws.
	DefaultBarLimit = 10
)

// FunnelBy is what a funnel counts: how events are tied into one journey.
type FunnelBy string

// The funnel journeys.
const (
	FunnelBySession FunnelBy = "session"
	FunnelByUser    FunnelBy = "user"
)

// Funnel is the definition a KindFunnel panel draws: the ordered event names
// a journey passes through, within Window, counted per session or per user.
type Funnel struct {
	// Steps are product event names (the names passed to track()), in order.
	Steps []string `json:"steps"`
	// Window is how long a journey may take from its first step to its last:
	// a number and a unit of s, m, h or d ("1h", "7d"). Empty is "1h".
	Window string `json:"window,omitempty"`
	// By is the journey's identity. Empty is FunnelBySession.
	By FunnelBy `json:"by,omitempty"`
}

// Funnel defaults, applied where a panel leaves a field empty.
const (
	DefaultFunnelWindow = "1h"
	DefaultFunnelBy     = FunnelBySession
)

// Funnel bounds.
const (
	minFunnelSteps = 2
	maxFunnelSteps = 10
)

// Thresholds colour a series' values the way Core Web Vitals do: at or below
// Good is good, above Poor is poor, between is "needs improvement". Renderers
// that cannot colour ignore it.
type Thresholds struct {
	Good float64 `json:"good"`
	Poor float64 `json:"poor"`
}

// Agg is an aggregate function.
type Agg string

// The aggregate functions a series can apply.
const (
	AggCount         Agg = "count"
	AggCountDistinct Agg = "count_distinct"
	AggSum           Agg = "sum"
	AggAvg           Agg = "avg"
	AggMax           Agg = "max"
	AggQuantile      Agg = "quantile"
	AggLast          Agg = "last"
	// AggRate, AggIncrease and AggMin are metrics-only. A rate is per second,
	// an increase is per bucket, and both are time-weighted per series; on a
	// monotonic sum, AggSum still means the increase.
	AggRate     Agg = "rate"
	AggIncrease Agg = "increase"
	AggMin      Agg = "min"
)

// Unit is how a panel's values are formatted.
type Unit string

// The units a panel's values are formatted in.
const (
	UnitNone    Unit = ""
	UnitCount   Unit = "count"
	UnitMillis  Unit = "ms"
	UnitBytes   Unit = "bytes"
	UnitPercent Unit = "percent" // values are fractions in [0,1]
	UnitSeconds Unit = "s"
)

// Op is a predicate operator.
type Op string

// The predicate operators.
const (
	OpEq     Op = "eq"
	OpNeq    Op = "neq"
	OpIn     Op = "in"
	OpNotIn  Op = "not_in"
	OpPrefix Op = "prefix"
	OpMatch  Op = "match" // RE2
	OpExists Op = "exists"
	OpGTE    Op = "gte"
)

// Semantic field names. A field is one of:
//
//	service.name, deployment.environment.name        (every signal)
//	span.name, span.kind, span.status,
//	span.status_message, span.duration               (traces)
//	span.attr.<key>                                  (traces)
//	log.severity, log.body, log.attr.<key>           (logs)
//	metric.attr.<key>                                (metrics)
//	resource.attr.<key>                              (every signal)
//
// span.kind is "server" or "client"; span.status and log.severity compare
// against "error". span.duration is milliseconds.
const (
	FieldService = "service.name"
	FieldEnv     = "deployment.environment.name"
)

// Predicate is one condition. Values are literals; Field is a semantic field.
type Predicate struct {
	Field  string   `json:"field"`
	Op     Op       `json:"op"`
	Value  string   `json:"value,omitempty"`
	Values []string `json:"values,omitempty"`
}

// Series is one aggregate shown by a panel.
type Series struct {
	Label string `json:"label"`
	Agg   Agg    `json:"agg"`
	// Quantile is the level for AggQuantile, e.g. 0.95.
	Quantile float64 `json:"quantile,omitempty"`
	// Field is the aggregated value: a semantic field for traces and logs
	// (span.duration, span.attr.lcp), the metric NAME for metrics. Empty for
	// AggCount.
	Field string `json:"field,omitempty"`
	// MetricType is "sum", "gauge", "histogram", "exp_histogram" or "summary"
	// (metrics only).
	MetricType string `json:"metricType,omitempty"`
	// Where narrows this series alone (e.g. only the failed requests).
	Where []Predicate `json:"where,omitempty"`
	// Thresholds grade the series' values good / needs improvement / poor.
	Thresholds *Thresholds `json:"thresholds,omitempty"`
}

// Panel is one tile.
type Panel struct {
	// Section is an optional declared dashboard section, for temporary viewer
	// toggles. Empty retains the unsectioned layout.
	Section     string    `json:"section,omitempty"`
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Kind        PanelKind `json:"kind"`
	// Signal is required on every kind but KindSQL, whose statement names its
	// own tables.
	Signal Signal   `json:"signal,omitempty"`
	Series []Series `json:"series,omitempty"`
	// Ratio makes a two-series line panel plot Series[0]/Series[1]
	// (an error rate is failed requests over all requests).
	Ratio bool `json:"ratio,omitempty"`
	// Formula makes a line or number panel plot one arithmetic expression
	// over its series instead of the series themselves: "A / B * 100", where
	// A is Series[0], B is Series[1], and so on (see ParseFormula). It is the
	// general form of Ratio, which a panel uses instead of, not with, it.
	Formula string `json:"formula,omitempty"`
	// Limit keeps the top Limit rows of a table or bar panel, largest
	// Series[0] first. Zero is DefaultBarLimit for a bar and the reader's own
	// row cap for a table; at most MaxLimit.
	Limit int `json:"limit,omitempty"`
	// SQL is a KindSQL panel's statement: one read-only SELECT over the
	// telemetry tables, which may use the SQLParam* query parameters. Only
	// that kind takes it.
	SQL string `json:"sql,omitempty"`
	// MinStep is the narrowest bucket a KindSQL panel's statement produces,
	// in the same form as DefaultRange ("1d"): set it when the statement
	// groups by a fixed bucket (toDate, toStartOfWeek) rather than by the
	// {step} parameter. A range shorter than two buckets can draw only one
	// point, so a renderer widens such a panel's range to hold two, and says
	// so. Only a sql panel takes it.
	MinStep string `json:"minStep,omitempty"`
	// Where narrows every series of the panel.
	Where   []Predicate `json:"where,omitempty"`
	GroupBy []string    `json:"groupBy,omitempty"`
	// Columns are the fields a KindSearch panel shows.
	Columns []string `json:"columns,omitempty"`
	// Funnel is what a KindFunnel panel draws; only that kind takes it.
	Funnel *Funnel `json:"funnel,omitempty"`
	// Postgres is the query a KindPostgres panel draws; only that kind takes
	// it, and it takes no Signal, Series or SQL.
	Postgres *PostgresQuery `json:"postgres,omitempty"`
	// HasError narrows a KindSessions panel to sessions that hit an error;
	// only that kind takes it.
	HasError bool `json:"hasError,omitempty"`
	// Markdown is a KindText panel's content (CommonMark with GFM tables and
	// links, at most MaxMarkdownBytes); only that kind takes it.
	Markdown string `json:"markdown,omitempty"`
	// Display picks how a line, bar, number or sql panel is drawn (see
	// Display); nil draws the kind's default.
	Display *Display `json:"display,omitempty"`
	Unit    Unit     `json:"unit,omitempty"`
	// Width is in grid columns out of 24; Height in rows. Zero takes the
	// default for the kind.
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
}

// Filter is a dashboard-level dropdown that narrows every panel.
type Filter struct {
	// PanelIDs restricts this filter to declared panels. Empty preserves the
	// dashboard-wide default. RPC filters must not blank runtime/billing panels.
	PanelIDs []string `json:"panelIds,omitempty"`
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Field    string   `json:"field"`
	// Signal is the telemetry the dropdown reads its values from. Empty is
	// Traces, which every dashboard but the metrics ones draws.
	Signal Signal `json:"signal,omitempty"`
}

// AnnotationSource names where a dashboard's time-series markers come from.
type AnnotationSource string

// The annotation sources. The spec only DECLARES a source; the renderer draws
// it from data it owns (the Reliant console reads the release ledger).
const (
	// AnnotationReleases marks every promotion of a release into the
	// environment on the dashboard's time-series panels.
	AnnotationReleases AnnotationSource = "releases"
)

// Annotation is a marker series drawn over every KindLine panel of a dashboard.
// HyperDX has no such concept, so RenderHyperDX ignores it.
type Annotation struct {
	Source AnnotationSource `json:"source"`
	Label  string           `json:"label,omitempty"`
}

// Section is a named, ordered group of panels. It changes presentation only;
// filtering remains explicit through Filter.PanelIDs.
type Section struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Scope says which telemetry a dashboard's panels read. It is one enum: the
// default and the two wider reads.
type Scope string

// The dashboard scopes.
const (
	// ScopeProject is the default (the empty value): panels read the
	// environments the viewer has selected within the route's project, and the
	// reader narrows every query to them.
	ScopeProject Scope = "project"
	// ScopeOrg reads across every project of the organization: all of the
	// org's environments, still narrowed to the environments the viewer
	// selected. The reader names no project, and a console lists the
	// environments as project/env.
	ScopeOrg Scope = "org"
	// ScopePlatform marks a dashboard whose signals are platform or
	// infrastructure telemetry that belongs to no environment (a database, a
	// message bus, a collector, a GitOps controller). That telemetry carries
	// no reliant.env.id, so an environment filter would match nothing. A
	// reader drops only that environment predicate: who may read, and from
	// which organization's data, is still the reader's decision.
	ScopePlatform Scope = "platform"
)

// Group is the list section a dashboard belongs to. It is presentation data
// only: it never affects identity, scope or a query.
type Group string

// The groups. A dashboard that declares none, or one a reader does not know,
// is GroupCustom.
const (
	GroupOverview Group = "overview"
	GroupServices Group = "services"
	GroupJobs     Group = "jobs"
	GroupProduct  Group = "product"
	GroupPlatform Group = "platform"
	GroupReleases Group = "releases"
	GroupCustom   Group = "custom"
)

// Groups lists every group in display order.
var Groups = []Group{GroupOverview, GroupServices, GroupJobs, GroupProduct, GroupPlatform, GroupReleases, GroupCustom}

// MaxTags bounds Dashboard.Tags.
const MaxTags = 8

// MaxReplaces bounds Dashboard.Replaces.
const MaxReplaces = 32

// GroupOf is the group d is listed under: its own, else GroupCustom.
func GroupOf(d Dashboard) Group {
	for _, g := range Groups {
		if d.Group == g {
			return g
		}
	}
	return GroupCustom
}

// Dashboard is an ordered list of panels under a shared set of filters.
type Dashboard struct {
	// Scope is empty for ScopeProject.
	Scope       Scope     `json:"scope,omitempty"`
	Sections    []Section `json:"sections,omitempty"`
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	// Group is the list section (see Group); Tags are free-form labels
	// (lowercase letters, digits, '.', '_' and '-', at most MaxTags).
	Group Group    `json:"group,omitempty"`
	Tags  []string `json:"tags,omitempty"`
	// Replaces lists the ids of dashboards this one supersedes (a renamed
	// generated dashboard, or several consolidated into one). A reader that
	// keeps per-dashboard state (stars, copies) moves it from a replaced id
	// to this one. It never changes what the dashboard draws.
	Replaces    []string     `json:"replaces,omitempty"`
	Filters     []Filter     `json:"filters,omitempty"`
	Annotations []Annotation `json:"annotations,omitempty"`
	// DefaultRange is the time range the dashboard opens on when the viewer
	// picked none: a number and a unit of s, m, h or d ("24h", "7d"), at most
	// MaxDefaultRange. A dashboard whose panels bucket by day declares "7d";
	// without it a renderer opens on its own default (an hour, in the console).
	// A renderer with fixed presets rounds up to the nearest one.
	DefaultRange string  `json:"defaultRange,omitempty"`
	Panels       []Panel `json:"panels"`
}

// MaxDefaultRange bounds Dashboard.DefaultRange: the longest range a
// dashboard's panels may be read over.
const MaxDefaultRange = 30 * 24 * time.Hour
