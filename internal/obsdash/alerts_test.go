package obsdash

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

func TestAlertInputFollowsTheDashboardInput(t *testing.T) {
	in := AlertInputOf(fixture())
	if in.Project != "shop" || in.FrontendService != "shop-frontend" {
		t.Fatalf("input = %+v", in)
	}
	want := []AlertService{{"billing", "billing.v1.BillingService"}, {"orders", "orders.v1.OrdersService"}}
	if len(in.Services) != 2 || in.Services[0] != want[0] || in.Services[1] != want[1] {
		t.Errorf("services = %+v, want %+v (sorted, rpc-bearing only)", in.Services, want)
	}
	noRPC := fixture()
	noRPC.Services = append(noRPC.Services, Service{Name: "empty", Package: "empty.v1", ProtoService: "EmptyService"})
	noRPC.Frontends = nil
	got := AlertInputOf(noRPC)
	if len(got.Services) != 2 || got.FrontendService != "" {
		t.Errorf("a service with no rpc, or no frontend, leaked into the input: %+v", got)
	}
}

func TestGoldenDefaultAlertSQL(t *testing.T) {
	rules, err := ResolveAlertDefaults(AlertInputOf(fixture()), "dev", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 16 {
		t.Fatalf("got %d default rules, want the 5 telemetry rules (error rate, latency, silent, JS errors, LCP), the 5 customer-metadata rules, the three issue markers (new, regressed, spike), DeployFailed and the two quota levels", len(rules))
	}
	var b strings.Builder
	for _, r := range rules {
		b.WriteString("-- " + r.Alert + " (" + r.ID + ") severity=" + r.Labels["severity"] + " for=" + r.For + " interval=" + r.Interval + "\n")
		b.WriteString(r.Expr + "\n\n")
	}
	golden(t, "alert-defaults-dev.sql", []byte(b.String()))
	catalog, err := MarshalAlertDefaults(AlertInputOf(fixture()))
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "alert-defaults.json", catalog)
}

func TestDefaultsRouteToNoPagerAndAreMetricsBased(t *testing.T) {
	rules, _ := ResolveAlertDefaults(AlertInputOf(fixture()), "dev", nil)
	for _, r := range rules {
		if r.Labels["severity"] == "" {
			t.Errorf("%s has no severity", r.Alert)
		}
		if _, paged := r.Labels["page"]; paged {
			t.Errorf("%s carries a paging label; defaults must never page", r.Alert)
		}
	}
	for _, id := range []string{"error-rate", "p95-latency", "service-silent"} {
		for _, r := range rules {
			if r.ID != id {
				continue
			}
			if strings.Contains(r.Expr, "otel_traces") {
				t.Errorf("%s reads sampled spans; RED must come from the unsampled rpc.server.* metrics", id)
			}
			if !strings.Contains(r.Expr, "otel_metrics_") {
				t.Errorf("%s does not read a metrics table", id)
			}
		}
	}
	for _, r := range rules {
		if r.ID == "frontend-js-errors" && !strings.Contains(r.Expr, "lower(StatusCode) = 'error'") {
			t.Error("the span-based default must count only ERROR spans, which the tail sampler always keeps")
		}
	}
}

func TestDefaultsScopeToTheEnvAndTheProjectsServices(t *testing.T) {
	rules, _ := ResolveAlertDefaults(AlertInputOf(fixture()), "stag'ing", nil)
	for _, r := range rules {
		if r.ID != "error-rate" && r.ID != "p95-latency" && r.ID != "service-silent" && r.ID != "frontend-js-errors" {
			continue
		}
		if !strings.Contains(r.Expr, `'stag\'ing'`) {
			t.Errorf("%s does not scope to the (quoted) env: %s", r.ID, r.Expr)
		}
		if r.ID != "frontend-js-errors" && !strings.Contains(r.Expr, "'orders.v1.OrdersService'") {
			t.Errorf("%s does not scope to the project's rpc services", r.ID)
		}
	}
}

func TestSilentDefaultNeedsSteadyTraffic(t *testing.T) {
	rules, _ := ResolveAlertDefaults(AlertInputOf(fixture()), "dev", nil)
	for _, r := range rules {
		if r.ID != "service-silent" {
			continue
		}
		if !strings.Contains(r.Expr, "uniqExactIf(toStartOfHour(t)") || !strings.Contains(r.Expr, ">= 18") {
			t.Errorf("service-silent must require 18 active hours of the previous 24h inside the SQL: %s", r.Expr)
		}
		if r.For != "0s" {
			t.Errorf("service-silent for = %q: the silence window is in the SQL, so the hold time is 0s", r.For)
		}
	}
}

func TestTuningDisablesRetargetsAndRefusesTypos(t *testing.T) {
	in := AlertInputOf(fixture())
	rules, err := ResolveAlertDefaults(in, "dev", map[string]AlertTuning{
		"error-rate":  {Enabled: false},
		"p95-latency": {Enabled: true, Labels: map[string]string{"severity": "critical"}, For: "15m", Params: map[string]float64{"threshold_seconds": 2.5}},
	})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]AlertRule{}
	for _, r := range rules {
		by[r.ID] = r
	}
	if _, ok := by["error-rate"]; ok {
		t.Error("a disabled default is still resolved")
	}
	p95 := by["p95-latency"]
	if p95.Labels["severity"] != "critical" || p95.For != "15m" || !strings.Contains(p95.Expr, "value > 2.5") {
		t.Errorf("tuning not applied: %+v", p95)
	}
	for name, tuning := range map[string]map[string]AlertTuning{
		"unknown default": {"nope": {Enabled: true}},
		"unknown param":   {"error-rate": {Enabled: true, Params: map[string]float64{"thresold": 0.1}}},
		"zero threshold":  {"error-rate": {Enabled: true, Params: map[string]float64{"threshold": 0}}},
	} {
		if _, err := ResolveAlertDefaults(in, "dev", tuning); err == nil {
			t.Errorf("%s: want a refusal", name)
		}
	}
}

func TestDefaultsFollowTheProject(t *testing.T) {
	// Only the platform rules apply to every project: a failed deploy and a quota state need no service.
	ids := func(rules []AlertRule) string {
		seen := map[string]bool{}
		var out []string
		for _, r := range rules {
			if !seen[r.ID] {
				seen[r.ID] = true
				out = append(out, r.ID)
			}
		}
		return strings.Join(out, ",")
	}
	none := AlertInput{Project: "x"}
	rules, _ := ResolveAlertDefaults(none, "dev", nil)
	if got := ids(rules); got != "deploy-failed,observability-quota" {
		t.Errorf("a project with no service and no frontend has defaults %q", got)
	}
	feOnly := AlertInput{Project: "x", FrontendService: "x-frontend"}
	rules, _ = ResolveAlertDefaults(feOnly, "dev", nil)
	if got := ids(rules); got != "deploy-failed,frontend-js-errors,frontend-lcp-poor,issue-regressed,issue-spike,new-issue,observability-quota" {
		t.Errorf("a frontend-only project: %q", got)
	}
}

// TestAlertDefaultsFileRoundTrips: what Write puts on disk — the defaults
// file without its services, and each service's ServicePart — reads back as
// the input it was written from. The file itself lists no service, so two
// branches that each add one do not both change it.
func TestAlertDefaultsFileRoundTrips(t *testing.T) {
	b, err := MarshalAlertDefaults(AlertInputOf(fixture()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"services"`) || strings.Contains(string(b), "OrdersService") {
		t.Errorf("%s lists services; they belong in each ServicePart:\n%s", AlertDefaultsFile, b)
	}

	dir := t.TempDir()
	if _, err := Write(dir, fixture(), &checksums.FileChecksums{}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadAlertInput(dir)
	if err != nil || !ok {
		t.Fatalf("ReadAlertInput = ok %v, err %v", ok, err)
	}
	want, _ := json.Marshal(AlertInputOf(fixture()))
	have, _ := json.Marshal(got)
	if string(want) != string(have) {
		t.Errorf("round trip changed the input:\n%s\n%s", want, have)
	}
	if _, ok, err := ReadAlertInput(t.TempDir()); ok || err != nil {
		t.Errorf("a project with no defaults file: ok %v, err %v; want no defaults and no error", ok, err)
	}
	if _, err := ParseAlertDefaults([]byte(`{"version":"forge.alert-defaults/v0"}`)); err == nil {
		t.Error("an unknown file version must be refused")
	}
}

func byAlert(rules []AlertRule) map[string]AlertRule {
	m := map[string]AlertRule{}
	for _, r := range rules {
		m[r.Alert] = r
	}
	return m
}

var metadataRuleIDs = []string{"crash-loop", "oom-killed", "memory-near-limit", "image-pull-failed", "evicted"}

func TestMetadataDefaultsReadCustomerMetadataByEnvID(t *testing.T) {
	rules, _ := ResolveAlertDefaults(AlertInputOf(fixture()), "dev", nil)
	got := map[string]AlertRule{}
	for _, r := range rules {
		got[r.ID] = r
	}
	for _, id := range metadataRuleIDs {
		r, ok := got[id]
		if !ok {
			t.Fatalf("default %s is not resolved", id)
		}
		// The node agent stamps reliant.env.id, never deployment.environment.name, and the id is the
		// control plane's to bind: forge records the placeholder and never invents an id.
		if !strings.Contains(r.Expr, EnvIDPlaceholder) || strings.Contains(r.Expr, "'dev'") || strings.Contains(r.Expr, "deployment.environment.name") {
			t.Errorf("%s must scope by %s, not by the env name: %s", id, EnvIDPlaceholder, r.Expr)
		}
		if err := validateExprForTest(BindEnvID(r.Expr, "env-1")); err != nil {
			t.Errorf("%s once bound: %v", id, err)
		}
		if strings.Contains(BindEnvID(r.Expr, "env-1"), EnvIDPlaceholder) {
			t.Errorf("%s: BindEnvID left a placeholder", id)
		}
		if r.Labels["severity"] == "" {
			t.Errorf("%s has no severity", id)
		}
	}
	// Restarts and memory are gauges: a restart is the growth of one pod's count, never a sum of raw values.
	if e := got["crash-loop"].Expr; !strings.Contains(e, "max(Value) - min(Value)") || !strings.Contains(e, "otel_metrics_gauge") || !strings.Contains(e, "otel_logs") {
		t.Errorf("crash-loop must difference per-pod restart gauges and read BackOff events: %s", e)
	}
	if e := got["oom-killed"].Expr; !strings.Contains(e, "k8s.container.last_terminated_reason") || !strings.Contains(e, "OOMKilling") {
		t.Errorf("oom-killed must read the terminated reason and the OOMKilling event: %s", e)
	}
	if e := got["memory-near-limit"].Expr; !strings.Contains(e, "k8s.container.memory_limit_utilization") || !strings.Contains(e, "avg(Value)") {
		t.Errorf("memory-near-limit must average the utilization ratio so a spike does not fire: %s", e)
	}
	if got["memory-near-limit"].For != "10m" || got["crash-loop"].Labels["severity"] != "critical" || got["oom-killed"].Labels["severity"] != "critical" {
		t.Errorf("default holds/severities changed: %+v", got)
	}
}

func TestOptingOutOfMetadataSkipsTheMetadataDefaults(t *testing.T) {
	in := AlertInputOf(fixture())
	rules, skipped, err := ResolveAlertDefaultsFor(in, "dev", nil, []string{"metadata"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rules {
		for _, id := range metadataRuleIDs {
			if r.ID == id {
				t.Errorf("%s is recorded for an env that opted out of metadata: it can only ever be empty", id)
			}
		}
	}
	if len(skipped) != len(metadataRuleIDs) {
		t.Errorf("skipped = %+v, want the %d metadata defaults", skipped, len(metadataRuleIDs))
	}
	by := byAlert(rules)
	for _, keep := range []string{"ServiceErrorRateHigh", "DeployFailed", "ObservabilityQuotaOver100"} {
		if _, ok := by[keep]; !ok {
			t.Errorf("%s must survive a metadata opt-out", keep)
		}
	}
	// A different category changes nothing here.
	rules, skipped, _ = ResolveAlertDefaultsFor(in, "dev", nil, []string{"logs", "profiles"})
	if len(skipped) != 0 || len(rules) != 16 {
		t.Errorf("opting out of logs skipped %v and left %d rules", skipped, len(rules))
	}
}

func TestPlatformDefaultsAreMarkersTheControlPlaneRecognizes(t *testing.T) {
	rules, _ := ResolveAlertDefaults(AlertInputOf(fixture()), "dev", nil)
	by := byAlert(rules)
	want := map[string][2]string{
		"DeployFailed":              {"deploy-failed", "critical"},
		"ObservabilityQuotaOver100": {"observability-quota:over_100", "warning"},
		"ObservabilityQuotaOver150": {"observability-quota:over_150", "critical"},
	}
	for name, w := range want {
		r, ok := by[name]
		if !ok {
			t.Fatalf("%s missing", name)
		}
		if !IsPlatformExpr(r.Expr) || !strings.Contains(r.Expr, "reliant:platform "+w[0]+" env:"+EnvIDPlaceholder) || strings.Contains(r.Expr, " off") {
			t.Errorf("%s expr = %q", name, r.Expr)
		}
		if r.Labels["severity"] != w[1] || r.ID != strings.Split(w[0], ":")[0] {
			t.Errorf("%s = id %s labels %v", name, r.ID, r.Labels)
		}
		if err := validateExprForTest(BindEnvID(r.Expr, "env-1")); err != nil {
			t.Errorf("%s once bound is not an acceptable expr: %v", name, err)
		}
	}
	// Whatever evaluates it without knowing the marker finds no row: it can never fire spuriously.
	if !strings.Contains(by["DeployFailed"].Expr, "WHERE 0") {
		t.Error("a platform marker must be a query that returns no row")
	}
	for _, id := range []string{"error-rate", "crash-loop"} {
		for _, r := range rules {
			if r.ID == id && IsPlatformExpr(r.Expr) {
				t.Errorf("%s is a telemetry rule and must not look like a platform marker", id)
			}
		}
	}
}

func TestPlatformDefaultsAreTunedAndDisabledByID(t *testing.T) {
	in := AlertInputOf(fixture())
	rules, err := ResolveAlertDefaults(in, "dev", map[string]AlertTuning{
		"deploy-failed":       {Enabled: true, For: "15m", Labels: map[string]string{"team": "platform", "severity": "warning"}},
		"observability-quota": {Enabled: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	by := byAlert(rules)
	d := by["DeployFailed"]
	if d.For != "15m" || d.Labels["severity"] != "warning" || d.Labels["team"] != "platform" {
		t.Errorf("deploy-failed tuning not applied: %+v", d)
	}
	// A disabled platform default is still RECORDED, marked off: the control plane injects the built-ins itself,
	// so absence in the shape could not tell "disabled" from "recorded by a forge that predates the rule".
	for _, n := range []string{"ObservabilityQuotaOver100", "ObservabilityQuotaOver150"} {
		r, ok := by[n]
		if !ok || !strings.HasSuffix(r.Expr, " off */") {
			t.Errorf("%s: a disabled platform default must be recorded as off, got %+v", n, r)
		}
	}
	if _, err := ResolveAlertDefaults(in, "dev", map[string]AlertTuning{"deploy-failed": {Enabled: true, Params: map[string]float64{"x": 1}}}); err == nil {
		t.Error("a platform default takes no params: a typo must be refused")
	}
}

func TestMetadataDefaultsAreTunableAndDisablable(t *testing.T) {
	in := AlertInputOf(fixture())
	rules, err := ResolveAlertDefaults(in, "dev", map[string]AlertTuning{
		"crash-loop":        {Enabled: true, Params: map[string]float64{"min_restarts": 5, "window_minutes": 30}, For: "2m"},
		"memory-near-limit": {Enabled: true, Params: map[string]float64{"threshold": 0.8}},
		"evicted":           {Enabled: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	by := byAlert(rules)
	if c := by["WorkloadCrashLooping"]; c.For != "2m" || !strings.Contains(c.Expr, ">= 5") || !strings.Contains(c.Expr, "INTERVAL 30 MINUTE") {
		t.Errorf("crash-loop tuning not applied: %+v", c)
	}
	if m := by["WorkloadMemoryNearLimit"]; !strings.Contains(m.Expr, "value > 0.8") {
		t.Errorf("memory threshold not applied: %s", m.Expr)
	}
	if _, ok := by["WorkloadEvicted"]; ok {
		t.Error("a disabled metadata default is still resolved")
	}
}

func TestBindEnvIDQuotesTheID(t *testing.T) {
	if got := BindEnvID("a = "+EnvIDPlaceholder+" AND b = "+EnvIDPlaceholder, `x'y`); got != `a = 'x\'y' AND b = 'x\'y'` {
		t.Errorf("BindEnvID = %s", got)
	}
}

func TestUserRulesMayNotTakeADefaultsName(t *testing.T) {
	names := map[string]bool{}
	for _, n := range AlertDefaultAlertNames() {
		names[n] = true
	}
	for _, n := range []string{"WorkloadCrashLooping", "WorkloadOOMKilled", "WorkloadMemoryNearLimit", "WorkloadImagePullFailed", "WorkloadEvicted", "DeployFailed", "ObservabilityQuotaOver100", "ObservabilityQuotaOver150"} {
		if !names[n] {
			t.Errorf("%s is not reserved against user rules", n)
		}
	}
}

// The control plane injects these three rules for a shape recorded by a forge
// that predates them (control-plane internal/alerting/builtin.go), and its test
// TestInjectedBuiltinsEqualWhatForgeRecords pins the SAME literals. If a value
// here changes, change it there in the same pin bump; until then a customer on
// the older forge and one on this forge get different rules.
func TestPlatformDefaultsAreExactlyWhatTheControlPlaneInjects(t *testing.T) {
	rules, _ := ResolveAlertDefaults(AlertInputOf(fixture()), "dev", nil)
	type pinned struct{ severity, hold, interval, expr, summary string }
	want := map[string]pinned{
		"DeployFailed": {"critical", "5m", "1m",
			"SELECT 1 AS value WHERE 0 /* reliant:platform deploy-failed env:{reliant_env_id:String} */",
			"The release promoted to this env failed to converge on {{ $labels.cluster }}: {{ $labels.reason }}"},
		"ObservabilityQuotaOver100": {"warning", "0s", "1m",
			"SELECT 1 AS value WHERE 0 /* reliant:platform observability-quota:over_100 env:{reliant_env_id:String} */",
			"This org's observability usage is at {{ $value }}% of its monthly allowance; the gateway is limiting ingest"},
		"ObservabilityQuotaOver150": {"critical", "0s", "1m",
			"SELECT 1 AS value WHERE 0 /* reliant:platform observability-quota:over_150 env:{reliant_env_id:String} */",
			"This org's observability usage is at {{ $value }}% of its monthly allowance; the gateway is limiting ingest"},
		"NewIssue": {"warning", "0s", "1m",
			"SELECT 1 AS value WHERE 0 /* reliant:platform new-issue env:{reliant_env_id:String} */",
			"New issue in {{ $labels.service }} ({{ $labels.env }}): {{ $labels.title }}. Details: {{ $labels.console_url }}"},
		"IssueRegressed": {"warning", "0s", "1m",
			"SELECT 1 AS value WHERE 0 /* reliant:platform issue-regressed env:{reliant_env_id:String} */",
			"A resolved issue came back in {{ $labels.service }} ({{ $labels.env }}): {{ $labels.title }}. Details: {{ $labels.console_url }}"},
		"IssueSpike": {"warning", "0s", "5m",
			"SELECT 1 AS value WHERE 0 /* reliant:platform issue-spike env:{reliant_env_id:String} */",
			"Issue spiking in {{ $labels.service }} ({{ $labels.env }}): {{ $labels.title }}, {{ $value }} events/hour, over 5 times its rate the day before. Details: {{ $labels.console_url }}"},
	}
	by := byAlert(rules)
	for name, w := range want {
		r := by[name]
		got := pinned{r.Labels["severity"], r.For, r.Interval, r.Expr, r.Annotations["summary"]}
		if got != w {
			t.Errorf("%s = %+v, want %+v", name, got, w)
		}
	}
}

// The issue alerts are platform markers like deploy-failed: "new" and
// "regressed" are state transitions in the control plane's issue registry and a
// spike is measured per issue, so forge records only how the env tuned them and
// the control plane evaluates them. They exist for a project with anything that
// can raise an issue: a service or a frontend.
func TestIssueAlertsArePlatformMarkers(t *testing.T) {
	rules, _ := ResolveAlertDefaults(AlertInputOf(fixture()), "dev", nil)
	by := byAlert(rules)
	for name, key := range map[string]string{
		"NewIssue":       "new-issue",
		"IssueRegressed": "issue-regressed",
		"IssueSpike":     "issue-spike",
	} {
		r, ok := by[name]
		if !ok {
			t.Fatalf("%s missing", name)
		}
		want := "SELECT 1 AS value WHERE 0 /* reliant:platform " + key + " env:{reliant_env_id:String} */"
		if r.Expr != want || r.ID != key || r.Labels["severity"] != SeverityWarning {
			t.Errorf("%s = id %s severity %s expr %q", name, r.ID, r.Labels["severity"], r.Expr)
		}
		if !IsPlatformExpr(r.Expr) {
			t.Errorf("%s is not recognized as a platform marker", name)
		}
		if err := validateExprForTest(BindEnvID(r.Expr, "env-1")); err != nil {
			t.Errorf("%s once bound is not an acceptable expr: %v", name, err)
		}
	}

	noFrontend := AlertInputOf(Input{Project: "x", Services: fixture().Services})
	rules, _ = ResolveAlertDefaults(noFrontend, "dev", nil)
	by = byAlert(rules)
	for _, r := range rules {
		if strings.HasPrefix(r.Alert, "Frontend") {
			t.Errorf("%s applies to a project with no frontend", r.Alert)
		}
	}
	for _, name := range []string{"NewIssue", "IssueRegressed", "IssueSpike"} {
		if _, ok := by[name]; !ok {
			t.Errorf("%s does not apply to a project with services and no frontend (server errors are issues too)", name)
		}
	}
}

func TestIssueAlertsAreTunedAndDisabledByID(t *testing.T) {
	in := AlertInputOf(fixture())
	rules, _ := ResolveAlertDefaults(in, "dev", map[string]AlertTuning{
		"new-issue":       {Enabled: true, For: "2m", Labels: map[string]string{"severity": "critical"}},
		"issue-regressed": {Enabled: false},
	})
	by := byAlert(rules)
	if r := by["NewIssue"]; r.For != "2m" || r.Labels["severity"] != "critical" || strings.Contains(r.Expr, " off") {
		t.Errorf("tuned NewIssue = %+v", r)
	}
	// A disabled platform default is still recorded, marked off, so the control
	// plane does not inject it back.
	if r := by["IssueRegressed"]; !strings.HasSuffix(r.Expr, " off */") {
		t.Errorf("disabled IssueRegressed = %q, want it recorded as off", r.Expr)
	}

	// Params are refused: a platform default has none to tune.
	if err := ValidateAlertTuning(map[string]AlertTuning{"new-issue": {Enabled: true, Params: map[string]float64{"events": 9}}}); err == nil {
		t.Error("a param on a platform default must be refused")
	}
}

func TestFrontendLCPPoorDefault(t *testing.T) {
	rules, _ := ResolveAlertDefaults(AlertInputOf(fixture()), "dev", nil)
	r, ok := byAlert(rules)["FrontendLCPPoor"]
	if !ok {
		t.Fatal("FrontendLCPPoor missing")
	}
	if r.ID != "frontend-lcp-poor" || r.Labels["severity"] != SeverityInfo || r.For != "10m" {
		t.Errorf("FrontendLCPPoor = id %s severity %s for %s", r.ID, r.Labels["severity"], r.For)
	}
	// p75 over 30 minutes, more than 4000 ms, with at least 100 samples.
	for _, want := range []string{
		"quantile(0.75)(toFloat64OrZero(SpanAttributes['lcp']))",
		"INTERVAL 30 MINUTE",
		"SpanName = 'webvitals'",
		"ServiceName = 'shop-frontend'",
		"ResourceAttributes['deployment.environment.name'] = 'dev'",
		"HAVING samples >= 100 AND value > 4000",
	} {
		if !strings.Contains(r.Expr, want) {
			t.Errorf("FrontendLCPPoor expr lacks %q:\n%s", want, r.Expr)
		}
	}
	if err := validateExprForTest(r.Expr); err != nil {
		t.Errorf("FrontendLCPPoor is not an acceptable expr: %v", err)
	}

	tuned, _ := ResolveAlertDefaults(AlertInputOf(fixture()), "prod", map[string]AlertTuning{
		"frontend-lcp-poor": {Enabled: true, Params: map[string]float64{"threshold_ms": 2500, "window_minutes": 60, "min_samples": 300}},
	})
	if e := byAlert(tuned)["FrontendLCPPoor"].Expr; !strings.Contains(e, "HAVING samples >= 300 AND value > 2500") || !strings.Contains(e, "INTERVAL 60 MINUTE") || !strings.Contains(e, "= 'prod'") {
		t.Errorf("tuned FrontendLCPPoor expr:\n%s", e)
	}
}

func TestJSErrorSpikeIgnoresStringOnlyConsoleErrors(t *testing.T) {
	rules, _ := ResolveAlertDefaults(AlertInputOf(fixture()), "dev", nil)
	e := byAlert(rules)["FrontendJSErrorSpike"].Expr
	// console.error("text") makes an error span with no exception event; only
	// console.error(err) does. The other error sources always carry one.
	if !strings.Contains(e, "(SpanName != 'console.error' OR has(Events.Name, 'exception'))") {
		t.Errorf("FrontendJSErrorSpike does not exclude string-only console errors:\n%s", e)
	}
	// The fetch / XHR exclusion that was there stays.
	if !strings.Contains(e, "NOT IN ('fetch', 'xml-http-request')") {
		t.Error("the fetch/xhr exclusion was lost")
	}
}

func TestFrontendDefaultsTakeNoneOfTheNamesAUserRuleMayUse(t *testing.T) {
	names := map[string]bool{}
	for _, n := range AlertDefaultAlertNames() {
		names[n] = true
	}
	for _, n := range []string{"NewIssue", "IssueRegressed", "IssueSpike", "FrontendLCPPoor", "FrontendJSErrorSpike"} {
		if !names[n] {
			t.Errorf("%s is not reserved", n)
		}
	}
}
