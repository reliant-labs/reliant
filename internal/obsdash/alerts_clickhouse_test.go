package obsdash

import (
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestDefaultAlertSQLOnClickHouse runs every generated default against a real
// ClickHouse holding the clickhouseexporter's tables, with seeded series for
// each case the SQL must get right: cumulative counters with a restart, delta
// counters, an unsampled error ratio, low traffic, a stopped service and a
// young one. The goldens pin the text; this pins what it MEANS.
//
// Set FORGE_ALERT_CLICKHOUSE to the name of a running container with
// clickhouse-client and the exporter's tables (password "pw"), e.g. the one
// internal/templates' ClickStack gate starts.
func TestDefaultAlertSQLOnClickHouse(t *testing.T) {
	container := os.Getenv("FORGE_ALERT_CLICKHOUSE")
	if testing.Short() || container == "" {
		t.Skip("needs a ClickHouse container; set FORGE_ALERT_CLICKHOUSE=<container>")
	}
	run := func(sql string) string {
		t.Helper()
		cmd := exec.Command("docker", "exec", "-i", container, "clickhouse-client", "--password", "pw",
			"--joined_subquery_requires_alias=0", "--multiquery", "--format", "TSV")
		cmd.Stdin = strings.NewReader(sql)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("clickhouse: %v\n%s\n--- sql ---\n%s", err, out, sql)
		}
		return strings.TrimSpace(string(out))
	}
	seed, err := os.ReadFile("testdata/clickhouse-seed.sql")
	if err != nil {
		t.Fatal(err)
	}
	run(string(seed))

	in := AlertInput{
		Project: "shop", FrontendServices: []string{"shop-web"},
		Services: []AlertService{
			{"api", "a.v1.S"}, {"quiet", "q.v1.Q"}, {"reset", "r.v1.R"}, {"delta", "d.v1.D"},
			{"fast", "f.v1.F"}, {"stopped", "s.v1.T"}, {"healthy", "h.v1.H"}, {"sparse", "p.v1.P"}, {"young", "y.v1.Y"},
		},
	}
	rules, err := ResolveAlertDefaults(in, "dev", nil)
	if err != nil {
		t.Fatal(err)
	}
	// FrontendServices parameterize the SQL (ServiceName IN ...), so
	// each frontend-* case below re-resolves with the seeded service it is about.
	frontendFired := func(service, id string) bool {
		t.Helper()
		fin := in
		fin.FrontendServices = []string{service}
		rs, err := ResolveAlertDefaults(fin, "dev", nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rs {
			if r.ID == id {
				return run(r.Expr) != ""
			}
		}
		t.Fatalf("no default %s", id)
		return false
	}
	fired := map[string][]string{}
	for _, r := range rules {
		if !strings.Contains(r.Expr, "otel_traces") && !strings.Contains(r.Expr, "otel_metrics_sum") && !strings.Contains(r.Expr, "otel_metrics_histogram") {
			continue // metadata and platform rules have their own tests
		}
		out := run(r.Expr)
		if out != "" {
			for _, line := range strings.Split(out, "\n") {
				fired[r.ID] = append(fired[r.ID], strings.Split(line, "\t")[0])
			}
		}
	}
	want := map[string][]string{
		// api fails 20% of 200 calls, delta 10% of 60. quiet (3 calls in the window), reset (no errors) stay quiet.
		"error-rate": {"api", "delta"},
		// api's p95 sits in the (1s, 5s] bucket; fast's in the first.
		"p95-latency": {"api"},
		// stopped served traffic through most of the last day, then stopped. sparse (4 active hours) and young (2h) do not count as steady; healthy is serving.
		"service-silent":     {"stopped"},
		"frontend-js-errors": {"shop-web"},
	}
	for id, w := range want {
		if strings.Join(fired[id], ",") != strings.Join(w, ",") {
			t.Errorf("%s fired for %v, want %v", id, fired[id], w)
		}
	}

	// FrontendJSErrorSpike: a string-only console.error is not a JS error, but
	// console.error(err) (which carries an exception event) is.
	for service, want := range map[string]bool{"shop-strings": false, "shop-errobj": true} {
		if got := frontendFired(service, "frontend-js-errors"); got != want {
			t.Errorf("frontend-js-errors for %s = %v, want %v", service, got, want)
		}
	}
	// FrontendLCPPoor: the p75 of LCP measurements, with a minimum sample, and
	// other web-vitals spans (INP) do not enter the percentile.
	for service, want := range map[string]bool{"shop-slow": true, "shop-fast": false, "shop-few": false, "shop-mixed": false} {
		if got := frontendFired(service, "frontend-lcp-poor"); got != want {
			t.Errorf("frontend-lcp-poor for %s = %v, want %v", service, got, want)
		}
	}

	// A different env sees none of dev's data.
	other, _ := ResolveAlertDefaults(in, "prod", nil)
	for _, r := range other {
		if r.ID == "service-silent" || r.ID == "frontend-js-errors" || strings.Contains(r.Expr, "{reliant_env_id") {
			continue
		}
		if out := run(r.Expr); strings.Contains(out, "api") && r.ID != "error-rate" {
			t.Errorf("%s in env prod saw dev's series: %s", r.ID, out)
		}
	}
}

// TestMetadataAlertSQLOnClickHouse runs the customer-metadata defaults (the node
// agent's gauges and Kubernetes events, in the exporter's real tables) against a
// real ClickHouse with seeded series: a growing restart gauge, a restart count
// that is high but not moving, a replaced pod, an OOM reason delivered as a data
// point attribute and as a resource attribute, a spike versus a sustained memory
// ratio, events inside and outside the window, other envs, and a workload-less
// event. Same container and conventions as TestDefaultAlertSQLOnClickHouse.
func TestMetadataAlertSQLOnClickHouse(t *testing.T) {
	container := os.Getenv("FORGE_ALERT_CLICKHOUSE")
	if testing.Short() || container == "" {
		t.Skip("needs a ClickHouse container; set FORGE_ALERT_CLICKHOUSE=<container>")
	}
	run := func(sql string) string {
		t.Helper()
		cmd := exec.Command("docker", "exec", "-i", container, "clickhouse-client", "--password", "pw",
			"--joined_subquery_requires_alias=0", "--multiquery", "--format", "TSV")
		cmd.Stdin = strings.NewReader(sql)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("clickhouse: %v\n%s\n--- sql ---\n%s", err, out, sql)
		}
		return strings.TrimSpace(string(out))
	}
	seed, err := os.ReadFile("testdata/clickhouse-seed-metadata.sql")
	if err != nil {
		t.Fatal(err)
	}
	run(string(seed))

	in := AlertInput{Project: "shop", Services: []AlertService{{"api", "a.v1.S"}}}
	rules, err := ResolveAlertDefaults(in, "dev", nil)
	if err != nil {
		t.Fatal(err)
	}
	fired := func(r AlertRule, envID string) []string {
		out := run(BindEnvID(r.Expr, envID))
		if out == "" {
			return nil
		}
		var got []string
		for _, line := range strings.Split(out, "\n") {
			got = append(got, strings.Join(strings.Split(line, "\t")[:len(strings.Split(line, "\t"))-1], "/"))
		}
		return got
	}
	want := map[string][]string{
		// crashy's gauge grew 2 -> 5; backoff has BackOff "restarting" events. twice (2), old (40, not moving),
		// rolled (a replaced pod), pulling (a BackOff on an image pull) must not fire.
		"crash-loop": {"backoff", "crashy"},
		// oomer (reason on the data point), oomres (reason on the resource) and oomevent (an OOMKilling event)
		// fire; old has OOMKilled as its reason but its count did not move in the window.
		"oom-killed": {"oomer", "oomevent", "oomres"},
		// hot is at 95% throughout; spike averages 55% with one 99% sample; the working-set gauge is not a ratio.
		"memory-near-limit": {"hot/app"},
		// imgfail has two pull failures; pulling is a BackOff on "pulling image"; quietpull only pulled.
		"image-pull-failed": {"imgfail", "pulling"},
		// evictee was evicted 6 minutes ago; oldevict 90 minutes ago; the Node event has no workload.
		"evicted": {"evictee"},
	}
	for _, r := range rules {
		w, ok := want[r.ID]
		if !ok {
			continue
		}
		if got := fired(r, "env-1"); strings.Join(got, ",") != strings.Join(w, ",") {
			t.Errorf("%s fired for %v in env-1, want %v", r.ID, got, w)
		}
		// Another env's id sees only its own data; an unknown id sees nothing, and neither errors.
		if got := fired(r, "env-nobody"); len(got) != 0 {
			t.Errorf("%s fired for %v in an env with no data", r.ID, got)
		}
	}
	// env-2 has its own crash loop, a hot container, an eviction and a pull error.
	wantEnv2 := map[string][]string{
		"crash-loop": {"other"}, "oom-killed": {"other"}, "memory-near-limit": {"hot2/app"},
		"image-pull-failed": {"elsewhere"}, "evicted": {"elsewhere"},
	}
	for _, r := range rules {
		if w, ok := wantEnv2[r.ID]; ok {
			if got := fired(r, "env-2"); strings.Join(got, ",") != strings.Join(w, ",") {
				t.Errorf("%s fired for %v in env-2, want %v", r.ID, got, w)
			}
		}
	}

	// An env that opted out of metadata has no rows at all: even a rule bound to it returns nothing (and never errors).
	run("TRUNCATE TABLE otel_metrics_gauge; TRUNCATE TABLE otel_logs;")
	for _, r := range rules {
		if _, ok := want[r.ID]; ok {
			if got := fired(r, "env-1"); len(got) != 0 {
				t.Errorf("%s fired on an empty metadata table: %v", r.ID, got)
			}
		}
	}
}

// TestJobsSummarySQLOnClickHouse runs the Jobs dashboard's per-job table against
// a real ClickHouse holding the exporter's otel_logs: the same hashed Job name
// in two environments (one succeeds, one fails) is TWO runs, a Pod's event
// about a Job-named object and another project's job are not counted, and a job
// with no completed run has NULL durations. Same gate as the alert defaults.
func TestJobsSummarySQLOnClickHouse(t *testing.T) {
	container := os.Getenv("FORGE_ALERT_CLICKHOUSE")
	if testing.Short() || container == "" {
		t.Skip("needs a ClickHouse container; set FORGE_ALERT_CLICKHOUSE=<container>")
	}
	run := func(sql string) string {
		t.Helper()
		cmd := exec.Command("docker", "exec", "-i", container, "clickhouse-client", "--password", "pw", "--multiquery", "--format", "TSV")
		cmd.Stdin = strings.NewReader(sql)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("clickhouse: %v\n%s\n--- sql ---\n%s", err, out, sql)
		}
		return strings.TrimSpace(string(out))
	}
	seed, err := os.ReadFile("testdata/clickhouse-jobs-seed.sql")
	if err != nil {
		t.Fatal(err)
	}
	// Every INSERT would evaluate now() at its own instant, and on a loaded
	// host those drift apart by seconds, turning a 90s run into 91s. One anchor
	// keeps the seeded offsets exact.
	anchor := fmt.Sprintf("toDateTime64(%d, 9, 'UTC')", time.Now().Unix())
	run(strings.ReplaceAll(string(seed), "now()", anchor))
	// "indexer" reports under a service.name of its own (OTEL_SERVICE_NAME), and
	// "reindex" under its workload name: the two identities a selection must
	// both resolve.
	var jobs []Process
	for _, n := range []string{"cloudsql-backup-prune", "db-migrate-up", "dbrole-provision-reliant", "indexer", "nightly", "reindex", "rebuild"} {
		p := Process{Name: n, Kind: "job", ServiceNames: []string{n}, Workloads: []string{n}}
		if n == "indexer" {
			p.ServiceNames = []string{"search-indexer"}
		}
		jobs = append(jobs, p)
	}
	bindWith := func(q, envs, services string) string {
		q = strings.ReplaceAll(q, "{from:DateTime64(9)}", "now() - INTERVAL 1 DAY")
		q = strings.ReplaceAll(q, "{to:DateTime64(9)}", "now() + INTERVAL 1 DAY")
		q = strings.ReplaceAll(q, "{step:UInt32}", "3600")
		q = strings.ReplaceAll(q, "{services:Array(String)}", services)
		return strings.ReplaceAll(q, "{env_ids:Array(String)}", envs)
	}
	bindSQL := func(q, envs string) string { return bindWith(q, envs, "[]") }
	bind := func(envs string) string { return bindSQL(jobsSummarySQL(jobs), envs) }
	row := func(out, job string) []string {
		for _, l := range strings.Split(out, "\n") {
			if f := strings.Split(l, "\t"); f[0] == job {
				return f
			}
		}
		return nil
	}
	all := run(bind("[]"))
	if r := row(all, "db-migrate-up"); r == nil || r[1] != "2" || r[2] != "1" || r[3] != "1" {
		t.Errorf("db-migrate-up over both envs = %v, want 2 runs, 1 succeeded, 1 failed:\n%s", r, all)
	}
	if r := row(all, "cloudsql-backup-prune"); r == nil || r[1] != "1" || r[2] != "1" || r[5] != "90" {
		t.Errorf("cloudsql-backup-prune = %v, want 1 run, 1 succeeded, 90s (the Pod event must not count):\n%s", r, all)
	}
	if strings.Contains(all, "other-thing") {
		t.Errorf("another project's job is listed:\n%s", all)
	}
	if r := row(run(bind("['stg']")), "db-migrate-up"); r == nil || r[1] != "1" || r[3] != "1" || r[5] != `\N` || r[6] != `\N` {
		t.Errorf("a failed-only environment = %v, want 1 failed run and NULL durations", r)
	}
	if r := row(run(bind("['prod']")), "db-migrate-up"); r == nil || r[1] != "1" || r[2] != "1" || r[3] != "0" {
		t.Errorf("prod alone = %v, want 1 succeeded run", r)
	}
	// The NEWEST run decides last_duration_s: an older success must not show
	// beside a newer failed run's start (argMax skips NULL arguments), nor beside
	// a run that is still going.
	if r := row(all, "dbrole-provision-reliant"); r == nil || r[1] != "2" || r[2] != "1" || r[3] != "1" || r[5] != `\N` || r[6] != "40" {
		t.Errorf("older success, newer failure = %v, want 2 runs, 1 ok, 1 failed, last_duration NULL, avg 40", r)
	}
	if r := row(all, "nightly"); r == nil || r[1] != "2" || r[2] != "1" || r[5] != `\N` {
		t.Errorf("older success, newest still running = %v, want last_duration NULL", r)
	}
	// One execution is one run however many pods the Job controller created
	// for it: "indexer" emitted four SuccessfulCreate events (three parallel
	// pods and a replacement) and is ONE run.
	if r := row(all, "indexer"); r == nil || r[1] != "1" || r[2] != "1" || r[3] != "0" || r[5] != "80" {
		t.Errorf("indexer = %v, want 1 run (4 SuccessfulCreate events), 1 succeeded, 80s:\n%s", r, all)
	}
	// A Job recreated under the same hashed name is two executions, told apart
	// by the Job UID when the telemetry carries it...
	if r := row(all, "reindex"); r == nil || r[1] != "2" || r[2] != "1" || r[3] != "1" || r[5] != "50" || r[6] != "50" || r[7] != "50" {
		t.Errorf("reindex (two UIDs, one name) = %v, want 2 runs, 1 succeeded, 1 failed, last/avg/max duration 50s:\n%s", r, all)
	}
	// ...and by the create that follows a terminal event when it does not (the
	// node agent drops k8s.object.uid today). The second execution used two pods.
	if r := row(all, "rebuild"); r == nil || r[1] != "2" || r[2] != "1" || r[3] != "1" || r[5] != "70" || r[6] != "70" {
		t.Errorf("rebuild (recreated, no UID) = %v, want 2 runs, 1 succeeded, 1 failed, last duration 70s:\n%s", r, all)
	}
	// The totals count the same executions: db-migrate-up 2 (prod, stg),
	// cloudsql-backup-prune 1, dbrole-provision-reliant 2, indexer 1, nightly 2,
	// reindex 2, rebuild 2 = 12 started; succeeded 1+1+1+1+1+1+1 = 7; failed
	// 1+1+1+1 = 4.
	if out := run(bindSQL(jobsTotalsSQL(jobs), "[]")); out != "12\t7\t4" {
		t.Errorf("totals = %q, want 12 started, 7 succeeded, 4 failed (one per execution, not per event)", out)
	}
	// Per job over time: each execution is one point, so indexer sums to 1.
	byJob := run(bindSQL(jobsByJobSQL(jobs, "started", "started > 0"), "[]"))
	idx := 0
	for _, l := range strings.Split(byJob, "\n") {
		if f := strings.Split(l, "\t"); len(f) == 3 && f[1] == "indexer" {
			n, _ := strconv.Atoi(f[2])
			idx += n
		}
	}
	if idx != 1 {
		t.Errorf("runs-by-job counts indexer %d times, want 1:\n%s", idx, byJob)
	}
	failedBy := run(bindSQL(jobsByJobSQL(jobs, "failed_at", "failed"), "[]"))
	if n := strings.Count(failedBy, "\n") + 1; strings.TrimSpace(failedBy) == "" || n != 4 {
		t.Errorf("failures-by-job rows = %d, want 4 (db-migrate-up in stg, dbrole-provision-reliant, reindex, rebuild):\n%s", n, failedBy)
	}

	// The service selector narrows EVERY panel of the Jobs dashboard alike, by
	// whichever name the operator picks. The panels are the dashboard's own
	// (BuildJobs), run with the selection bound the way the console binds it.
	dash := BuildJobs("shop", jobs)
	// What each panel returns for a job is read off the column that names it.
	jobColumn := map[string]int{
		"jobs-summary": 0, "runs-by-job": 1, "failures-by-job": 1, "recent-runs": 0,
		"warning-events": 1, "restarts": 1, "error-logs": 0,
	}
	for _, tc := range []struct {
		kind, pick string
		job        string
		// panels that have data for the job in the seed, and what each shows
		// (a fragment of its text, "" when only the row's existence matters)
		has map[string]string
	}{
		{"custom OTEL_SERVICE_NAME", "['search-indexer']", "indexer",
			map[string]string{"jobs-summary": "", "runs-by-job": "", "recent-runs": "", "warning-events": "", "restarts": "", "error-logs": "indexer failed to open shard"}},
		{"bare workload name", "['indexer']", "indexer",
			map[string]string{"jobs-summary": "", "runs-by-job": "", "recent-runs": "", "warning-events": "", "restarts": "", "error-logs": "indexer failed to open shard"}},
		{"hashed run name", "['indexer-eeeeeeeeee']", "indexer",
			map[string]string{"jobs-summary": "", "runs-by-job": "", "recent-runs": "", "warning-events": "", "restarts": "", "error-logs": "indexer failed to open shard"}},
		{"forge name (reindex)", "['reindex']", "reindex",
			map[string]string{"jobs-summary": "", "runs-by-job": "", "failures-by-job": "", "recent-runs": "", "warning-events": "", "restarts": "", "error-logs": "reindex hit the backoff limit"}},
		{"hashed run name (reindex)", "['reindex-ffffffffff']", "reindex",
			map[string]string{"jobs-summary": "", "runs-by-job": "", "failures-by-job": "", "recent-runs": "", "warning-events": "", "restarts": "", "error-logs": "reindex hit the backoff limit"}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			// Run every panel in one round trip, results separated by a marker.
			var script strings.Builder
			for _, p := range dash.Panels {
				script.WriteString(strings.TrimSpace(bindWith(p.SQL, "[]", tc.pick)) + ";\nSELECT '--end--';\n")
			}
			parts := strings.Split(run(script.String()), "--end--")
			if len(parts) != len(dash.Panels)+1 {
				t.Fatalf("got %d result sets for %d panels", len(parts)-1, len(dash.Panels))
			}
			for i, p := range dash.Panels {
				var rows []string
				for _, l := range strings.Split(strings.TrimSpace(parts[i]), "\n") {
					if l != "" {
						rows = append(rows, l)
					}
				}
				if p.ID == "runs-totals" {
					// No job column: a single row of counts. It must exist for a job that ran.
					if len(rows) != 1 || rows[0] == "0\t0\t0" {
						t.Errorf("runs-totals for %s = %v, want the job's runs", tc.pick, rows)
					}
					continue
				}
				col, ok := jobColumn[p.ID]
				if !ok {
					t.Fatalf("panel %s has no job column in this test: add it", p.ID)
				}
				want, expected := tc.has[p.ID]
				if !expected && len(rows) == 0 {
					continue
				}
				if expected && len(rows) == 0 {
					t.Errorf("%s: panel %s returned nothing for %s, want %s's data", tc.kind, p.ID, tc.pick, tc.job)
					continue
				}
				for _, r := range rows {
					f := strings.Split(r, "\t")
					if f[col] != tc.job {
						t.Errorf("%s: panel %s returned %s's row for %s: %q", tc.kind, p.ID, f[col], tc.pick, r)
					}
				}
				if want != "" && !strings.Contains(strings.Join(rows, "\n"), want) {
					t.Errorf("%s: panel %s lacks %q:\n%s", tc.kind, p.ID, want, strings.Join(rows, "\n"))
				}
			}
		})
	}
	// Selected by nothing that is a job, no panel returns anything.
	{
		var script strings.Builder
		for _, p := range dash.Panels {
			script.WriteString(strings.TrimSpace(bindWith(p.SQL, "[]", "['no-such-service']")) + ";\nSELECT '--end--';\n")
		}
		for i, part := range strings.Split(run(script.String()), "--end--")[:len(dash.Panels)] {
			if out := strings.TrimSpace(part); out != "" && dash.Panels[i].ID != "runs-totals" {
				t.Errorf("panel %s returned rows for a selection that is no job:\n%s", dash.Panels[i].ID, out)
			} else if dash.Panels[i].ID == "runs-totals" && out != "0\t0\t0" {
				t.Errorf("runs-totals for no job = %q", out)
			}
		}
	}
	// Unselected, every panel covers every job.
	if logs := run(bindSQL(jobsErrorLogsSQL(jobs), "[]")); strings.Contains(logs, "unrelated-service") || strings.Contains(logs, "the workload name is not a service.name here") || strings.Count(logs, "\n") != 1 {
		t.Errorf("unselected error logs = want the 2 error records of indexer and reindex only (another service's and a workload-named record are not a job's):\n%s", logs)
	}
	if rs := run(bindSQL(jobsRestartsSQL(jobs), "[]")); strings.Contains(rs, "other-thing") || !strings.Contains(rs, "indexer") || !strings.Contains(rs, "reindex") {
		t.Errorf("unselected restarts = %q, want indexer's and reindex's, not another project's", rs)
	}
	if n := strings.Count(strings.TrimSpace(run(bindSQL(jobsRecentRunsSQL(jobs), "[]"))), "\n") + 1; n != 12 {
		t.Errorf("recent runs unfiltered = %d rows, want 12 (every execution)", n)
	}
	if r := row(run(bindSQL(jobsRecentRunsSQL(jobs), "[]")), "reindex"); r == nil || r[2] != "prod" {
		t.Errorf("recent runs lack reindex: %v", r)
	}

	// Warning events: the Job's own (BackoffLimitExceeded) and its pods', both
	// resolved to the forge job; another project's Job and Normal events are not.
	warnings := map[string]int{}
	for _, l := range strings.Split(run(bindSQL(jobsWarningsSQL(jobs), "[]")), "\n") {
		if f := strings.Split(l, "\t"); len(f) == 3 {
			n, _ := strconv.Atoi(f[2])
			warnings[f[1]] += n
		}
	}
	if want := map[string]int{"reindex": 1, "indexer": 2}; !reflect.DeepEqual(warnings, want) {
		t.Errorf("warning events per job = %v, want %v (a Job's own and its pods', not another project's, not Normal ones)", warnings, want)
	}
	if got := run(bindWith(jobsWarningsSQL(jobs), "[]", "['search-indexer']")); !strings.Contains(got, "indexer") || strings.Contains(got, "reindex") {
		t.Errorf("warnings for service search-indexer = %q, want only indexer's", got)
	}
}
