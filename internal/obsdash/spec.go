// Package obsdash turns a forge project's own service metadata into
// observability dashboards.
//
// The package has two layers, and the split is the point:
//
//   - Spec (this file) is a backend-neutral description of WHAT to show:
//     dashboards made of panels, each panel a query intent (signal, aggregate,
//     predicates on OpenTelemetry attribute names). It names no ClickHouse
//     column, no HyperDX field and no chart library. Build derives it from the
//     project's services, rpcs, frontends and database. It is also serialized
//     (deploy/observability/spec/, one file for the project and one per
//     service — see ServicePart) so a second renderer — the Reliant console,
//     drawing with uPlot — can consume exactly the same model, through the
//     release bundle, without parsing HyperDX JSON. The panel vocabulary and
//     its validator are published as pkg/dashspec (aliased in dashspec.go), so
//     every reader of a dashboard file judges it by the same rules.
//
//   - RenderHyperDX (hyperdx.go) is one renderer over the Spec: it emits
//     HyperDX v2 dashboard JSON for the file provisioner (local dev, operators)
//     or for the HyperDX API (hosted).
//
// Everything is keyed on OpenTelemetry names (service.name,
// deployment.environment.name, span.name, ...), the contract forge's runtime
// and KCL already settle, so no panel depends on how a given backend stores
// them.
package obsdash

import (
	"fmt"
	"sort"
)

// PartsVersion identifies the shape of the files the Spec is written to
// disk as (ProjectPart, ServicePart).
const PartsVersion = "forge.dashboard-parts/v1"

// withReleaseMarkers declares the releases annotation on d when it has a
// time-series panel to draw it on and does not declare one already.
func withReleaseMarkers(d Dashboard) Dashboard {
	if len(d.Annotations) > 0 {
		return d
	}
	for _, p := range d.Panels {
		if p.Kind == KindLine {
			d.Annotations = []Annotation{{Source: AnnotationReleases, Label: "Releases"}}
			break
		}
	}
	return d
}

// SavedSearch is a named log search a service owner returns to.
type SavedSearch struct {
	ID      string      `json:"id"`
	Title   string      `json:"title"`
	Signal  Signal      `json:"signal"`
	Where   []Predicate `json:"where"`
	Columns []string    `json:"columns,omitempty"`
}

// Spec is everything the generator derives for one project.
type Spec struct {
	Version       string        `json:"version"`
	Project       string        `json:"project"`
	Dashboards    []Dashboard   `json:"dashboards"`
	SavedSearches []SavedSearch `json:"savedSearches"`
}

// Method is one rpc of a service.
type Method struct {
	Name string
}

// Service is one Connect/gRPC service.
type Service struct {
	// Name is the short service name used in file names and titles
	// ("orders").
	Name string
	// Package is the proto package ("orders.v1"); ProtoService the proto
	// service name ("OrdersService"). Together they give the procedure
	// "orders.v1.OrdersService/GetOrder" the RPC interceptors name spans by.
	Package      string
	ProtoService string
	Methods      []Method
}

// procedure returns the full procedure name of m: the span name the
// otelconnect server interceptor uses.
func procedure(s Service, m Method) string {
	return s.Package + "." + s.ProtoService + "/" + m.Name
}

// Input is what Build reads from the project.
type Input struct {
	Project   string
	Services  []Service
	Frontends []string
	// HasDatabase adds the database panels: true once the project has a
	// migration, since the panels read spans the ORM's query hook emits.
	HasDatabase bool
	// Funnels are the declared product funnels. Each becomes one panel on the
	// frontend dashboard, so they only matter when Frontends is non-empty.
	Funnels []FrontendFunnel
}

// FrontendFunnel is one declared funnel (forge.Funnel in KCL).
type FrontendFunnel struct {
	ID    string
	Title string
	Funnel
}

// maxPerRPCRows bounds the per-rpc RED rows: past this a service gets the
// RED table only, because 3 tiles per rpc stops being readable (and
// HyperDX caps a dashboard at 500 tiles).
const maxPerRPCRows = 25

// FrontendServiceName is the service.name the scaffolded frontends report
// (src/lib/otel_gen.ts: '<project>-frontend').
func FrontendServiceName(project string) string { return project + "-frontend" }

// Build derives the project's Spec.
func Build(in Input) Spec {
	return Assemble(BuildParts(in))
}

// ProjectPart is the part of the Spec that belongs to the project as a
// whole: the overview, which leads, and the dashboards that follow the
// services' (Workloads, Releases, the frontend's). See ServicePart.
type ProjectPart struct {
	Version    string      `json:"version"`
	Project    string      `json:"project"`
	Overview   Dashboard   `json:"overview"`
	Dashboards []Dashboard `json:"dashboards"`
}

// ServicePart is everything forge derives for one service's observability:
// its dashboard, its saved searches, and what the default alerts know of it.
//
// The Spec is written to disk as one ProjectPart and one ServicePart per
// service (SpecDir) rather than as one file, because a file that lists every
// service is one that two branches each adding a service both change — at
// the same sorted position whenever the two services are neighbours, and in
// JSON so repetitive that git aligns even distant insertions onto each
// other. The merge then kept one side, and every rebase of a lane that
// adopted a service was followed by a generate that rewrote ~1,500 lines and
// an amend (reliant, P3b). One file per service is disjoint from every other
// branch's, so git merges it — with or without forge's merge driver — into
// exactly what a generate over both branches writes.
type ServicePart struct {
	Version       string        `json:"version"`
	Service       string        `json:"service"`
	Dashboard     Dashboard     `json:"dashboard"`
	SavedSearches []SavedSearch `json:"savedSearches"`
	Alert         AlertService  `json:"alert"`
}

// BuildParts derives the project's Spec as the parts it is written in. A
// service with no rpc has no part. Parts are sorted by service name.
func BuildParts(in Input) (ProjectPart, []ServicePart) {
	project := ProjectPart{
		Version:  PartsVersion,
		Project:  in.Project,
		Overview: withReleaseMarkers(buildOverview(in)),
	}

	services := append([]Service(nil), in.Services...)
	sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })
	var parts []ServicePart
	for _, s := range services {
		if len(s.Methods) == 0 {
			continue
		}
		parts = append(parts, ServicePart{
			Version:   PartsVersion,
			Service:   s.Name,
			Dashboard: withReleaseMarkers(buildService(in, s)),
			SavedSearches: []SavedSearch{{
				ID:      "errors-" + s.Name,
				Title:   fmt.Sprintf("%s: error logs", s.Name),
				Signal:  Logs,
				Where:   append(serviceLogScope(s), errorLogs),
				Columns: logColumns,
			}},
			Alert: AlertService{Name: s.Name, RPCService: s.Package + "." + s.ProtoService},
		})
	}

	project.Dashboards = append(project.Dashboards, BuildWorkloads(in.Project), BuildReleases(in.Project))
	if len(in.Frontends) > 0 {
		project.Dashboards = append(project.Dashboards, buildFrontend(in))
	}
	for i := range project.Dashboards {
		project.Dashboards[i] = withReleaseMarkers(project.Dashboards[i])
	}
	return project, parts
}

// Assemble is the Spec its parts make: the overview, then each service's
// dashboard in service-name order, then the project's other dashboards; the
// saved searches in the same service order.
func Assemble(project ProjectPart, services []ServicePart) Spec {
	sorted := append([]ServicePart(nil), services...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Service < sorted[j].Service })

	sp := Spec{Version: SpecVersion, Project: project.Project, SavedSearches: []SavedSearch{}}
	sp.Dashboards = append(sp.Dashboards, project.Overview)
	for _, s := range sorted {
		sp.Dashboards = append(sp.Dashboards, s.Dashboard)
		sp.SavedSearches = append(sp.SavedSearches, s.SavedSearches...)
	}
	sp.Dashboards = append(sp.Dashboards, project.Dashboards...)
	return sp
}

var (
	errorSpans = Predicate{Field: "span.status", Op: OpEq, Value: "error"}
	errorLogs  = Predicate{Field: "log.severity", Op: OpEq, Value: "error"}
	logColumns = []string{"timestamp", FieldService, "log.severity", "log.body"}

	// rpcServer selects the server side of an rpc: a server span named
	// "<package>.<Service>/<Method>". The name shape (no spaces, one slash)
	// keeps plain HTTP spans ("GET /x") out of an rpc dashboard.
	rpcServer = []Predicate{
		{Field: "span.kind", Op: OpEq, Value: "server"},
		{Field: "span.name", Op: OpMatch, Value: `^[A-Za-z0-9_.]+/[A-Za-z0-9_]+$`},
	}
)

func scoped(base []Predicate, extra ...Predicate) []Predicate {
	out := append([]Predicate(nil), base...)
	return append(out, extra...)
}

func commonFilters() []Filter {
	return []Filter{
		{ID: "service", Label: "service.name", Field: FieldService},
		{ID: "env", Label: "deployment.environment.name", Field: FieldEnv},
	}
}

func serviceLogScope(s Service) []Predicate {
	return []Predicate{{Field: "log.attr.procedure", Op: OpPrefix, Value: "/" + s.Package + "." + s.ProtoService + "/"}}
}

func latencySeries() []Series {
	return []Series{
		{Label: "p50", Agg: AggQuantile, Quantile: 0.5, Field: "span.duration"},
		{Label: "p95", Agg: AggQuantile, Quantile: 0.95, Field: "span.duration"},
		{Label: "p99", Agg: AggQuantile, Quantile: 0.99, Field: "span.duration"},
	}
}

func redTableSeries() []Series {
	s := []Series{
		{Label: "requests", Agg: AggCount},
		{Label: "errors", Agg: AggCount, Where: []Predicate{errorSpans}},
	}
	return append(s, latencySeries()...)
}

func errorRatioSeries() []Series {
	return []Series{
		{Label: "errors", Agg: AggCount, Where: []Predicate{errorSpans}},
		{Label: "requests", Agg: AggCount},
	}
}

func buildOverview(in Input) Dashboard {
	d := Dashboard{
		ID:          "overview",
		Title:       in.Project + ": overview",
		Description: "Request rate, error rate and latency per service, top errors and recent error logs. Generated by forge from the project's services; narrow with the service.name and deployment.environment.name filters.",
		Filters:     commonFilters(),
	}
	d.Panels = []Panel{
		{ID: "requests", Title: "Requests", Kind: KindNumber, Signal: Traces, Where: rpcServer, Series: []Series{{Label: "requests", Agg: AggCount}}, Unit: UnitCount, Width: 6, Height: 4},
		{ID: "errors", Title: "Server errors", Kind: KindNumber, Signal: Traces, Where: scoped(rpcServer, errorSpans), Series: []Series{{Label: "errors", Agg: AggCount}}, Unit: UnitCount, Width: 6, Height: 4},
		{ID: "p95", Title: "p95 latency", Kind: KindNumber, Signal: Traces, Where: rpcServer, Series: []Series{{Label: "p95", Agg: AggQuantile, Quantile: 0.95, Field: "span.duration"}}, Unit: UnitMillis, Width: 6, Height: 4},
		{ID: "p99", Title: "p99 latency", Kind: KindNumber, Signal: Traces, Where: rpcServer, Series: []Series{{Label: "p99", Agg: AggQuantile, Quantile: 0.99, Field: "span.duration"}}, Unit: UnitMillis, Width: 6, Height: 4},
		{ID: "request-rate", Title: "Requests per service", Description: "Count per time bucket.", Kind: KindLine, Signal: Traces, Where: rpcServer, Series: []Series{{Label: "requests", Agg: AggCount}}, GroupBy: []string{FieldService}, Unit: UnitCount, Width: 12, Height: 7},
		{ID: "error-rate", Title: "Error rate per service", Description: "Server faults (span status Error) over all requests. Client errors such as NotFound are not counted.", Kind: KindLine, Signal: Traces, Where: rpcServer, Series: errorRatioSeries(), Ratio: true, GroupBy: []string{FieldService}, Unit: UnitPercent, Width: 12, Height: 7},
		{ID: "latency", Title: "Latency p50 / p95 / p99", Kind: KindLine, Signal: Traces, Where: rpcServer, Series: latencySeries(), Unit: UnitMillis, Width: 24, Height: 7},
		{ID: "red-by-service", Title: "Rate, errors and latency by service", Kind: KindTable, Signal: Traces, Where: rpcServer, Series: redTableSeries(), GroupBy: []string{FieldService}, Width: 24, Height: 8},
		{ID: "top-errors", Title: "Top errors", Kind: KindTable, Signal: Traces, Where: scoped(rpcServer, errorSpans), Series: []Series{{Label: "count", Agg: AggCount}}, GroupBy: []string{"span.name", "span.status_message"}, Width: 24, Height: 8},
	}
	if in.HasDatabase {
		d.Panels = append(d.Panels, dbPanels(24)...)
	}
	d.Panels = append(d.Panels,
		Panel{ID: "error-logs", Title: "Recent error logs", Kind: KindSearch, Signal: Logs, Where: []Predicate{errorLogs}, Columns: logColumns, Width: 24, Height: 9},
	)
	return d
}

func dbPanels(width int) []Panel {
	dbSpans := []Predicate{
		{Field: "span.kind", Op: OpEq, Value: "client"},
		{Field: "span.attr.db.system", Op: OpExists},
	}
	half := width / 2
	return []Panel{
		{ID: "db-latency", Title: "Database call latency", Description: "Client spans carrying db.system (the ORM's query hook).", Kind: KindLine, Signal: Traces, Where: dbSpans, Series: latencySeries(), Unit: UnitMillis, Width: half, Height: 7},
		{ID: "db-by-operation", Title: "Database calls by operation", Kind: KindTable, Signal: Traces, Where: dbSpans, Series: []Series{
			{Label: "calls", Agg: AggCount},
			{Label: "errors", Agg: AggCount, Where: []Predicate{errorSpans}},
			{Label: "p95", Agg: AggQuantile, Quantile: 0.95, Field: "span.duration"},
		}, GroupBy: []string{"span.attr.db.operation"}, Width: half, Height: 7},
	}
}

func buildService(in Input, s Service) Dashboard {
	prefix := Predicate{Field: "span.name", Op: OpPrefix, Value: s.Package + "." + s.ProtoService + "/"}
	base := scoped(rpcServer, prefix)
	d := Dashboard{
		ID:          "service-" + s.Name,
		Title:       fmt.Sprintf("%s: %s service", in.Project, s.Name),
		Description: fmt.Sprintf("RED per rpc of %s.%s (server spans named %s.%s/<Method>), Go runtime and logs. The Go runtime panels describe the PROCESS that serves this service: narrow with the service.name filter.", s.Package, s.ProtoService, s.Package, s.ProtoService),
		Filters:     commonFilters(),
	}
	d.Panels = []Panel{
		{ID: "requests", Title: "Requests", Kind: KindNumber, Signal: Traces, Where: base, Series: []Series{{Label: "requests", Agg: AggCount}}, Unit: UnitCount, Width: 8, Height: 4},
		{ID: "errors", Title: "Server errors", Kind: KindNumber, Signal: Traces, Where: scoped(base, errorSpans), Series: []Series{{Label: "errors", Agg: AggCount}}, Unit: UnitCount, Width: 8, Height: 4},
		{ID: "p95", Title: "p95 latency", Kind: KindNumber, Signal: Traces, Where: base, Series: []Series{{Label: "p95", Agg: AggQuantile, Quantile: 0.95, Field: "span.duration"}}, Unit: UnitMillis, Width: 8, Height: 4},
		{ID: "red-by-rpc", Title: "Rate, errors and latency by rpc", Kind: KindTable, Signal: Traces, Where: base, Series: redTableSeries(), GroupBy: []string{"span.name"}, Width: 24, Height: 8},
	}
	if len(s.Methods) <= maxPerRPCRows {
		methods := append([]Method(nil), s.Methods...)
		sort.Slice(methods, func(i, j int) bool { return methods[i].Name < methods[j].Name })
		for _, m := range methods {
			w := scoped(rpcServer, Predicate{Field: "span.name", Op: OpEq, Value: procedure(s, m)})
			d.Panels = append(d.Panels,
				Panel{ID: "rpc-" + m.Name + "-rate", Title: m.Name + ": requests", Kind: KindLine, Signal: Traces, Where: w, Series: []Series{{Label: "requests", Agg: AggCount}}, Unit: UnitCount, Width: 8, Height: 6},
				Panel{ID: "rpc-" + m.Name + "-errors", Title: m.Name + ": error rate", Kind: KindLine, Signal: Traces, Where: w, Series: errorRatioSeries(), Ratio: true, Unit: UnitPercent, Width: 8, Height: 6},
				Panel{ID: "rpc-" + m.Name + "-latency", Title: m.Name + ": latency", Kind: KindLine, Signal: Traces, Where: w, Series: latencySeries(), Unit: UnitMillis, Width: 8, Height: 6},
			)
		}
	}
	if in.HasDatabase {
		d.Panels = append(d.Panels, dbPanels(24)...)
	}
	d.Panels = append(d.Panels, runtimePanels()...)
	d.Panels = append(d.Panels,
		Panel{ID: "error-logs", Title: "Error logs", Kind: KindSearch, Signal: Logs, Where: append(serviceLogScope(s), errorLogs), Columns: logColumns, Width: 24, Height: 9},
		Panel{ID: "logs", Title: "Logs", Description: "Every record whose rpc procedure belongs to this service.", Kind: KindSearch, Signal: Logs, Where: serviceLogScope(s), Columns: logColumns, Width: 24, Height: 9},
	)
	return d
}

// runtimePanels are the Go runtime metrics the OTel runtime instrumentation
// exports (go.opentelemetry.io/contrib/instrumentation/runtime), a subset of
// HyperDX's own Go Runtime template.
func runtimePanels() []Panel {
	return []Panel{
		{ID: "go-memory", Title: "Go memory used", Kind: KindLine, Signal: Metrics, Series: []Series{{Label: "used", Agg: AggSum, Field: "go.memory.used", MetricType: "sum"}, {Label: "gc goal", Agg: AggAvg, Field: "go.memory.gc.goal", MetricType: "sum"}}, Unit: UnitBytes, Width: 12, Height: 7},
		{ID: "go-goroutines", Title: "Goroutines", Kind: KindLine, Signal: Metrics, Series: []Series{{Label: "goroutines", Agg: AggSum, Field: "go.goroutine.count", MetricType: "sum"}}, Unit: UnitCount, Width: 12, Height: 7},
		{ID: "go-alloc", Title: "Heap allocation", Description: "Bytes allocated to the heap per time bucket: the rate the garbage collector has to keep up with.", Kind: KindLine, Signal: Metrics, Series: []Series{{Label: "allocated", Agg: AggSum, Field: "go.memory.allocated", MetricType: "sum"}}, Unit: UnitBytes, Width: 24, Height: 7},
	}
}

var (
	browserSpans = []Predicate{{Field: "resource.attr.rum.sessionId", Op: OpExists}}
	pageLoads    = Predicate{Field: "span.name", Op: OpIn, Values: []string{"documentLoad", "routeChange"}}
	ajax         = Predicate{Field: "span.attr.component", Op: OpIn, Values: []string{"fetch", "xml-http-request"}}
	notAjax      = Predicate{Field: "span.attr.component", Op: OpNotIn, Values: []string{"fetch", "xml-http-request"}}
	ajaxFailed   = Predicate{Field: "span.attr.http.status_code", Op: OpGTE, Value: "400"}
)

func buildFrontend(in Input) Dashboard {
	d := Dashboard{
		ID:          "frontend",
		Title:       in.Project + ": frontend",
		Description: fmt.Sprintf("Browser errors, page loads, web vitals and API calls from the HyperDX browser SDK (service.name %s). Panels follow HyperDX's Browser RUM template: a browser span carries the rum.sessionId resource attribute.", FrontendServiceName(in.Project)),
		Filters:     commonFilters(),
	}
	js := scoped(browserSpans, errorSpans, notAjax)
	loads := scoped(browserSpans, Predicate{Field: "span.name", Op: OpEq, Value: "documentLoad"})
	webvitals := Predicate{Field: "span.name", Op: OpEq, Value: "webvitals"}
	vital := func(id, title, key string, mantissa Unit) Panel {
		return Panel{ID: id, Title: title, Kind: KindNumber, Signal: Traces,
			Where:  scoped(browserSpans, webvitals, Predicate{Field: "span.attr." + key, Op: OpExists}),
			Series: []Series{{Label: key + " p75", Agg: AggQuantile, Quantile: 0.75, Field: "span.attr." + key}}, Unit: mantissa, Width: 6, Height: 4}
	}
	// p75 per route, graded against the Core Web Vitals thresholds. Each
	// webvitals span carries one metric, so a series' own "exists" keeps the
	// other metrics' spans out of its quantile.
	routeVital := func(key string, good, poor float64) Series {
		return Series{Label: key + " p75", Agg: AggQuantile, Quantile: 0.75, Field: "span.attr." + key,
			Where: []Predicate{{Field: "span.attr." + key, Op: OpExists}}, Thresholds: &Thresholds{Good: good, Poor: poor}}
	}
	d.Panels = []Panel{
		{ID: "js-errors", Title: "JS errors", Kind: KindNumber, Signal: Traces, Where: js, Series: []Series{{Label: "errors", Agg: AggCount}}, Unit: UnitCount, Width: 6, Height: 4},
		{ID: "sessions", Title: "Sessions", Kind: KindNumber, Signal: Traces, Where: browserSpans, Series: []Series{{Label: "sessions", Agg: AggCountDistinct, Field: "resource.attr.rum.sessionId"}}, Unit: UnitCount, Width: 6, Height: 4},
		{ID: "page-views", Title: "Page views", Kind: KindNumber, Signal: Traces, Where: scoped(browserSpans, pageLoads), Series: []Series{{Label: "views", Agg: AggCount}}, Unit: UnitCount, Width: 6, Height: 4},
		{ID: "api-failures", Title: "Failed API calls", Description: "fetch / XHR spans with an HTTP status of 400 or more.", Kind: KindNumber, Signal: Traces, Where: scoped(browserSpans, ajax, ajaxFailed), Series: []Series{{Label: "failures", Agg: AggCount}}, Unit: UnitCount, Width: 6, Height: 4},
		{ID: "errors-over-time", Title: "JS errors over time", Kind: KindLine, Signal: Traces, Where: js, Series: []Series{{Label: "errors", Agg: AggCount}}, Unit: UnitCount, Width: 24, Height: 6},
		{ID: "issues", Title: "Issues", Description: "Errors grouped by cause (type, message and the top in-app frames): new and regressed first, then the most frequent. A string-only console.error is not an issue.", Kind: KindIssues, Signal: Traces, Where: browserSpans, Width: 24, Height: 9},
		{ID: "sessions-with-errors", Title: "Sessions with errors", Description: "Sessions that hit an error, newest first; a session that was recorded links to its replay.", Kind: KindSessions, Signal: Traces, Where: browserSpans, HasError: true, Width: 24, Height: 8},
		vital("lcp", "LCP p75", "lcp", UnitMillis),
		vital("inp", "INP p75", "inp", UnitMillis),
		vital("cls", "CLS p75", "cls", UnitNone),
		vital("fcp", "FCP p75", "fcp", UnitMillis),
		vital("ttfb", "TTFB p75", "ttfb", UnitMillis),
		{ID: "vitals-by-route", Title: "Web vitals by route", Description: "p75 per page. LCP good at 2500 ms or less and poor above 4000 ms; INP 200 / 500 ms; CLS 0.1 / 0.25.", Kind: KindTable, Signal: Traces, Where: scoped(browserSpans, webvitals),
			Series:  []Series{routeVital("lcp", 2500, 4000), routeVital("inp", 200, 500), routeVital("cls", 0.1, 0.25), {Label: "samples", Agg: AggCount, Where: []Predicate{{Field: "span.attr.lcp", Op: OpExists}}}},
			GroupBy: []string{"span.attr.location.href"}, Width: 24, Height: 8},
		{ID: "errors-by-release", Title: "JS errors by release", Description: "The same error spans as above, one line per service.version (the build id), so a release that introduced a regression shows as its own line. Release markers are drawn from the ledger.", Kind: KindLine, Signal: Traces, Where: js, Series: []Series{{Label: "errors", Agg: AggCount}}, GroupBy: []string{"resource.attr.service.version"}, Unit: UnitCount, Width: 24, Height: 7},
		{ID: "page-load", Title: "Page load p50 / p90 / p99", Kind: KindLine, Signal: Traces, Where: loads, Series: []Series{
			{Label: "p50", Agg: AggQuantile, Quantile: 0.5, Field: "span.duration"},
			{Label: "p90", Agg: AggQuantile, Quantile: 0.9, Field: "span.duration"},
			{Label: "p99", Agg: AggQuantile, Quantile: 0.99, Field: "span.duration"},
		}, Unit: UnitMillis, Width: 24, Height: 7},
		{ID: "api-latency", Title: "API call latency (browser)", Description: "Browser-side fetch / XHR spans; the same calls appear server-side on the service dashboards, joined by traceparent.", Kind: KindLine, Signal: Traces, Where: scoped(browserSpans, ajax), Series: latencySeries(), Unit: UnitMillis, Width: 12, Height: 7},
		{ID: "api-calls", Title: "API calls by URL", Kind: KindTable, Signal: Traces, Where: scoped(browserSpans, ajax), Series: []Series{
			{Label: "calls", Agg: AggCount},
			{Label: "failures", Agg: AggCount, Where: []Predicate{ajaxFailed}},
			{Label: "p95", Agg: AggQuantile, Quantile: 0.95, Field: "span.duration"},
		}, GroupBy: []string{"span.attr.http.url"}, Width: 12, Height: 7},
		{ID: "browser-console", Title: "Browser console and error events", Description: "The HyperDX SDK ships console output and uncaught errors as spans (console.*, onerror, unhandledrejection), not as OTLP logs.", Kind: KindSearch, Signal: Traces, Where: scoped(browserSpans, Predicate{Field: "span.name", Op: OpIn, Values: []string{"console.error", "console.warn", "onerror", "unhandledrejection"}}), Columns: []string{"timestamp", FieldService, "span.name", "span.attr.error.message"}, Width: 24, Height: 9},
	}
	for _, f := range in.Funnels {
		fn := f.Funnel
		if fn.Window == "" {
			fn.Window = DefaultFunnelWindow
		}
		if fn.By == "" {
			fn.By = DefaultFunnelBy
		}
		title := f.Title
		if title == "" {
			title = f.ID
		}
		d.Panels = append(d.Panels, Panel{ID: "funnel-" + f.ID, Title: title, Description: fmt.Sprintf("Product events in order, within %s, counted per %s.", fn.Window, fn.By), Kind: KindFunnel, Signal: Traces, Funnel: &fn, Width: 24, Height: 8})
	}
	return d
}
