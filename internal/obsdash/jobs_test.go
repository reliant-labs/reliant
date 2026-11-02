package obsdash

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func panelIDs(d Dashboard) []string {
	var ids []string
	for _, p := range d.Panels {
		ids = append(ids, p.ID)
	}
	return ids
}

func hasPanel(d Dashboard, id string) bool { _, ok := findPanel(d, id); return ok }

func dashboardByID(t *testing.T, sp Spec, id string) Dashboard {
	t.Helper()
	for _, d := range sp.Dashboards {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("no dashboard %q in %v", id, dashboardIDs(sp))
	return Dashboard{}
}

func dashboardIDs(sp Spec) []string {
	var ids []string
	for _, d := range sp.Dashboards {
		ids = append(ids, d.ID)
	}
	return ids
}

func usesMetric(d Dashboard, metric string) bool {
	for _, p := range d.Panels {
		for _, s := range p.Series {
			if s.Field == metric {
				return true
			}
		}
	}
	return false
}

// fleet is a project with one process of every kind, as control-plane's prod
// env declares them.
func fleet() Input {
	proc := func(name, kind string, mod func(*Process)) Process {
		p := Process{Name: name, Kind: kind, Binary: "app", Args: []string{name}, ServiceNames: []string{name}, Workloads: []string{name}, NodeMetadata: true}
		if mod != nil {
			mod(&p)
		}
		return p
	}
	return Input{
		Project: "shop",
		Processes: []Process{
			proc("admin-api", "service", func(p *Process) {
				p.RPCServices = []string{"orders.v1.OrdersService"}
				p.HasDatabase, p.CPULimit, p.MemoryLimit = true, true, true
			}),
			proc("workers", "worker", func(p *Process) { p.CPULimit = true }),
			proc("workspace", "operator", nil),
			proc("db-migrate-up", "job", nil),
			proc("dbrole-provision-reliant", "job", nil),
			proc("cloudsql-backup-prune", "cron", nil),
		},
		Frontends: []string{"web"},
	}
}

func TestJobsAndCronsShareOneDashboardAndGetNoneOfTheirOwn(t *testing.T) {
	sp := Build(fleet())
	var processDashboards []string
	for _, d := range sp.Dashboards {
		if strings.HasPrefix(d.ID, "process-") {
			processDashboards = append(processDashboards, d.ID)
		}
	}
	want := []string{"process-admin-api", "process-workers", "process-workspace"}
	if !reflect.DeepEqual(processDashboards, want) {
		t.Fatalf("process dashboards = %v, want only the long-running processes %v", processDashboards, want)
	}
	jobs := dashboardByID(t, sp, "jobs")
	if jobs.Group != GroupJobs {
		t.Errorf("jobs group = %q", jobs.Group)
	}
	body, _ := json.Marshal(jobs)
	for _, name := range []string{"db-migrate-up", "dbrole-provision-reliant", "cloudsql-backup-prune"} {
		if !strings.Contains(string(body), name) {
			t.Errorf("the Jobs dashboard never mentions %s", name)
		}
	}
	for _, name := range []string{"admin-api", "workers", "workspace"} {
		if strings.Contains(string(body), `"`+name+`"`) {
			t.Errorf("the Jobs dashboard includes the long-running process %s", name)
		}
	}
}

func TestNoJobsMeansNoJobsDashboard(t *testing.T) {
	in := fleet()
	in.Processes = in.Processes[:3]
	for _, id := range dashboardIDs(Build(in)) {
		if id == "jobs" {
			t.Fatal("a project with no job or cron gets no Jobs dashboard")
		}
	}
}

func TestJobsDashboardContents(t *testing.T) {
	jobs := dashboardByID(t, Build(fleet()), "jobs")
	for _, id := range []string{"jobs-summary", "runs-totals", "runs-by-job", "failures-by-job", "recent-runs", "restarts", "warning-events", "error-logs"} {
		if !hasPanel(jobs, id) {
			t.Errorf("Jobs dashboard lacks panel %q; has %v", id, panelIDs(jobs))
		}
	}
	// A job exits before the runtime exporter's first export, so go-runtime
	// panels never get data; and a job serves no RPC.
	for _, p := range jobs.Panels {
		if strings.HasPrefix(p.ID, "go-") {
			t.Errorf("job dashboard carries go-runtime panel %q", p.ID)
		}
		for _, s := range p.Series {
			if strings.HasPrefix(s.Field, "rpc.server.") || strings.HasPrefix(s.Field, "go.") {
				t.Errorf("job panel %q reads %s", p.ID, s.Field)
			}
		}
	}
	if usesMetric(jobs, "k8s.container.cpu_limit_utilization") || usesMetric(jobs, "k8s.container.memory_limit_utilization") || strings.Contains(panelSQL(jobs), "cpu_limit_utilization") {
		t.Error("jobs declare no limit-utilization panel")
	}
	// The per-job table names its jobs by forge name, with run durations.
	summary, _ := findPanel(jobs, "jobs-summary")
	if summary.Kind != KindSQL {
		t.Fatalf("summary kind = %q", summary.Kind)
	}
	for _, want := range []string{"'cloudsql-backup-prune'", "'db-migrate-up'", "'dbrole-provision-reliant'", "last_started", "avg_duration_s", "failed", "{from:DateTime64(9)}", "k8s.object.kind"} {
		if !strings.Contains(summary.SQL, want) {
			t.Errorf("summary SQL lacks %q:\n%s", want, summary.SQL)
		}
	}
	// The per-job charts and the totals count EXECUTIONS (one Job in one
	// environment), not events: they are SQL panels over the grouped runs.
	for _, id := range []string{"runs-totals", "runs-by-job", "failures-by-job"} {
		p, _ := findPanel(jobs, id)
		if p.Kind != KindSQL || !strings.Contains(p.SQL, "GROUP BY env, run, uid, generation") {
			t.Errorf("%s must count distinct (env, Job) executions, got kind %q:\n%s", id, p.Kind, p.SQL)
		}
	}
	// The run list reads Job events only: a pod's own events would count twice.
	recent, _ := findPanel(jobs, "recent-runs")
	if recent.Kind != KindSQL || !strings.Contains(recent.SQL, "= 'Job'") {
		t.Errorf("recent-runs is not restricted to Job objects:\n%s", recent.SQL)
	}
}

// The Job controller emits SuccessfulCreate once per POD: a parallel Job, or a
// pod replaced after a failure, repeats it for one run. No panel may count
// those events as runs; every run count is over distinct executions.
func TestJobsDashboardNeverCountsCreateEventsAsRuns(t *testing.T) {
	jobs := dashboardByID(t, Build(fleet()), "jobs")
	for _, p := range jobs.Panels {
		counts := false
		for _, s := range p.Series {
			counts = counts || s.Agg == AggCount
		}
		for _, w := range p.Where {
			if counts && w.Field == fieldEventWhy && strings.Contains(strings.Join(append(w.Values, w.Value), " "), jobEventCreated) {
				t.Errorf("panel %q counts %s events, which are per pod, not per run", p.ID, jobEventCreated)
			}
		}
	}
	started, ok := findPanel(jobs, "runs-totals")
	if !ok || !strings.Contains(started.SQL, "GROUP BY env, run, uid, generation") || strings.Contains(started.SQL, "count() AS runs_started") {
		t.Errorf("runs started must count executions:\n%s", started.SQL)
	}
}

func TestEveryGeneratedDashboardValidates(t *testing.T) {
	for _, in := range []Input{fleet(), {Project: "shop"}, {Project: "shop", Frontends: []string{"web"}}} {
		for _, d := range Build(in).Dashboards {
			b, _ := json.Marshal(d)
			if _, err := ParseDashboard(d.ID, b); err != nil {
				t.Errorf("%s: %v", d.ID, err)
			}
		}
	}
	many := fleet()
	for i := 0; i < 23; i++ {
		n := "batch-job-with-a-long-name-" + string(rune('a'+i))
		many.Processes = append(many.Processes, Process{Name: n, Kind: "job", Binary: "app", ServiceNames: []string{n}, Workloads: []string{n}})
	}
	d := dashboardByID(t, Build(many), "jobs")
	b, _ := json.Marshal(d)
	if _, err := ParseDashboard("jobs", b); err != nil {
		t.Fatalf("a Jobs dashboard with many jobs must still validate: %v", err)
	}
	for _, id := range []string{"runs-by-job", "failures-by-job", "restarts", "warning-events", "recent-runs", "error-logs"} {
		if !hasPanel(d, id) {
			t.Errorf("26 jobs: Jobs dashboard lacks %q: %v", id, panelIDs(d))
		}
	}
}

func TestPanelsFollowWhatTheProcessDoes(t *testing.T) {
	sp := Build(fleet())
	api := dashboardByID(t, sp, "process-admin-api")
	workers := dashboardByID(t, sp, "process-workers")
	operator := dashboardByID(t, sp, "process-workspace")

	// RPC panels only for a process that serves RPCs.
	if !usesMetric(api, "rpc.server.calls") || !hasPanel(api, "red-by-rpc") {
		t.Errorf("a service that serves RPCs has RPC panels: %v", panelIDs(api))
	}
	for _, d := range []Dashboard{workers, operator} {
		if usesMetric(d, "rpc.server.calls") || usesMetric(d, "rpc.server.duration") || hasPanel(d, "red-by-rpc") {
			t.Errorf("%s serves no RPC but has RPC panels: %v", d.ID, panelIDs(d))
		}
		for _, f := range d.Filters {
			if strings.HasPrefix(f.ID, "rpc-") {
				t.Errorf("%s has the RPC filter %s", d.ID, f.ID)
			}
		}
	}
	// Worker-run panels only for workers and operators.
	for _, d := range []Dashboard{workers, operator} {
		if !hasPanel(d, "worker-runs") || !hasPanel(d, "worker-failures") {
			t.Errorf("%s runs workers but has no worker panels: %v", d.ID, panelIDs(d))
		}
	}
	if hasPanel(api, "worker-runs") {
		t.Error("a service has no worker-run panel")
	}
	// Limit panels only for a declared limit; the request is the fallback.
	if !usesMetric(api, "k8s.container.cpu_limit_utilization") || !usesMetric(api, "k8s.container.memory_limit_utilization") {
		t.Error("api declares both limits and draws both")
	}
	if !usesMetric(workers, "k8s.container.cpu_limit_utilization") || usesMetric(workers, "k8s.container.memory_limit_utilization") || !usesMetric(workers, "k8s.container.memory_request_utilization") {
		t.Errorf("workers declare a CPU limit only: %v", panelIDs(workers))
	}
	if usesMetric(operator, "k8s.container.cpu_limit_utilization") || usesMetric(operator, "k8s.container.memory_limit_utilization") {
		t.Error("a request-only process has no limit panel")
	}
	if !usesMetric(operator, "k8s.container.cpu_request_utilization") || !hasPanel(operator, "restarts") {
		t.Errorf("a request-only process is drawn against its request and still shows restarts: %v", panelIDs(operator))
	}
	// Every dashboard keeps runtime, logs and a database section only with a DB.
	if !hasPanel(api, "db-latency") || hasPanel(workers, "db-latency") {
		t.Error("database panels only for a process that reaches the database")
	}
	for _, d := range []Dashboard{api, workers, operator} {
		if !hasPanel(d, "go-memory") || !hasPanel(d, "logs") {
			t.Errorf("%s lacks runtime/log panels", d.ID)
		}
	}
}

func TestNoNodeAgentMeansNoSaturationPanels(t *testing.T) {
	in := fleet()
	for i := range in.Processes {
		in.Processes[i].NodeMetadata = false
	}
	for _, d := range Build(in).Dashboards {
		if !strings.HasPrefix(d.ID, "process-") {
			continue
		}
		for _, p := range d.Panels {
			if p.Section == "workload" {
				t.Errorf("%s has workload panel %q although the process runs where no node agent reports", d.ID, p.ID)
			}
		}
	}
}

func TestOverviewScopesEachPanelToTheProcessesItCanHaveDataFor(t *testing.T) {
	sp := Build(fleet())
	ov := dashboardByID(t, sp, "overview")
	if ov.Group != GroupOverview {
		t.Errorf("overview group = %q", ov.Group)
	}
	scope := func(id string) []string {
		p, ok := findPanel(ov, id)
		if !ok {
			t.Fatalf("overview lacks %q: %v", id, panelIDs(ov))
		}
		return p.Where[0].Values
	}
	if got := scope("requests"); !reflect.DeepEqual(got, []string{"admin-api"}) {
		t.Errorf("RPC panels are scoped to the processes that serve RPCs, got %v", got)
	}
	if got := scope("cpu-vs-limit"); !reflect.DeepEqual(got, []string{"admin-api", "workers"}) {
		t.Errorf("cpu-vs-limit scope = %v, want the workloads declaring a CPU limit", got)
	}
	if got := scope("memory-vs-limit"); !reflect.DeepEqual(got, []string{"admin-api"}) {
		t.Errorf("memory-vs-limit scope = %v", got)
	}
	if got := scope("restarts"); !reflect.DeepEqual(got, []string{"admin-api", "workers", "workspace"}) {
		t.Errorf("restarts scope = %v, want the long-running processes and no job", got)
	}
	body, _ := json.Marshal(ov)
	if strings.Contains(string(body), "db-migrate-up") {
		t.Error("the overview is the long-running processes; jobs are on the Jobs dashboard")
	}
}

func TestOverviewWithoutRPCsOrLimitsDropsThoseGroupsOfPanels(t *testing.T) {
	in := Input{Project: "shop", Processes: []Process{{Name: "worker", Kind: "worker", Binary: "app", ServiceNames: []string{"worker"}, Workloads: []string{"worker"}, NodeMetadata: true}}}
	ov := dashboardByID(t, Build(in), "overview")
	for _, id := range []string{"requests", "errors", "p95", "request-rate", "error-rate", "latency", "top-errors", "cpu-vs-limit", "memory-vs-limit"} {
		if hasPanel(ov, id) {
			t.Errorf("overview has %q although nothing serves RPCs or declares a limit", id)
		}
	}
	if !hasPanel(ov, "restarts") || !hasPanel(ov, "error-logs") {
		t.Errorf("restarts and error logs still apply: %v", panelIDs(ov))
	}
}

func TestEveryGeneratedDashboardHasAGroup(t *testing.T) {
	want := map[string]Group{
		"overview": GroupOverview, "process-admin-api": GroupServices, "process-workers": GroupServices, "process-workspace": GroupServices,
		"jobs": GroupJobs, "workloads": GroupPlatform, "releases": GroupReleases, "frontend": GroupProduct,
	}
	got := map[string]Group{}
	for _, d := range Build(fleet()).Dashboards {
		got[d.ID] = d.Group
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("groups = %v, want %v", got, want)
	}
}

func TestProcessDashboardIDIsTheForgeWorkloadName(t *testing.T) {
	in := Input{Project: "shop", Processes: []Process{{
		Name: "identity-canary", Kind: "worker", Binary: "control-plane", ServiceNames: []string{"identity-canary"}, Workloads: []string{"identity-canary"},
		Replaces: []string{"control-plane-identity-canary---admin-api-url-http-admin-api.control-plane-prod.svc.cluster.local-80-88a5f8616223", "identity-canary"},
	}}}
	d := dashboardByID(t, Build(in), "process-identity-canary")
	if d.Title != "shop: identity-canary" {
		t.Errorf("title = %q", d.Title)
	}
	want := []string{"process-control-plane-identity-canary---admin-api-url-http-admin-api.control-plane-prod.svc.cluster.local-80-88a5f8616223"}
	if !reflect.DeepEqual(d.Replaces, want) {
		t.Errorf("replaces = %v, want the old generated id (and not its own)", d.Replaces)
	}
	if strings.Contains(d.Description, "--") {
		t.Errorf("description carries rendered arguments: %q", d.Description)
	}
	b, _ := json.Marshal(d)
	if _, err := ParseDashboard(d.ID, b); err != nil {
		t.Fatalf("a dashboard replacing a long old id must validate: %v", err)
	}
}

func TestWithoutProcessDashboardsKeepsOnlyTheFrontend(t *testing.T) {
	sp := WithoutProcessDashboards(Build(Input{Project: "shop", Frontends: []string{"web"}}))
	if got := dashboardIDs(sp); !reflect.DeepEqual(got, []string{"frontend"}) {
		t.Fatalf("dashboards = %v", got)
	}
}

func TestFrontendDashboardFollowsTheServicesTheEnvServes(t *testing.T) {
	in := Input{Project: "control-plane", Frontends: []string{"reliant-web"}, FrontendServices: []string{"control-plane-reliant-web", "reliant-web"}}
	fe := dashboardByID(t, Build(in), "frontend")
	p, _ := findPanel(fe, "js-errors")
	var scope []string
	for _, w := range p.Where {
		if w.Field == FieldService {
			scope = w.Values
		}
	}
	if !reflect.DeepEqual(scope, []string{"control-plane-reliant-web", "reliant-web"}) {
		t.Errorf("frontend scope = %v", scope)
	}
	if strings.Contains(mustMarshal(t, fe), "internal-console") {
		t.Error("the frontend dashboard names a frontend the env does not serve")
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestJobsDashboardIsNotWrittenIntoTheProjectTree(t *testing.T) {
	dir := t.TempDir()
	written, err := Write(dir, fleet(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range written {
		if strings.Contains(w, "-jobs_gen") || strings.Contains(w, "db-migrate-up") || strings.Contains(w, "process-db-migrate") {
			t.Errorf("Write emitted %s: jobs read cluster events a local stack lacks, and a job has no dashboard of its own", w)
		}
	}
}

// The Jobs dashboard names every per-job dashboard it replaces, under the id
// each had before and the id it would have had under the workload name, so a
// reader can move stars and copies of any of them here.
func TestJobsDashboardReplacesTheOldPerJobDashboards(t *testing.T) {
	in := Input{Project: "shop", Processes: []Process{
		{Name: "db-migrate-up", Kind: "job", Binary: "app", ServiceNames: []string{"db-migrate-up"}, Workloads: []string{"db-migrate-up"}, Replaces: []string{"app-db-migrate-up"}},
		{Name: "cloudsql-backup-prune", Kind: "cron", Binary: "app", ServiceNames: []string{"x"}, Workloads: []string{"cloudsql-backup-prune"}, Replaces: []string{"app-cloudsql-prune-backups---project-p-bcbc04b8f7e4"}},
	}}
	d := dashboardByID(t, Build(in), "jobs")
	want := []string{
		"process-app-cloudsql-prune-backups---project-p-bcbc04b8f7e4", "process-app-db-migrate-up",
		"process-cloudsql-backup-prune", "process-db-migrate-up",
	}
	if !reflect.DeepEqual(d.Replaces, want) {
		t.Fatalf("replaces = %v, want %v", d.Replaces, want)
	}
	b, _ := json.Marshal(d)
	if _, err := ParseDashboard("jobs", b); err != nil {
		t.Fatalf("a Jobs dashboard with replaces must validate: %v", err)
	}
	for _, id := range d.Replaces {
		if id == "jobs" {
			t.Error("a dashboard never replaces itself")
		}
	}
}

// A run is one Kubernetes Job in one ENVIRONMENT: identical specs deployed to
// two environments share a hashed Job name, and must not collapse into one run.
func TestJobsSummaryGroupsRunsByEnvironmentAndJob(t *testing.T) {
	sql := jobsSummarySQL([]Process{{Name: "db-migrate-up", Kind: "job", ServiceNames: []string{"db-migrate-up"}, Workloads: []string{"db-migrate-up"}}})
	if !strings.Contains(sql, "GROUP BY env, run, uid, generation") || !strings.Contains(sql, "ResourceAttributes['reliant.env.id'] AS env") {
		t.Errorf("the per-run grouping must carry the environment, the Job UID and the generation:\n%s", sql)
	}
	if !strings.Contains(sql, "ResourceAttributes['k8s.object.uid'] AS uid") {
		t.Errorf("the Job UID must come from the involved object's UID:\n%s", sql)
	}
	if !strings.Contains(sql, "has({env_ids:Array(String)}, ResourceAttributes['reliant.env.id'])") {
		t.Errorf("the summary must honour the dashboard's environments:\n%s", sql)
	}
}

// A standalone Job's name is its pod-spec hash alone, so recreating an
// unchanged Job reuses it. Every run count is built on the same executions
// subquery, so none of them can drift from the summary.
func TestEveryRunCountSharesTheExecutionGrouping(t *testing.T) {
	jobs := dashboardByID(t, Build(fleet()), "jobs")
	for _, id := range []string{"jobs-summary", "runs-totals", "runs-by-job", "failures-by-job", "recent-runs"} {
		p, ok := findPanel(jobs, id)
		if !ok {
			t.Fatalf("no panel %s: %v", id, panelIDs(jobs))
		}
		if !strings.Contains(p.SQL, "GROUP BY env, run, uid, generation") {
			t.Errorf("%s does not count distinct executions (env, name, UID, generation):\n%s", id, p.SQL)
		}
	}
}

// panelSQL is every SQL statement on d, for a search across the whole dashboard.
func panelSQL(d Dashboard) string {
	var b strings.Builder
	for _, p := range d.Panels {
		b.WriteString(p.SQL)
		b.WriteByte('\n')
	}
	return b.String()
}

// The Jobs dashboard has ONE way to select a job. The console hands the
// dashboard's service selector to every DECLARATIVE panel as ServiceName IN
// (...) and a panel cannot opt out of it, while the telemetry a job leaves
// carries three names (metrics the workload, logs the service.name, events the
// Job object's). So a declarative panel is selected by a different subset than
// its neighbours, or by nothing. Every panel here is SQL, and the selection
// reaches a statement only through the resolver.
func TestEveryJobsPanelSelectsThroughTheOneResolver(t *testing.T) {
	jobs := dashboardByID(t, Build(fleet()), "jobs")
	resolver := jobResolverSQL(fleetJobs())
	if len(jobs.Panels) != 8 {
		t.Errorf("Jobs dashboard has %d panels, want its 8: %v", len(jobs.Panels), panelIDs(jobs))
	}
	for _, p := range jobs.Panels {
		if p.Kind != KindSQL {
			t.Errorf("panel %s is %q: a declarative panel receives the raw service selector, which cannot select a job", p.ID, p.Kind)
			continue
		}
		// The statement embeds the resolver verbatim, and has its jobs selected by it.
		if !strings.Contains(p.SQL, resolver) {
			t.Errorf("panel %s does not carry the shared resolver:\n%s", p.ID, p.SQL)
		}
		if !strings.Contains(p.SQL, "has(selected_jobs, job)") {
			t.Errorf("panel %s does not keep only the selected jobs:\n%s", p.ID, p.SQL)
		}
		// The raw selection is bound exactly once per resolver and never anywhere else.
		stripped := strings.ReplaceAll(p.SQL, resolver, "")
		if strings.Contains(stripped, "{services:") {
			t.Errorf("panel %s binds the raw service selector outside the resolver:\n%s", p.ID, stripped)
		}
		if strings.Contains(stripped, "{envs:") {
			t.Errorf("panel %s binds the raw environment names: the console binds {env_ids}", p.ID)
		}
		if p.Signal != "" || len(p.Where) > 0 || len(p.Series) > 0 {
			t.Errorf("panel %s carries declarative selection (signal %q, where %v, series %v) beside its SQL", p.ID, p.Signal, p.Where, p.Series)
		}
	}
	// One filter, the service selector, and it targets every panel (no panelIds
	// subset that would leave one with a stale value).
	if len(jobs.Filters) != 1 || jobs.Filters[0].Field != FieldService || len(jobs.Filters[0].PanelIDs) != 0 {
		t.Errorf("Jobs filters = %+v, want the one dashboard-wide service filter", jobs.Filters)
	}
	// And the resolver is defined once per statement, from the same process list.
	for _, p := range jobs.Panels {
		if n := strings.Count(p.SQL, "AS job_identity"); n < 1 {
			t.Errorf("panel %s never defines job_identity", p.ID)
		}
	}
}

// fleetJobs are the jobs and crons of fleet(), in the order Build hands them to
// BuildJobs (sorted by name).
func fleetJobs() []Process {
	var out []Process
	for _, p := range fleet().Processes {
		if p.IsJob() {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// The resolver's identity table names every identity a job leaves: its forge
// name, its workloads and their service.names, quoted.
func TestJobResolverCarriesEveryIdentityOfEachJob(t *testing.T) {
	jobs := []Process{
		{Name: "indexer", Kind: "job", Workloads: []string{"indexer", "indexer-dev"}, ServiceNames: []string{"search-indexer", "indexer-dev"}},
		{Name: "o'brien", Kind: "cron", Workloads: []string{"o'brien"}, ServiceNames: []string{"o'brien"}},
	}
	got := jobResolverSQL(jobs)
	for _, want := range []string{
		`('indexer', ['indexer', 'indexer-dev'], ['search-indexer', 'indexer-dev'])`,
		`('o\'brien', ['o\'brien'], ['o\'brien'])`,
		"{services:Array(String)}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("resolver lacks %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "{services:Array(String)}"); n != 2 {
		t.Errorf("the resolver binds the selector %d times, want twice (the empty test and the match), all inside it", n)
	}
}

// Each source is filtered by the identity it carries: metrics and events the
// workload or object name, logs the service.name.
func TestEachJobsSourceIsFilteredByTheIdentityItCarries(t *testing.T) {
	jobs := fleetJobs()
	for _, tc := range []struct {
		name, sql string
		want      []string
		not       []string
	}{
		{"restarts (metrics)", jobsRestartsSQL(jobs), []string{"FROM otel_metrics_gauge", "k8s.container.restarts", "match(ResourceAttributes['k8s.workload.name']"}, []string{"has(j.3, ServiceName)"}},
		{"warning events", jobsWarningsSQL(jobs), []string{"FROM otel_logs", "k8s.event.type'] = 'Warning'", "match(ResourceAttributes['k8s.workload.name']"}, []string{"has(j.3, ServiceName)"}},
		{"run events", jobsSummarySQL(jobs), []string{"k8s.object.kind", "match(ResourceAttributes['k8s.workload.name']"}, []string{"has(j.3, ServiceName)"}},
		{"error logs", jobsErrorLogsSQL(jobs), []string{"FROM otel_logs", "has(j.3, ServiceName)", "IN ('ERROR', 'ERR')"}, []string{"k8s.workload.name"}},
	} {
		for _, w := range tc.want {
			if !strings.Contains(tc.sql, w) {
				t.Errorf("%s lacks %q:\n%s", tc.name, w, tc.sql)
			}
		}
		for _, n := range tc.not {
			if strings.Contains(tc.sql, n) {
				t.Errorf("%s filters by an identity its source does not carry (%q):\n%s", tc.name, n, tc.sql)
			}
		}
	}
}

// Across every generated dashboard: a panel that pins its own identity
// (service.name IN, or k8s.workload.name IN) must not ALSO receive the
// dashboard's service selector for the other identity. The node agent stamps
// node-derived series (saturation, restarts) with the WORKLOAD name, while
// the application's spans, metrics and logs carry its service.name
// (OTEL_SERVICE_NAME). When a project renames a service, those differ, and a
// dashboard-wide service filter blanks one half of the dashboard whichever
// name is picked. Such a dashboard must split the filter by identity: the
// service filter on the application panels, a workload filter on the node
// panels (Filter.PanelIDs).
func TestNoDashboardGivesAPanelAServiceSelectionItsDataCannotMatch(t *testing.T) {
	in := fleet()
	// Rename two services away from their workload names, as OTEL_SERVICE_NAME does.
	for i := range in.Processes {
		switch in.Processes[i].Name {
		case "admin-api":
			in.Processes[i].ServiceNames = []string{"admin-service"}
		case "workers":
			in.Processes[i].ServiceNames = []string{"worker-service"}
		}
	}
	in.Deployed = Identities{ServiceNames: []string{"admin-service", "worker-service", "workspace"}, Workloads: []string{"admin-api", "workers", "workspace"}}
	sp := Build(in)
	checked := 0
	for _, d := range sp.Dashboards {
		var serviceFilter *Filter
		for i := range d.Filters {
			if d.Filters[i].Field == FieldService {
				serviceFilter = &d.Filters[i]
			}
		}
		if serviceFilter == nil || d.ID == JobsID {
			continue
		}
		// A process whose service.names are its workload names has no split.
		if proc, ok := strings.CutPrefix(d.ID, "process-"); ok {
			unchanged := false
			for _, p := range in.Processes {
				if p.Name == proc && reflect.DeepEqual(p.ServiceNames, p.Workloads) {
					unchanged = true
				}
			}
			if unchanged {
				continue
			}
		}
		for _, p := range d.Panels {
			if p.Kind == KindSQL {
				continue
			}
			byWorkload := false
			for _, w := range p.Where {
				byWorkload = byWorkload || w.Field == fieldWorkload
			}
			if !byWorkload {
				continue
			}
			checked++
			// The panel is selected by workload name, so the service selector
			// must not reach it.
			if len(serviceFilter.PanelIDs) == 0 || contains(serviceFilter.PanelIDs, p.ID) {
				t.Errorf("%s: the service.name filter reaches panel %s, whose data is keyed by workload (a renamed service blanks it)", d.ID, p.ID)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no workload-keyed panel was checked: the test proves nothing")
	}
}

// When no service is renamed (every service.name is its workload name) there
// is nothing to split and the filter stays dashboard-wide.
func TestServiceFilterStaysDashboardWideWhenNamesAgree(t *testing.T) {
	for _, d := range Build(fleet()).Dashboards {
		for _, f := range d.Filters {
			if f.Field == FieldService && len(f.PanelIDs) != 0 {
				t.Errorf("%s: the service filter is split to %v although every service.name is its workload name", d.ID, f.PanelIDs)
			}
		}
	}
}
