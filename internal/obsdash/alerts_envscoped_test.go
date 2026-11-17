package obsdash

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The env-scoped defaults are the ones a project with no generated alert
// defaults (a static site) records: the uptime defaults, over the env's own
// checks, with exactly the SQL a project with generated defaults records.
func TestEnvScopedDefaultsAreTheUptimeDefaults(t *testing.T) {
	site, _, err := ResolveEnvAlertDefaultsFor(AlertInput{UptimeChecks: []string{"web"}}, "prod", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range site {
		names = append(names, r.Alert)
	}
	if got := strings.Join(names, ","); got != "CertificateExpiring,CertificateExpiringSoon,UptimeCheckDown,UptimeCheckFailing" {
		t.Fatalf("env-scoped defaults = %s, want exactly the four uptime rules", got)
	}
	project := rulesByAlert(t, uptimeInput("web"), "prod", nil)
	for _, r := range site {
		p := project[r.Alert]
		if r.ID != p.ID || r.Expr != p.Expr || r.For != p.For || r.Interval != p.Interval || r.Labels["severity"] != p.Labels["severity"] || r.Annotations["summary"] != p.Annotations["summary"] {
			t.Errorf("%s differs between a site and a project with generated defaults:\nsite:    %+v\nproject: %+v", r.Alert, r, p)
		}
	}

	if rules, _, err := ResolveEnvAlertDefaultsFor(AlertInput{}, "prod", nil, nil); err != nil || len(rules) != 0 {
		t.Errorf("an env with no uptime checks got %v (%v): an uptime rule there could only ever be silent", rules, err)
	}
	// The per-env opt-out is the existing one: forge.Alerts.defaults by id.
	rules, _, err := ResolveEnvAlertDefaultsFor(AlertInput{UptimeChecks: []string{"web"}}, "prod", map[string]AlertTuning{
		"uptime-check-failing": {Enabled: false}, "certificate-expiring": {Enabled: false},
	}, nil)
	if err != nil || len(rules) != 0 {
		t.Errorf("both uptime defaults disabled: got %v (%v)", rules, err)
	}
	if _, _, err := ResolveEnvAlertDefaultsFor(AlertInput{UptimeChecks: []string{"web"}}, "prod", map[string]AlertTuning{"uptime-chek-failing": {}}, nil); err == nil {
		t.Error("a misspelled default id is accepted")
	}
	// Opting out of metrics leaves the prober's metrics unread, as for any project.
	rules, skipped, err := ResolveEnvAlertDefaultsFor(AlertInput{UptimeChecks: []string{"web"}}, "prod", nil, []string{"metrics"})
	if err != nil || len(rules) != 0 || len(skipped) != 2 {
		t.Errorf("metrics opted out: rules %v skipped %v (%v)", rules, skipped, err)
	}
}

// TestUptimeAlertsNeverFireWithoutData runs the rules a static site records
// against a real ClickHouse: with no uptime results at all (the prober is not
// running, or has not reached the env yet) nothing fires; with healthy
// results nothing fires; with failing results they fire.
//
// Set FORGE_ALERT_CLICKHOUSE to a running container with clickhouse-client
// (password "pw") whose default database has otel_metrics_gauge.
func TestUptimeAlertsNeverFireWithoutData(t *testing.T) {
	container := os.Getenv("FORGE_ALERT_CLICKHOUSE")
	if testing.Short() || container == "" {
		t.Skip("needs a ClickHouse container; set FORGE_ALERT_CLICKHOUSE=<container>")
	}
	run := func(sql string) string {
		t.Helper()
		cmd := exec.Command("docker", "exec", "-i", container, "clickhouse-client", "--password", "pw", "--multiquery", "--format", "JSONEachRow")
		cmd.Stdin = strings.NewReader(sql)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("clickhouse: %v\n%s\n--- sql ---\n%s", err, out, sql)
		}
		return strings.TrimSpace(string(out))
	}
	rules, _, err := ResolveEnvAlertDefaultsFor(AlertInput{UptimeChecks: []string{"web"}}, "prod", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 4 {
		t.Fatalf("got %d rules, want the four uptime rules", len(rules))
	}
	firing := func() []string {
		t.Helper()
		var out []string
		for _, r := range rules {
			if rows := run(r.Expr); rows != "" {
				out = append(out, r.Alert)
			}
		}
		return out
	}
	// probe writes one probe of check web, `ago` minutes back.
	probe := func(ago, up int, status, errType string, certDays float64) {
		t.Helper()
		res := "map('deployment.environment.name','prod')"
		run(fmt.Sprintf(`INSERT INTO otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value) VALUES
(%[1]s, 'uptime-prober', 'uptime.check.up', map('check','web','http.response.status_code','%[3]s','error.type','%[4]s'), now() - INTERVAL %[2]d MINUTE, %[5]d),
(%[1]s, 'uptime-prober', 'uptime.check.duration', map('check','web'), now() - INTERVAL %[2]d MINUTE, 0.2),
(%[1]s, 'uptime-prober', 'tls.cert.expiry_seconds', map('check','web'), now() - INTERVAL %[2]d MINUTE, %[6]g)`,
			res, ago, status, errType, up, certDays*86400))
	}

	run(`TRUNCATE TABLE otel_metrics_gauge`)
	if got := firing(); len(got) != 0 {
		t.Errorf("with no uptime results at all, %v fired: a site whose prober is not running must not page", got)
	}
	// Another env's failures and another check's are not this site's data either.
	for _, ec := range [][2]string{{"dev", "web"}, {"prod", "api"}} {
		run(fmt.Sprintf(`INSERT INTO otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value)
SELECT map('deployment.environment.name', '%s'), 'uptime-prober', 'uptime.check.up', map('check', '%s', 'error.type', 'timeout'), now() - INTERVAL number MINUTE, 0
FROM numbers(12)`, ec[0], ec[1]))
	}
	if got := firing(); len(got) != 0 {
		t.Errorf("only another env's or another check's results exist, and %v fired", got)
	}

	for ago := 12; ago >= 1; ago-- {
		probe(ago, 1, "200", "", 80)
	}
	if got := firing(); len(got) != 0 {
		t.Errorf("healthy results, a far certificate: %v fired", got)
	}

	run(`TRUNCATE TABLE otel_metrics_gauge`)
	for ago := 6; ago >= 1; ago-- {
		probe(ago, 0, "503", "status", 80)
	}
	if got := strings.Join(firing(), ","); got != "UptimeCheckFailing" {
		t.Errorf("six failed probes in a row fired %q, want UptimeCheckFailing", got)
	}
	run(`TRUNCATE TABLE otel_metrics_gauge`)
	for ago := 12; ago >= 1; ago-- {
		probe(ago, 0, "", "timeout", 2)
	}
	if got := strings.Join(firing(), ","); got != "CertificateExpiringSoon,UptimeCheckDown" {
		t.Errorf("twelve failed probes and a 2-day certificate fired %q, want CertificateExpiringSoon and UptimeCheckDown", got)
	}
	run(`TRUNCATE TABLE otel_metrics_gauge`)
}
