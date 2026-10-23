package obsdash

import (
	"os"
	"os/exec"
	"strings"
	"testing"
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
		Project: "shop", FrontendService: "shop-frontend",
		Services: []AlertService{
			{"api", "a.v1.S"}, {"quiet", "q.v1.Q"}, {"reset", "r.v1.R"}, {"delta", "d.v1.D"},
			{"fast", "f.v1.F"}, {"stopped", "s.v1.T"}, {"healthy", "h.v1.H"}, {"sparse", "p.v1.P"}, {"young", "y.v1.Y"},
		},
	}
	rules, err := ResolveAlertDefaults(in, "dev", nil)
	if err != nil {
		t.Fatal(err)
	}
	// FrontendService is one name per project; the SQL is parameterized on it, so
	// each frontend-* case below re-resolves with the seeded service it is about.
	frontendFired := func(service, id string) bool {
		t.Helper()
		fin := in
		fin.FrontendService = service
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
		"frontend-js-errors": {"shop-frontend"},
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
