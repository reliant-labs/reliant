package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func checkAlerts() []release.ShapeAlert {
	return []release.ShapeAlert{
		{Alert: "ServiceErrorRateHigh", Expr: "SELECT service, value FROM e", For: "5m", Interval: "1m", Labels: map[string]string{"severity": "critical"}, Source: release.AlertSourceDefault, DefaultID: "error-rate"},
		{Alert: "ServiceSilent", Expr: "SELECT service, value FROM s", For: "0s", Labels: map[string]string{"severity": "warning"}, Source: release.AlertSourceDefault},
		{Alert: "Broken", Expr: "SELECT x FROM nope", Labels: map[string]string{"severity": "info"}, Source: release.AlertSourceUser},
		{Alert: "Refused", Expr: "DROP TABLE otel_logs", Labels: map[string]string{"severity": "info"}, Source: release.AlertSourceUser},
	}
}

func fakeLoader(a []release.ShapeAlert) alertShapeLoader {
	return func(context.Context, io.Writer, string) ([]release.ShapeAlert, error) { return a, nil }
}

func fakeRunner(t *testing.T, seen *[]string) alertQueryRunner {
	return func(_ context.Context, sql string, settings map[string]any) ([]byte, error) {
		*seen = append(*seen, sql)
		if settings["readonly"] != 2 || settings["max_result_rows"] == nil || settings["max_execution_time"] == nil {
			t.Errorf("a rule ran without the evaluator's guard settings: %v", settings)
		}
		switch {
		case strings.Contains(sql, "FROM e"):
			return []byte(`{"service":"api","value":0.2}` + "\n" + `{"service":"delta","value":0.1}` + "\n"), nil
		case strings.Contains(sql, "FROM s"):
			return nil, nil
		}
		return nil, errors.New("Unknown table nope")
	}
}

func TestObserveAlertsCheck_PrintsWhatFiresWithLabelsAndValues(t *testing.T) {
	var seen []string
	var out bytes.Buffer
	err := runObsAlertsCheck(context.Background(), &out, io.Discard, obsAlertsCheckOpts{env: "dev", maxRows: 200}, fakeLoader(checkAlerts()), fakeRunner(t, &seen))
	if err == nil {
		t.Error("a rule that errored must make the command fail")
	}
	got := out.String()
	for _, want := range []string{
		"ServiceErrorRateHigh", "FIRING x2", `{service="api"}  value=0.2`, `{service="delta"}  value=0.1`,
		"ServiceSilent", "ok",
		"Broken", "ERROR", "Unknown table nope",
		"Refused", "must start with SELECT",
		"4 rule(s), 2 instance(s) returning now, 2 rule(s) failed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	for _, s := range seen {
		if strings.Contains(s, "DROP TABLE") {
			t.Error("a refused expr reached ClickHouse")
		}
	}
}

// `for` is not applied: a rule returning rows now is listed even if it must hold 5m before it pages.
func TestObserveAlertsCheck_DoesNotApplyForAndSendsNothing(t *testing.T) {
	var seen []string
	var out bytes.Buffer
	_ = runObsAlertsCheck(context.Background(), &out, io.Discard, obsAlertsCheckOpts{env: "dev", rule: "ServiceErrorRateHigh", maxRows: 200}, fakeLoader(checkAlerts()), fakeRunner(t, &seen))
	if !strings.Contains(out.String(), "FIRING x2") || !strings.Contains(out.String(), "for 5m") {
		t.Errorf("%s", out.String())
	}
	if len(seen) != 1 {
		t.Errorf("--rule ran %d queries, want 1", len(seen))
	}
}

func TestObserveAlertsCheck_JSONAndSQL(t *testing.T) {
	var seen []string
	var out bytes.Buffer
	_ = runObsAlertsCheck(context.Background(), &out, io.Discard, obsAlertsCheckOpts{env: "dev", rule: "ServiceErrorRateHigh", asJSON: true, maxRows: 200}, fakeLoader(checkAlerts()), fakeRunner(t, &seen))
	for _, want := range []string{`"alert": "ServiceErrorRateHigh"`, `"service": "api"`, `"value": 0.2`, `"severity": "critical"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("json lacks %s:\n%s", want, out.String())
		}
	}
	out.Reset()
	seen = nil
	if err := runObsAlertsCheck(context.Background(), &out, io.Discard, obsAlertsCheckOpts{env: "dev", printSQL: true}, fakeLoader(checkAlerts()), fakeRunner(t, &seen)); err != nil || len(seen) != 0 {
		t.Errorf("--sql must not run anything: %v %v", err, seen)
	}
	if !strings.Contains(out.String(), "SELECT service, value FROM e") {
		t.Errorf("--sql did not print the expr:\n%s", out.String())
	}
}

func TestObserveAlertsCheck_NoRulesAndUnknownRule(t *testing.T) {
	var out bytes.Buffer
	if err := runObsAlertsCheck(context.Background(), &out, io.Discard, obsAlertsCheckOpts{env: "dev"}, fakeLoader(nil), nil); err != nil || !strings.Contains(out.String(), "declares no alert rules") {
		t.Errorf("no rules: %v %q", err, out.String())
	}
	if err := runObsAlertsCheck(context.Background(), &out, io.Discard, obsAlertsCheckOpts{env: "dev", rule: "Nope"}, fakeLoader(checkAlerts()), nil); err == nil || !strings.Contains(err.Error(), "ServiceSilent") {
		t.Errorf("an unknown --rule should name the rules: %v", err)
	}
}

// A shape alert survives the JSON round trip into pkg/alerting's RuleSpec with its timing intact.
func TestRuleSpecOf_KeepsTheShapeFields(t *testing.T) {
	spec, err := ruleSpecOf(release.ShapeAlert{Alert: "A", Expr: "SELECT 1 AS value", For: "90s", Interval: "5m", Labels: map[string]string{"severity": "info"}, Source: release.AlertSourceDefault, DefaultID: "x", KeepFiringFor: "1m"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.For.Std().Seconds() != 90 || spec.Interval.Std().Minutes() != 5 || string(spec.Source) != "default" || spec.Validate() != nil {
		t.Errorf("spec = %+v (%v)", spec, spec.Validate())
	}
}
