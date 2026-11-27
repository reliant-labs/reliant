package obsdash

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/alerting"
	"github.com/reliant-labs/forge/pkg/telemetry"
)

func TestTelemetryAlertName(t *testing.T) {
	for service, want := range map[string]string{
		"temporal":     "TemporalTargetDown",
		"pg-bouncer":   "PgBouncerTargetDown",
		"cert-manager": "CertManagerTargetDown",
		"my_db.v2":     "MyDbV2TargetDown",
		"3scale":       "Telemetry3scaleTargetDown",
		"nats":         "NatsTargetDown",
	} {
		if got := TelemetryAlertName(service, "TargetDown"); got != want {
			t.Errorf("TelemetryAlertName(%q) = %q, want %q", service, got, want)
		}
	}
}

func telemetryAlertInput(t *testing.T) AlertInput {
	t.Helper()
	return AlertInputOf(telemetryFixture(t))
}

func TestTelemetryDefaultsAreOneRulePerSystem(t *testing.T) {
	r := rulesByAlert(t, telemetryAlertInput(t), "prod", nil)
	for alert, want := range map[string]struct{ id, service string }{
		"TemporalTargetDown":   {TelemetryTargetDown, "temporal"},
		"TemporalCheckFailing": {TelemetryCheckFailing, "temporal"},
		"ClickhouseTargetDown": {TelemetryTargetDown, "clickhouse"},
	} {
		got, ok := r[alert]
		if !ok {
			t.Errorf("%s missing", alert)
			continue
		}
		if got.ID != want.id || got.Labels["service"] != want.service || got.Labels["severity"] != SeverityWarning || got.For != "5m" {
			t.Errorf("%s = %+v", alert, got)
		}
		if err := alerting.ValidateExpr(got.Expr); err != nil {
			t.Errorf("%s: the alert guard refuses its own expr: %v", alert, err)
		}
		if !strings.Contains(got.Expr, "ServiceName = '"+want.service+"'") {
			t.Errorf("%s does not read its system's service.name:\n%s", alert, got.Expr)
		}
	}
	if _, ok := r["ClickhouseCheckFailing"]; ok {
		t.Error("a system with no check got a check alert")
	}
	if !strings.Contains(r["TemporalTargetDown"].Expr, "deployment.environment.name'] = 'prod'") {
		t.Errorf("a project system's rule must read its env:\n%s", r["TemporalTargetDown"].Expr)
	}
	if strings.Contains(r["ClickhouseTargetDown"].Expr, "deployment.environment.name") {
		t.Errorf("a platform system's signals carry no env; its rule must not filter on one:\n%s", r["ClickhouseTargetDown"].Expr)
	}
}

func TestTelemetryDefaultsAreTunedOnceAndOptedOutPerSystem(t *testing.T) {
	r := rulesByAlert(t, telemetryAlertInput(t), "prod", map[string]AlertTuning{
		TelemetryTargetDown:   {Enabled: true, For: "15m", Labels: map[string]string{"team": "infra"}, Params: map[string]float64{pWindow: 30}},
		TelemetryCheckFailing: {Enabled: false},
	})
	for _, alert := range []string{"TemporalTargetDown", "ClickhouseTargetDown"} {
		if got := r[alert]; got.For != "15m" || got.Labels["team"] != "infra" || !strings.Contains(got.Expr, "INTERVAL 30 MINUTE") {
			t.Errorf("tuning must reach every system's rule: %s %+v", alert, got)
		}
	}
	if _, ok := r["TemporalCheckFailing"]; ok {
		t.Error("a disabled default still raised a rule")
	}

	optedOut := temporalTarget()
	optedOut.DefaultAlerts = false
	if got := AlertTelemetryTargetsOf([]telemetry.Target{optedOut, clickhouseTarget()}); len(got) != 1 || got[0].Service != "clickhouse" {
		t.Errorf("default_alerts = False must leave the system out: %+v", got)
	}

	_, skipped, err := ResolveAlertDefaultsFor(telemetryAlertInput(t), "prod", nil, []string{"metrics"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range skipped {
		ids = append(ids, s.ID)
	}
	if !slices.Contains(ids, TelemetryTargetDown) || !slices.Contains(ids, TelemetryCheckFailing) {
		t.Errorf("an env that opted out of metrics has nothing for the telemetry defaults to read: skipped %v", ids)
	}
}

func TestTelemetryDefaultsReachAProjectWithNoGeneratedDefaults(t *testing.T) {
	// A static site generates no alert defaults; a system it declares still
	// alerts, from the env's render alone.
	rules, _, err := ResolveEnvAlertDefaultsFor(AlertInput{TelemetryTargets: AlertTelemetryTargetsOf([]telemetry.Target{clickhouseTarget()})}, "prod", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Alert != "ClickhouseTargetDown" {
		t.Errorf("rules = %+v", rules)
	}
}

func TestTelemetryDefaultsInTheCatalog(t *testing.T) {
	ids := func(in AlertInput) []string {
		var out []string
		for _, d := range AlertDefaults(in).Defaults {
			out = append(out, d.ID)
		}
		return out
	}
	if got := ids(telemetryAlertInput(t)); !slices.Contains(got, TelemetryTargetDown) || !slices.Contains(got, TelemetryCheckFailing) {
		t.Errorf("a project that declares telemetry must list its defaults: %v", got)
	}
	if got := ids(AlertInputOf(fixture())); slices.Contains(got, TelemetryTargetDown) || slices.Contains(got, TelemetryCheckFailing) {
		t.Errorf("a project that declares none must not: %v", got)
	}
	if !slices.Contains(AlertDefaultIDs(), TelemetryTargetDown) || ValidateAlertTuning(map[string]AlertTuning{TelemetryCheckFailing: {Params: map[string]float64{pWindow: 10}}}) != nil {
		t.Error("the telemetry defaults must be tunable by id")
	}
}

func TestGoldenTelemetryAlertSQL(t *testing.T) {
	rules, err := ResolveAlertDefaults(telemetryAlertInput(t), "prod", nil)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, r := range rules {
		if r.ID != TelemetryTargetDown && r.ID != TelemetryCheckFailing {
			continue
		}
		b.WriteString("-- " + r.Alert + " (" + r.ID + ") severity=" + r.Labels["severity"] + " service=" + r.Labels["service"] + " for=" + r.For + " interval=" + r.Interval + "\n")
		b.WriteString(r.Expr + "\n\n")
	}
	golden(t, "alert-defaults-telemetry.sql", []byte(b.String()))
}

// TestTelemetryAlertsOnClickHouse runs the telemetry defaults against a real
// ClickHouse holding the exporter's gauge and sum tables.
//
// Set FORGE_ALERT_CLICKHOUSE to a running container with clickhouse-client
// (password "pw") whose default database has otel_metrics_gauge and
// otel_metrics_sum.
func TestTelemetryAlertsOnClickHouse(t *testing.T) {
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
	run(`TRUNCATE TABLE otel_metrics_gauge; TRUNCATE TABLE otel_metrics_sum`)
	gauge := func(env, svc, metric, inst, attrs string, ago int, v float64) {
		t.Helper()
		run(fmt.Sprintf(`INSERT INTO otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value) VALUES
(map('deployment.environment.name','%s','service.instance.id','%s'), '%s', '%s', map(%s), now() - INTERVAL %d MINUTE, %g)`, env, inst, svc, metric, attrs, ago, v))
	}
	sum := func(env, svc, metric, attrs string, ago int, v float64) {
		t.Helper()
		run(fmt.Sprintf(`INSERT INTO otel_metrics_sum (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value, AggregationTemporality, IsMonotonic) VALUES
(map('deployment.environment.name','%s'), '%s', '%s', map(%s), now() - INTERVAL %d MINUTE, %g, 2, false)`, env, svc, metric, attrs, ago, v))
	}
	// up: temporal pod-a down now (was up), pod-b up; clickhouse (platform,
	// no env) down; prod's up for a dev-only failure must not count.
	gauge("prod", "temporal", "up", "pod-a:9090", "", 6, 1)
	gauge("prod", "temporal", "up", "pod-a:9090", "", 1, 0)
	gauge("prod", "temporal", "up", "pod-b:9090", "", 1, 1)
	gauge("dev", "temporal", "up", "pod-c:9090", "", 1, 0)
	gauge("", "clickhouse", "up", "ch-0:9363", "", 2, 0)
	// checks: tcp refused throughout; http answered 503 throughout; another
	// http url recovered; an http url that never answered (errors only).
	gauge("prod", "temporal", "tcpcheck.status", "", "'tcpcheck.endpoint','temporal:7233'", 3, 0)
	gauge("prod", "temporal", "tcpcheck.status", "", "'tcpcheck.endpoint','temporal:7233'", 1, 0)
	for _, ago := range []int{3, 1} {
		sum("prod", "temporal", "httpcheck.status", "'http.url','http://temporal:7243/health','http.status_class','2xx'", ago, 0)
		sum("prod", "temporal", "httpcheck.status", "'http.url','http://temporal:7243/health','http.status_class','5xx'", ago, 1)
		sum("prod", "temporal", "httpcheck.error", "'http.url','http://temporal:7244/dead','error.message','refused'", ago, float64(4-ago))
	}
	sum("prod", "temporal", "httpcheck.status", "'http.url','http://temporal:7245/ok','http.status_class','2xx'", 3, 0)
	sum("prod", "temporal", "httpcheck.status", "'http.url','http://temporal:7245/ok','http.status_class','2xx'", 1, 1)

	by := byAlert(func() []AlertRule {
		rules, err := ResolveAlertDefaults(telemetryAlertInput(t), "prod", nil)
		if err != nil {
			t.Fatal(err)
		}
		return rules
	}())
	firing := func(alert string, col int) []string {
		t.Helper()
		var out []string
		for _, line := range strings.Split(run(by[alert].Expr), "\n") {
			if line != "" {
				out = append(out, strings.Split(line, "\t")[col])
			}
		}
		return out
	}
	if got := firing("TemporalTargetDown", 1); !slices.Equal(got, []string{"pod-a:9090"}) {
		t.Errorf("TemporalTargetDown fired for %v, want [pod-a:9090]: the newest up of the env's endpoints", got)
	}
	if got := firing("ClickhouseTargetDown", 1); !slices.Equal(got, []string{"ch-0:9363"}) {
		t.Errorf("ClickhouseTargetDown fired for %v, want [ch-0:9363]: a platform target reads no env", got)
	}
	if got := firing("TemporalCheckFailing", 1); !slices.Equal(got, []string{"http://temporal:7243/health", "http://temporal:7244/dead", "temporal:7233"}) {
		t.Errorf("TemporalCheckFailing fired for %v", got)
	}
}

func TestTelemetryAlertServiceLabelDoesNotIdentifyOrigin(t *testing.T) {
	in := AlertInput{TelemetryTargets: []AlertTelemetryTarget{{Service: "temporal", Scrapes: true, Checks: true}}}
	tuning := map[string]AlertTuning{}
	for _, id := range []string{"telemetry-target-down", "telemetry-check-failing"} {
		tuning[id] = AlertTuning{Enabled: true, Labels: map[string]string{"service": "custom-service"}}
	}
	rules, _, err := ResolveEnvAlertDefaultsFor(in, "prod", tuning, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("got %d rules, want both telemetry defaults", len(rules))
	}
	for _, r := range rules {
		if r.Labels["service"] != "custom-service" {
			t.Errorf("%s: service label = %q, want custom-service", r.ID, r.Labels["service"])
		}
	}

	// Another default cannot hide a name collision by adopting the target's
	// service label either: default IDs, not labels, identify the origin.
	err = validateTelemetryDefaultNames([]AlertRule{{
		ID: "another-default", Alert: "TemporalTargetDown", Labels: map[string]string{"service": "temporal"},
	}}, map[string]telemetryAlertOrigin{
		"TemporalTargetDown": {service: "temporal", defaultID: "telemetry-target-down"},
	})
	if err == nil || !strings.Contains(err.Error(), "another-default") {
		t.Fatalf("expected collision with another-default, got %v", err)
	}
}

// TestTelemetryAlertNamesNeverCollide: TelemetryAlertName keeps only a
// service.name's letters and digits, so foo.bar, foo_bar and foo-bar would
// all raise FooBarTargetDown. Two systems must never share a rule identity:
// the resolver refuses the pair, naming both, instead of emitting duplicates.
func TestTelemetryAlertNamesNeverCollide(t *testing.T) {
	for _, pair := range [][2]string{{"foo.bar", "foo_bar"}, {"foo_bar", "foo-bar"}, {"foo.bar", "foo-bar"}} {
		in := AlertInput{TelemetryTargets: []AlertTelemetryTarget{
			{Service: pair[0], Scrapes: true, Checks: true},
			{Service: pair[1], Scrapes: true, Checks: true},
		}}
		_, _, err := ResolveEnvAlertDefaultsFor(in, "prod", nil, nil)
		if err == nil {
			t.Fatalf("%q and %q both raise FooBarTargetDown, and the resolver accepted it", pair[0], pair[1])
		}
		for _, want := range []string{pair[0], pair[1], "FooBar", "default_alerts = False"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%v: the error must name %q", err, want)
			}
		}
	}

	// Names that differ in their letters or digits stay distinct rules.
	in := AlertInput{TelemetryTargets: []AlertTelemetryTarget{{Service: "foo.bar", Scrapes: true, Checks: true}, {Service: "foo-baz", Scrapes: true}}}
	rules, _, err := ResolveEnvAlertDefaultsFor(in, "prod", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range rules {
		if seen[r.Alert] {
			t.Errorf("duplicate rule identity %s", r.Alert)
		}
		seen[r.Alert] = true
	}
	for _, want := range []string{"FooBarTargetDown", "FooBarCheckFailing", "FooBazTargetDown"} {
		if !seen[want] {
			t.Errorf("%s missing from %v", want, seen)
		}
	}
}
