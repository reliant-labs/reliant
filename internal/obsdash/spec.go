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
//     service — see ProcessPart) so a second renderer — the Reliant console,
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
	"strings"

	"github.com/reliant-labs/forge/internal/naming"
)

// PartsVersion identifies the shape of the files the Spec is written to
// disk as (ProjectPart, ProcessPart).
const PartsVersion = "forge.dashboard-parts/v2"

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

// Input is what Build reads from the project.
type Input struct {
	Project   string
	Services  []Service
	Processes []Process
	Frontends []string
	// FrontendServices, when set, are the service.names those frontends report
	// (a frontend built from another repository bakes its own); empty derives
	// each from the project and frontend name (naming.FrontendServiceName).
	FrontendServices []string
	// HasDatabase adds the database panels: true once the project has a
	// migration, since the panels read spans the ORM's query hook emits.
	HasDatabase bool
	// Funnels are the declared product funnels. Each becomes one panel on the
	// frontend dashboard, so they only matter when Frontends is non-empty.
	Funnels []FrontendFunnel
	// Deployed scopes the overview, together with Processes' own identities,
	// when the per-process inventory is not being built: forge generate knows
	// every environment's deployed service.names and workloads, but per-process
	// parts are the selected environment's alone (built at bundle time, see
	// ProcessPart). Without it a generate-time overview filtered on the
	// no-deployed-process sentinel and drew nothing.
	Deployed Identities
}

// Identities are the telemetry and workload names deployed processes run
// under: what the overview's panels are scoped to.
type Identities struct {
	ServiceNames []string
	Workloads    []string
	// RPCServiceNames are the service.names of the processes that serve RPCs:
	// the only ones an rpc.server.* panel can have data for.
	RPCServiceNames []string
	// CPULimitWorkloads and MemoryLimitWorkloads are the workloads that
	// declare that limit: the only ones a *_limit_utilization panel can have
	// data for.
	CPULimitWorkloads    []string
	MemoryLimitWorkloads []string
}

// FrontendFunnel is one declared funnel (forge.Funnel in KCL).
type FrontendFunnel struct {
	ID    string
	Title string
	Funnel
}

// frontendServices are the service.names the project's web frontends report,
// in declaration order: naming.FrontendServiceName per frontend, the same
// derivation that bakes SERVICE_NAME into each frontend's otel_gen.ts.
func frontendServices(project string, frontends []string) []string {
	var out []string
	for _, fe := range frontends {
		out = append(out, naming.FrontendServiceName(project, fe))
	}
	return out
}

// frontendServiceNames are the service.names the frontends report.
func (in Input) frontendServiceNames() []string {
	if len(in.FrontendServices) > 0 {
		return in.FrontendServices
	}
	return frontendServices(in.Project, in.Frontends)
}

// WithoutProcessDashboards is sp for an environment that deploys no process: a
// static site has nothing for the overview, workloads and releases dashboards
// to measure (the overview would filter on the no-deployed-process sentinel),
// so only the dashboards that do not need a process remain.
func WithoutProcessDashboards(sp Spec) Spec {
	kept := sp.Dashboards[:0:0]
	for _, d := range sp.Dashboards {
		if d.ID == "frontend" {
			kept = append(kept, d)
		}
	}
	sp.Dashboards = kept
	return sp
}

// Build derives the project's Spec.
func Build(in Input) Spec {
	return Assemble(BuildParts(in))
}

// ProjectPart is the part of the Spec that belongs to the project as a
// whole: the overview, which leads, and the dashboards that follow the
// services' (Workloads, Releases, the frontend's). See ProcessPart.
type ProjectPart struct {
	// Input retains reusable generation fragments; selected-env assembly fills
	// Processes from rendered KCL, never from stale disk process parts.
	Input      *Fragments  `json:"input,omitempty"`
	Version    string      `json:"version"`
	Project    string      `json:"project"`
	Overview   Dashboard   `json:"overview"`
	Dashboards []Dashboard `json:"dashboards"`
}

// Fragments holds only project-wide generation inputs. Per-service alert
// inputs live in separate parts, and dashboard RPC ownership comes from the
// rendered workloads (Process.RPCServices). Duplicating the service inventory
// here would make independent service additions change the same shared JSON;
// the merge driver keeps one side's certified render, losing the other addition.
type Fragments struct {
	Frontends        []string
	FrontendServices []string `json:",omitempty"`
	HasDatabase      bool
	Funnels          []FrontendFunnel
}

// ProcessPart holds one deployed executable/role's dashboard and saved searches.
// Separate files keep unrelated process changes disjoint; RPC alert inputs are
// independent alert-service parts, never duplicated for each mounting process.
type ProcessPart struct {
	Version       string        `json:"version"`
	Process       Process       `json:"process"`
	Dashboard     Dashboard     `json:"dashboard"`
	SavedSearches []SavedSearch `json:"savedSearches"`
}

// Process is a deployed executable and command/root, with the exact telemetry
// and workload identities it owns across environments. No RPC ownership is
// inferred from a package name, a workload name, or handler reach alone.
type Process struct {
	// Name is the forge workload name (the first, when several workloads run
	// the same executable and command): "admin-api", never the rendered
	// Kubernetes object name, the command line or a hash. It names the
	// process's dashboard ("process-<name>").
	Name string `json:"name"`
	// Kind is the forge workload kind: service, worker, operator, job or
	// cron. Empty is a service. Jobs and crons share one Jobs dashboard.
	Kind         string   `json:"kind,omitempty"`
	Binary       string   `json:"binary"`
	Args         []string `json:"args,omitempty"`
	Root         string   `json:"root,omitempty"`
	ServiceNames []string `json:"serviceNames"`
	Workloads    []string `json:"workloads"`
	RPCServices  []string `json:"rpcServices,omitempty"`
	HasDatabase  bool     `json:"hasDatabase,omitempty"`
	// CPULimit and MemoryLimit say the workload declares that resource limit,
	// the only case the node agent's *_limit_utilization series exist.
	CPULimit    bool `json:"cpuLimit,omitempty"`
	MemoryLimit bool `json:"memoryLimit,omitempty"`
	// NodeMetadata says the process runs where the node agent delivers
	// per-container metadata (a cluster or hosted runtime); a host or compose
	// process has no saturation or restart series.
	NodeMetadata bool `json:"nodeMetadata,omitempty"`
	// ServesRPCDeclared is set for a Go process forge does not build (a sibling
	// repository's, built by a ShellBuild): a service with a port is taken to
	// serve RPCs, since forge cannot read its mounts.
	ServesRPCDeclared bool `json:"servesRpcDeclared,omitempty"`
	// NonGo is set for a process forge can tell is not a Go program of this
	// project or its siblings (a third-party or Docker-built image): it has no
	// Go runtime metrics and no RPC metrics to draw.
	NonGo bool `json:"nonGo,omitempty"`
	// Replaces are the names (dashboard ids minus "process-") this process
	// was known by before dashboards were named for the workload.
	Replaces []string `json:"replaces,omitempty"`
}

// ServesRPC reports whether p serves RPCs, the only processes an
// rpc.server.* panel has data for.
func (p Process) ServesRPC() bool { return len(p.RPCServices) > 0 || p.ServesRPCDeclared }

// Kinds a Process can have (the forge workload kinds that run something).
const (
	KindServiceProc  = "service"
	KindWorkerProc   = "worker"
	KindOperatorProc = "operator"
	KindJobProc      = "job"
	KindCronProc     = "cron"
)

// IsJob reports whether p runs to completion (a job or a cron), and so is
// drawn on the Jobs dashboard rather than on a dashboard of its own.
func (p Process) IsJob() bool { return p.Kind == KindJobProc || p.Kind == KindCronProc }

// runsWork reports whether p runs worker loops (worker.<name>.run root spans).
func (p Process) runsWork() bool { return p.Kind == KindWorkerProc || p.Kind == KindOperatorProc }

// BuildParts derives the project's Spec as process parts, sorted by process
// identity. RPC-free workers and gateways retain their runtime/workload panels.
func BuildParts(in Input) (ProjectPart, []ProcessPart) {
	fragments := Fragments{Frontends: in.Frontends, FrontendServices: in.FrontendServices, HasDatabase: in.HasDatabase, Funnels: in.Funnels}
	project := ProjectPart{
		Input:    &fragments,
		Version:  PartsVersion,
		Project:  in.Project,
		Overview: withReleaseMarkers(buildOverview(in)),
	}

	processes := append([]Process(nil), in.Processes...)
	sort.Slice(processes, func(i, j int) bool { return processes[i].Name < processes[j].Name })
	var parts []ProcessPart
	var jobs []Process
	for _, p := range processes {
		if p.IsJob() {
			jobs = append(jobs, p)
			continue
		}
		parts = append(parts, ProcessPart{
			Version:   PartsVersion,
			Process:   p,
			Dashboard: withReleaseMarkers(buildProcess(in, p)),
			SavedSearches: []SavedSearch{{
				ID:      "errors-process-" + p.Name,
				Title:   fmt.Sprintf("%s: error logs", p.Name),
				Signal:  Logs,
				Where:   scoped(processScope(p), errorLogs),
				Columns: logColumns,
			}},
		})
	}

	project.Dashboards = append(project.Dashboards, BuildWorkloads(in.Project), BuildReleases(in.Project))
	if len(jobs) > 0 {
		project.Dashboards = append(project.Dashboards, BuildJobs(in.Project, jobs))
	}
	if len(in.Frontends) > 0 || len(in.FrontendServices) > 0 {
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
func Assemble(project ProjectPart, processes []ProcessPart) Spec {
	sorted := append([]ProcessPart(nil), processes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Process.Name < sorted[j].Process.Name })

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

func buildOverview(in Input) Dashboard {
	d := Dashboard{
		ID:          "overview",
		Title:       in.Project + ": overview",
		Description: "Metric-backed request, server-fault and duration health across deployed processes; saturation, errors and releases. Counters are unsampled; trace drilldowns may be sampled.",
		Group:       GroupOverview,
		Filters:     commonFilters(),
	}
	// Jobs and crons have their own dashboard; the overview is the long-running
	// processes. Each panel is scoped to the processes it can have data for: RPC
	// panels to those that serve RPCs, a limit panel to those that declare it.
	services := append([]string(nil), in.Deployed.ServiceNames...)
	workloads := append([]string(nil), in.Deployed.Workloads...)
	rpcServices := append([]string(nil), in.Deployed.RPCServiceNames...)
	cpuLimited := append([]string(nil), in.Deployed.CPULimitWorkloads...)
	memLimited := append([]string(nil), in.Deployed.MemoryLimitWorkloads...)
	for _, p := range in.Processes {
		if p.IsJob() {
			continue
		}
		services = append(services, p.ServiceNames...)
		workloads = append(workloads, p.Workloads...)
		if p.ServesRPC() {
			rpcServices = append(rpcServices, p.ServiceNames...)
		}
		if p.CPULimit {
			cpuLimited = append(cpuLimited, p.Workloads...)
		}
		if p.MemoryLimit {
			memLimited = append(memLimited, p.Workloads...)
		}
	}
	services, workloads, rpcServices = sortedIdentities(services), sortedIdentities(workloads), sortedIdentities(rpcServices)
	cpuLimited, memLimited = sortedIdentities(cpuLimited), sortedIdentities(memLimited)

	if len(rpcServices) > 0 {
		rpc := rpcHealthPanels()
		rpc = append(rpc, Panel{ID: "top-errors", Title: "Top server faults", Kind: KindTable, Signal: Traces, Where: scoped(rpcServer, errorSpans), Series: []Series{{Label: "sampled faults", Agg: AggCount}}, GroupBy: []string{FieldService, "span.name", "span.status_message"}, Limit: 20, Width: 24, Height: 7})
		for i := range rpc {
			if rpc[i].Kind == KindLine {
				rpc[i].GroupBy = []string{FieldService}
			}
			rpc[i].Where = scoped(identityScope(FieldService, rpcServices), rpc[i].Where...)
		}
		d.Panels = append(d.Panels, rpc...)
	}
	limits := map[string][]string{"cpu-vs-limit": cpuLimited, "memory-vs-limit": memLimited, "restarts": workloads}
	var nodePanels []string
	for _, p := range BuildWorkloads(in.Project).Panels {
		if scope, ok := limits[p.ID]; ok && (p.ID == "restarts" || len(scope) > 0) {
			p.Where = scoped(identityScope(fieldWorkload, scope), p.Where...)
			d.Panels = append(d.Panels, p)
			nodePanels = append(nodePanels, p.ID)
		}
	}
	for _, p := range BuildReleases(in.Project).Panels {
		if p.ID == "request-rate-by-version" {
			p.Where = scoped(identityScope(FieldService, services), p.Where...)
			d.Panels = append(d.Panels, p)
		}
	}
	d.Panels = append(d.Panels,
		Panel{ID: "error-logs", Title: "Recent error logs", Kind: KindSearch, Signal: Logs, Where: scoped(identityScope(FieldService, services), errorLogs), Columns: logColumns, Width: 24, Height: 9},
	)
	splitServiceFilter(&d, nodePanels, services, workloads)
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

func buildProcess(in Input, p Process) Dashboard {
	d := Dashboard{ID: ProcessDashboardID(p.Name), Title: in.Project + ": " + p.Name,
		Description: "The " + processKind(p) + " " + p.Name + ". Panels are chosen by what it does: RPC health if it serves RPCs, worker runs if it runs workers, saturation against the limits it declares; runtime and logs always.",
		Group:       GroupServices, Tags: []string{processKind(p)}, Filters: commonFilters()}
	for _, old := range p.Replaces {
		if id := ProcessDashboardID(old); id != d.ID {
			d.Replaces = append(d.Replaces, id)
		}
	}
	d.Sections = []Section{{ID: "runtime", Title: "Runtime"}, {ID: "workload", Title: "Workload saturation"}, {ID: "logs", Title: "Logs"}}
	if p.runsWork() {
		d.Sections = append([]Section{{ID: "runs", Title: "Worker runs"}}, d.Sections...)
	}
	if p.ServesRPC() {
		d.Sections = append([]Section{{ID: "rpc", Title: "RPC health"}}, d.Sections...)
		d.Panels = rpcHealthPanels()
		d.Panels = append(d.Panels,
			Panel{ID: "red-by-rpc", Title: "Rate, errors and latency by RPC", Kind: KindTable, Signal: Traces, Where: rpcServer, Series: redTableSeries(), GroupBy: []string{"span.name"}, Limit: 50, Width: 24, Height: 8})
	}
	for i := range d.Panels {
		d.Panels[i].Section = "rpc"
	}
	if p.runsWork() {
		for _, panel := range workerPanels() {
			panel.Section = "runs"
			d.Panels = append(d.Panels, panel)
		}
	}
	if p.HasDatabase {
		d.Sections = append(d.Sections, Section{ID: "database", Title: "Database"})
		for _, panel := range dbPanels(24) {
			panel.Section = "database"
			d.Panels = append(d.Panels, panel)
		}
	}
	if !p.NonGo {
		for _, panel := range runtimePanels() {
			panel.Section = "runtime"
			d.Panels = append(d.Panels, panel)
		}
	}
	d.Panels = append(d.Panels,
		Panel{ID: "error-logs", Section: "logs", Title: "Error logs", Kind: KindSearch, Signal: Logs, Where: []Predicate{errorLogs}, Columns: logColumns, Width: 24, Height: 8},
		Panel{ID: "logs", Section: "logs", Title: "Process logs", Kind: KindSearch, Signal: Logs, Columns: logColumns, Width: 24, Height: 8})
	for i := range d.Panels {
		d.Panels[i].Where = append(processScope(p), d.Panels[i].Where...)
	}
	// Node-derived metadata is keyed by workload, not the application's
	// service.name. A renamed OTEL_SERVICE_NAME must not blank saturation.
	// Only where the node agent reports at all, and a limit panel only for a
	// declared limit: a request-only workload is drawn against its request.
	var nodePanels []string
	if p.NodeMetadata {
		want := map[string]bool{"restarts": true}
		if p.CPULimit {
			want["cpu-vs-limit"] = true
		} else {
			want["cpu-vs-request"] = true
		}
		if p.MemoryLimit {
			want["memory-vs-limit"] = true
		} else {
			want["memory-vs-request"] = true
		}
		for _, panel := range BuildWorkloads(in.Project).Panels {
			if !want[panel.ID] {
				continue
			}
			panel.Section = "workload"
			panel.Where = scoped(identityScope(fieldWorkload, p.Workloads), panel.Where...)
			d.Panels = append(d.Panels, panel)
			nodePanels = append(nodePanels, panel.ID)
		}
	}
	if p.ServesRPC() {
		var metricPanels []string
		for _, panel := range d.Panels {
			if panel.Section == "rpc" && panel.Signal == Metrics {
				metricPanels = append(metricPanels, panel.ID)
			}
		}
		d.Filters = append(d.Filters,
			Filter{ID: "rpc-procedure", Label: "RPC procedure (metrics)", Field: "metric.attr.procedure", Signal: Metrics, PanelIDs: metricPanels},
			Filter{ID: "rpc-service", Label: "RPC service (traces)", Field: "span.attr.rpc.service", Signal: Traces, PanelIDs: []string{"red-by-rpc"}},
			Filter{ID: "rpc-method", Label: "RPC method (traces)", Field: "span.attr.rpc.method", Signal: Traces, PanelIDs: []string{"red-by-rpc"}})
	}
	splitServiceFilter(&d, nodePanels, p.ServiceNames, p.Workloads)
	return d
}

// ProcessDashboardID is the dashboard id of the process named name.
func ProcessDashboardID(name string) string { return "process-" + name }

func processKind(p Process) string {
	if p.Kind == "" {
		return KindServiceProc
	}
	return p.Kind
}

// workerPanels are the runs of the process's worker loops: every
// lifecyclekit.Every / observe.RunRoot tick is a root span named
// "worker.<name>.<what>".
func workerPanels() []Panel {
	runs := Predicate{Field: "span.name", Op: OpPrefix, Value: "worker."}
	return []Panel{
		{ID: "worker-runs", Title: "Worker runs", Description: "Root spans of the process's worker loops (worker.<name>.*), one line per loop.", Kind: KindLine, Signal: Traces, Where: []Predicate{runs}, Series: []Series{{Label: "runs", Agg: AggCount}}, GroupBy: []string{"span.name"}, Unit: UnitCount, Width: 12, Height: 7},
		{ID: "worker-failures", Title: "Worker run failures", Description: "Runs that ended with an error, one line per loop.", Kind: KindLine, Signal: Traces, Where: []Predicate{runs, errorSpans}, Series: []Series{{Label: "failed", Agg: AggCount}}, GroupBy: []string{"span.name"}, Unit: UnitCount, Width: 12, Height: 7},
		{ID: "worker-duration", Title: "Worker run duration", Kind: KindTable, Signal: Traces, Where: []Predicate{runs}, Series: append([]Series{{Label: "runs", Agg: AggCount}, {Label: "failed", Agg: AggCount, Where: []Predicate{errorSpans}}}, latencySeries()...), GroupBy: []string{"span.name"}, Limit: 50, Width: 24, Height: 8},
	}
}

func processScope(p Process) []Predicate {
	return identityScope(FieldService, p.ServiceNames)
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
	pageLoads  = Predicate{Field: "span.name", Op: OpIn, Values: []string{"documentLoad", "routeChange"}}
	ajax       = Predicate{Field: "span.attr.component", Op: OpIn, Values: []string{"fetch", "xml-http-request"}}
	notAjax    = Predicate{Field: "span.attr.component", Op: OpNotIn, Values: []string{"fetch", "xml-http-request"}}
	ajaxFailed = Predicate{Field: "span.attr.http.status_code", Op: OpGTE, Value: "400"}
)

func buildFrontend(in Input) Dashboard {
	services := in.frontendServiceNames()
	d := Dashboard{
		ID:          "frontend",
		Title:       in.Project + ": frontend",
		Description: fmt.Sprintf("Browser errors, page loads, web vitals and API calls from the HyperDX browser SDK (service.name %s). Panels follow HyperDX's Browser RUM template: a browser span carries the rum.sessionId resource attribute.", strings.Join(services, ", ")),
		Group:       GroupProduct,
		Tags:        []string{"browser"},
		Filters:     commonFilters(),
	}
	// A browser span is one of THIS project's frontends' (service.name, the
	// value otel_gen.ts reports) that carries a RUM session. The service scope
	// keeps another project's browser spans in the same store off the panels.
	browserSpans := []Predicate{
		{Field: FieldService, Op: OpIn, Values: services},
		{Field: "resource.attr.rum.sessionId", Op: OpExists},
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
