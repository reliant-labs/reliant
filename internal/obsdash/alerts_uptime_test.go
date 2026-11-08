package obsdash

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/alerting"
	"github.com/reliant-labs/forge/pkg/release"
)

func uptimeInput(checks ...string) AlertInput {
	in := AlertInputOf(fixture())
	in.UptimeChecks = checks
	return in
}

func rulesByAlert(t *testing.T, in AlertInput, env string, tuning map[string]AlertTuning) map[string]AlertRule {
	t.Helper()
	rules, err := ResolveAlertDefaults(in, env, tuning)
	if err != nil {
		t.Fatal(err)
	}
	return byAlert(rules)
}

func TestUptimeDefaultsApplyOnlyToAnEnvWithChecks(t *testing.T) {
	if r := rulesByAlert(t, uptimeInput(), "prod", nil); r["UptimeCheckFailing"].Alert != "" || r["CertificateExpiring"].Alert != "" {
		t.Error("an env with no uptime checks got an uptime alert: it could only ever be silent")
	}
	r := rulesByAlert(t, uptimeInput("web"), "prod", nil)
	for alert, want := range map[string]struct{ id, severity string }{
		"UptimeCheckFailing":      {"uptime-check-failing", SeverityWarning},
		"UptimeCheckDown":         {"uptime-check-failing", SeverityCritical},
		"CertificateExpiring":     {"certificate-expiring", SeverityWarning},
		"CertificateExpiringSoon": {"certificate-expiring", SeverityCritical},
	} {
		got, ok := r[alert]
		if !ok {
			t.Errorf("%s missing from an env with checks", alert)
			continue
		}
		if got.ID != want.id || got.Labels["severity"] != want.severity {
			t.Errorf("%s = id %q severity %q, want %q %q", alert, got.ID, got.Labels["severity"], want.id, want.severity)
		}
		if err := alerting.ValidateExpr(got.Expr); err != nil {
			t.Errorf("%s: the alert guard refuses its own expr: %v", alert, err)
		}
		if !strings.Contains(got.Expr, "'prod'") || !strings.Contains(got.Expr, "IN ('web')") {
			t.Errorf("%s is not scoped to the env and its checks:\n%s", alert, got.Expr)
		}
	}
}

func TestUptimeDefaultsAreTunableAsOneAndOptOutTakesBothLevels(t *testing.T) {
	r := rulesByAlert(t, uptimeInput("web"), "prod", map[string]AlertTuning{
		"uptime-check-failing": {Enabled: true, Params: map[string]float64{"failures": 5, "critical_failures": 20}},
	})
	if !strings.Contains(r["UptimeCheckFailing"].Expr, "failures >= 5 AND failures < 20") || !strings.Contains(r["UptimeCheckDown"].Expr, "failures >= 20") {
		t.Errorf("a tuned threshold did not reach both levels:\n%s\n%s", r["UptimeCheckFailing"].Expr, r["UptimeCheckDown"].Expr)
	}
	r = rulesByAlert(t, uptimeInput("web"), "prod", map[string]AlertTuning{"certificate-expiring": {Enabled: false}})
	if r["CertificateExpiring"].Alert != "" || r["CertificateExpiringSoon"].Alert != "" {
		t.Error("disabling certificate-expiring left a level behind")
	}
	if r["UptimeCheckFailing"].Alert == "" {
		t.Error("disabling one default disabled another")
	}
	if err := ValidateAlertTuning(map[string]AlertTuning{"certificate-expiring": {Params: map[string]float64{"nope": 1}}}); err == nil {
		t.Error("an unknown param is accepted")
	}
}

func TestUptimeAlertNamesAreReservedAndTheMetadataOptOutLeavesThemAlone(t *testing.T) {
	names := map[string]bool{}
	for _, n := range AlertDefaultAlertNames() {
		names[n] = true
	}
	for _, n := range []string{"UptimeCheckFailing", "UptimeCheckDown", "CertificateExpiring", "CertificateExpiringSoon"} {
		if !names[n] {
			t.Errorf("%s is not reserved: a user rule could shadow it", n)
		}
	}
	// The uptime data is a metric the prober writes: the node-agent metadata
	// opt-out does not remove it, the metrics opt-out does.
	_, skipped, err := ResolveAlertDefaultsFor(uptimeInput("web"), "prod", nil, []string{"metadata"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range skipped {
		if strings.Contains(s.ID, "uptime") || strings.Contains(s.ID, "certificate") {
			t.Errorf("opting out of metadata skipped %s", s.ID)
		}
	}
	_, skipped, _ = ResolveAlertDefaultsFor(uptimeInput("web"), "prod", nil, []string{"metrics"})
	got := map[string]bool{}
	for _, s := range skipped {
		got[s.ID] = true
	}
	if !got["uptime-check-failing"] || !got["certificate-expiring"] {
		t.Errorf("opting out of metrics did not skip the uptime defaults: %v", skipped)
	}
}

func TestGoldenUptimeAlertSQL(t *testing.T) {
	rules, err := ResolveAlertDefaults(uptimeInput("checkout", "web"), "prod", nil)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, r := range rules {
		if r.ID != "uptime-check-failing" && r.ID != "certificate-expiring" {
			continue
		}
		b.WriteString("-- " + r.Alert + " (" + r.ID + ") severity=" + r.Labels["severity"] + " for=" + r.For + " interval=" + r.Interval + "\n")
		b.WriteString(r.Expr + "\n\n")
	}
	golden(t, "alert-defaults-uptime.sql", []byte(b.String()))
}

// TestUptimeAlertsOnClickHouse runs the uptime defaults against a real
// ClickHouse holding the exporter's gauge table, with one series per case the
// SQL must get right, and reads the why payload back through pkg/alerting.
//
// Set FORGE_ALERT_CLICKHOUSE to a running container with clickhouse-client
// (password "pw") whose default database has otel_metrics_gauge.
func TestUptimeAlertsOnClickHouse(t *testing.T) {
	container := os.Getenv("FORGE_ALERT_CLICKHOUSE")
	if testing.Short() || container == "" {
		t.Skip("needs a ClickHouse container; set FORGE_ALERT_CLICKHOUSE=<container>")
	}
	run := func(sql string) string {
		t.Helper()
		cmd := exec.Command("docker", "exec", "-i", container, "clickhouse-client", "--password", "pw", "--multiquery", "--format", "JSONEachRow", "--output_format_json_quote_64bit_integers=0")
		cmd.Stdin = strings.NewReader(sql)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("clickhouse: %v\n%s\n--- sql ---\n%s", err, out, sql)
		}
		return strings.TrimSpace(string(out))
	}
	run(`TRUNCATE TABLE otel_metrics_gauge`)

	// noCert is a certDays that writes no certificate sample (an http check).
	const noCert = -1e9
	// put writes one probe of a check, `ago` minutes back: up/duration always,
	// the certificate unless certDays is noCert.
	put := func(env, check string, ago int, up int, status, errType string, durationS, certDays float64) {
		t.Helper()
		res := fmt.Sprintf("map('deployment.environment.name','%s')", env)
		attrs := fmt.Sprintf("map('check','%s','http.response.status_code','%s','error.type','%s')", check, status, errType)
		plain := fmt.Sprintf("map('check','%s')", check)
		stmt := fmt.Sprintf(`INSERT INTO otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value) VALUES
(%[1]s, 'uptime-prober', 'uptime.check.up', %[2]s, now() - INTERVAL %[3]d MINUTE, %[4]d),
(%[1]s, 'uptime-prober', 'uptime.check.duration', %[5]s, now() - INTERVAL %[3]d MINUTE, %[6]g)`, res, attrs, ago, up, plain, durationS)
		if certDays != noCert {
			stmt += fmt.Sprintf(",\n(%s, 'uptime-prober', 'tls.cert.expiry_seconds', %s, now() - INTERVAL %d MINUTE, %g)", res, plain, ago, certDays*86400)
		}
		run(stmt)
	}
	series := func(env, check string, upAgo []int, status, errType string, certDays float64) {
		for _, a := range upAgo {
			put(env, check, a, 0, status, errType, 1.5, certDays)
		}
	}
	healthy := func(env, check string, certDays float64, agos ...int) {
		for _, a := range agos {
			put(env, check, a, 1, "200", "", 0.12, certDays)
		}
	}

	// healthy: passing throughout, a far certificate.
	healthy("prod", "healthy", 80, 9, 8, 7, 6, 5, 4, 3, 2, 1)
	// blip: two failures in a row, below the warning threshold of 3.
	healthy("prod", "blip", 80, 9, 8, 7, 6, 5, 4, 3)
	series("prod", "blip", []int{2, 1}, "503", "status", 80)
	// failing: four in a row after passing probes -> warning (>= 3, < 10).
	healthy("prod", "failing", 80, 9, 8, 7)
	series("prod", "failing", []int{6, 5, 4, 3, 2, 1}, "502", "status", 80)
	// recovered: failed earlier, passing now -> nothing.
	series("prod", "recovered", []int{9, 8, 7, 6, 5}, "500", "status", 80)
	healthy("prod", "recovered", 80, 4, 3, 2, 1)
	// down: twelve failures in a row with no response at all -> critical.
	series("prod", "down", []int{14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3}, "", "timeout", noCert)
	// stale: failing, but the prober stopped writing 20 minutes ago -> no alert
	// from this rule (a silent prober is the heartbeat's alert).
	series("prod", "stale", []int{29, 28, 27, 26, 25, 24, 23, 22, 21, 20}, "500", "status", 80)
	// another env's failures never count for prod, and an undeclared check is ignored.
	series("dev", "healthy", []int{4, 3, 2, 1}, "500", "status", 80)
	series("prod", "undeclared", []int{4, 3, 2, 1}, "500", "status", 80)

	// certificates: 20d far, 10d warning, 3d critical, 0.5d critical, expired (-2d) critical.
	healthy("prod", "cert-20", 20, 3, 2, 1)
	healthy("prod", "cert-10", 10, 3, 2, 1)
	healthy("prod", "cert-3", 3, 3, 2, 1)
	healthy("prod", "cert-half", 0.5, 3, 2, 1)
	healthy("prod", "cert-expired", -2, 3, 2, 1)
	healthy("prod", "cert-http", noCert, 3, 2, 1) // an http check writes no certificate

	checks := []string{"healthy", "blip", "failing", "recovered", "down", "stale", "cert-20", "cert-10", "cert-3", "cert-half", "cert-expired", "cert-http"}
	rules, err := ResolveAlertDefaults(uptimeInput(checks...), "prod", nil)
	if err != nil {
		t.Fatal(err)
	}
	by := byAlert(rules)

	rows := func(alert string) map[string]map[string]any {
		t.Helper()
		out := map[string]map[string]any{}
		for _, line := range strings.Split(run(by[alert].Expr), "\n") {
			if line == "" {
				continue
			}
			var row map[string]any
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				t.Fatalf("%s: %v\n%s", alert, err, line)
			}
			out[row["check"].(string)] = row
		}
		return out
	}
	names := func(m map[string]map[string]any) string {
		var n []string
		for k := range m {
			n = append(n, k)
		}
		return strings.Join(sortedStrs(n), ",")
	}

	warn, down := rows("UptimeCheckFailing"), rows("UptimeCheckDown")
	if got := names(warn); got != "failing" {
		t.Errorf("UptimeCheckFailing fired for %q, want only the check that has failed 6 in a row (blip is 2, recovered passes, stale stopped reporting)", got)
	}
	if got := names(down); got != "down" {
		t.Errorf("UptimeCheckDown fired for %q, want only the check that has failed 12 in a row", got)
	}
	if v := warn["failing"]["value"]; v != float64(6) {
		t.Errorf("failing run length = %v, want 6", v)
	}
	if v := down["down"]["value"]; v != float64(12) {
		t.Errorf("down run length = %v, want 12", v)
	}

	// The why payload is a valid alerting.Why that says what failed and since when.
	w := whyOf(t, warn["failing"])
	if w.Detector != alerting.DetectorUptimeCheck || w.Signal != "consecutive_failures" || w.Unit != "probes" ||
		w.Observed == nil || *w.Observed != 6 || w.Since.IsZero() || w.Window != "30m" ||
		w.Thresholds["failures"] != 3 || w.Thresholds["critical_failures"] != 10 ||
		w.DedupeKey != "uptime_check/failing/-" || len(w.Next) != 2 {
		t.Errorf("failing why = %+v", w)
	}
	codes := map[string]alerting.WhyFinding{}
	for _, f := range w.Context.Findings {
		codes[f.Code] = f
	}
	if f := codes["last_status"]; f.Value != 502 || f.Ref != "failing" || !strings.Contains(f.Message, "the last probe failed: status") {
		t.Errorf("last_status finding = %+v", f)
	}
	if f := codes["latency_seconds"]; f.Value != 1.5 {
		t.Errorf("latency finding = %+v", f)
	}
	if f := codes["tls_expiry_days"]; f.Value != 80 {
		t.Errorf("tls_expiry finding = %+v", f)
	}
	wd := whyOf(t, down["down"])
	if f := firstFinding(wd, "last_status"); !strings.Contains(f.Message, "the last probe failed: timeout") {
		t.Errorf("a probe that got no response must say how it failed: %+v", f)
	}
	if firstFinding(wd, "tls_expiry_days").Code != "" {
		t.Error("a check that never saw a certificate reports a tls_expiry finding")
	}

	cwarn, ccrit := rows("CertificateExpiring"), rows("CertificateExpiringSoon")
	if got := names(cwarn); got != "cert-10" {
		t.Errorf("CertificateExpiring fired for %q, want only cert-10 (20d is far, 3d and below are critical)", got)
	}
	if got := names(ccrit); got != "cert-3,cert-expired,cert-half" {
		t.Errorf("CertificateExpiringSoon fired for %q, want cert-3, cert-half and the expired one", got)
	}
	if v := cwarn["cert-10"]["value"]; v != float64(10) {
		t.Errorf("cert-10 days = %v", v)
	}
	if v := ccrit["cert-expired"]["value"]; v != float64(-2) {
		t.Errorf("expired days = %v, want -2", v)
	}
	wc := whyOf(t, cwarn["cert-10"])
	if wc.Detector != alerting.DetectorCertificateExpiring || wc.Unit != "days" || wc.Observed == nil || *wc.Observed != 10 || wc.Thresholds["warn_days"] != 14 {
		t.Errorf("certificate why = %+v", wc)
	}

	// Through the evaluator's own result path: the rows become instances with the
	// why column lifted out of the labels.
	for _, alert := range []string{"UptimeCheckFailing", "UptimeCheckDown", "CertificateExpiring", "CertificateExpiringSoon"} {
		ev := alerting.Row{}
		for _, line := range strings.Split(run(by[alert].Expr), "\n") {
			if line == "" {
				continue
			}
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				t.Fatal(err)
			}
			if _, ok := ev["why"].(string); !ok {
				t.Errorf("%s: the why column is %T, want a JSON string the evaluator parses", alert, ev["why"])
			}
			break
		}
	}
	_ = context.Background
	_ = bytes.NewReader
}

func sortedStrs(in []string) []string {
	for i := range in {
		for j := i + 1; j < len(in); j++ {
			if in[j] < in[i] {
				in[i], in[j] = in[j], in[i]
			}
		}
	}
	return in
}

func whyOf(t *testing.T, row map[string]any) alerting.Why {
	t.Helper()
	s, ok := row["why"].(string)
	if !ok {
		t.Fatalf("row has no why string: %v", row)
	}
	w, err := alerting.ParseWhy(json.RawMessage(s))
	if err != nil {
		t.Fatalf("why does not parse: %v\n%s", err, s)
	}
	if _, err := w.JSON(); err != nil {
		t.Fatalf("why does not re-encode: %v", err)
	}
	return w
}

func firstFinding(w alerting.Why, code string) alerting.WhyFinding {
	for _, f := range w.Context.Findings {
		if f.Code == code {
			return f
		}
	}
	return alerting.WhyFinding{}
}

var _ = release.UptimeMetricUp
