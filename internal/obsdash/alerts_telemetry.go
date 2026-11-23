package obsdash

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/reliant-labs/forge/pkg/telemetry"
)

// ── declared telemetry ───────────────────────────────────────────────────────
//
// Every system a forge.Telemetry declares gets two defaults, one rule per
// system, named for it and labelled service = its service.name:
//
//	<Service>TargetDown    a scraped endpoint's `up` has been 0 for 5m
//	<Service>CheckFailing  a check has not succeeded for 5m
//
// Both read only what the collector itself writes for every target — the
// Prometheus receiver's `up` gauge, the tcpcheck / httpcheck receivers'
// status series — stamped service.name = the target's service_name by the
// collector (pkg/telemetry: every signal of a target carries it). They know
// nothing about the system. Like any default they are tuned in
// forge.Alerts.defaults by id, once for every system; a system opts out with
// Telemetry.default_alerts = False, and its own rules then take the names.

// The telemetry default ids.
const (
	TelemetryTargetDown   = "telemetry-target-down"
	TelemetryCheckFailing = "telemetry-check-failing"
)

// telemetryAlertPlaceholder stands for the system in a telemetry default's
// Alert ("<Service>TargetDown"); each rule replaces it (TelemetryAlertName).
const telemetryAlertPlaceholder = "<Service>"

// AlertTelemetryTarget is one declared system whose default alerts are armed.
type AlertTelemetryTarget struct {
	// Service is its service.name.
	Service string
	// Platform is scope "platform": its signals carry no env, so its rules
	// read every env's.
	Platform bool
	// Scrapes and Checks say it declares a metrics scrape and a check.
	Scrapes bool
	Checks  bool
}

func scrapedTarget(t AlertTelemetryTarget) bool { return t.Scrapes }
func checkedTarget(t AlertTelemetryTarget) bool { return t.Checks }

// AlertTelemetryTargetsOf are the targets of one env's render whose default
// alerts are armed (Telemetry.default_alerts), for AlertInput.TelemetryTargets.
func AlertTelemetryTargetsOf(targets []telemetry.Target) []AlertTelemetryTarget {
	var out []AlertTelemetryTarget
	for _, t := range targets {
		if !t.DefaultAlerts {
			continue
		}
		out = append(out, AlertTelemetryTarget{Service: t.ServiceName, Platform: t.Scope == telemetry.ScopePlatform,
			Scrapes: len(t.Metrics) > 0, Checks: len(t.Checks) > 0})
	}
	return out
}

// TelemetryAlertName is the name of the rule a telemetry default raises for
// service: the service.name in UpperCamelCase ("pg-bouncer" → "PgBouncer"),
// then suffix. A name an alert may not start with (a digit) is prefixed
// "Telemetry", so the result is always a valid rule name. The mapping folds
// punctuation (foo.bar and foo-bar both give FooBar), so the resolver refuses
// two systems whose names collide rather than emit one rule identity twice.
func TelemetryAlertName(service, suffix string) string {
	var b strings.Builder
	upper := true
	for _, r := range service {
		if !(r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r))) {
			upper = true
			continue
		}
		if upper {
			r = unicode.ToUpper(r)
			upper = false
		}
		b.WriteRune(r)
	}
	name := b.String()
	if name == "" || unicode.IsDigit(rune(name[0])) {
		name = "Telemetry" + name
	}
	return name + suffix
}

// telemetryEnvCond narrows a project-scope target's rows to env, like every
// other default; a platform target's rows carry no env.
func telemetryEnvCond(env string, t AlertTelemetryTarget) string {
	if t.Platform {
		return ""
	}
	return "\n  AND ResourceAttributes['deployment.environment.name'] = " + sqlQuote(env)
}

// telemetryTargetDownSQL is one row per scraped endpoint (service.instance.id,
// which the Prometheus receiver sets from the scrape's instance) whose newest
// `up` in the window is 0. With `for` 5m, an endpoint fires once its scrapes
// have failed for 5 minutes.
func telemetryTargetDownSQL(env string, t AlertTelemetryTarget, p map[string]float64) string {
	return fmt.Sprintf(`SELECT ServiceName AS service,
       ResourceAttributes['service.instance.id'] AS instance,
       argMax(Value, TimeUnix) AS value
FROM otel_metrics_gauge
WHERE MetricName = '%s'
  AND ServiceName = %s
  AND TimeUnix >= now() - INTERVAL %s MINUTE%s
GROUP BY service, instance
HAVING value = 0
ORDER BY instance`, metricUp, sqlQuote(t.Service), num(p[pWindow]), telemetryEnvCond(env, t))
}

// telemetryCheckFailingSQL is one row per checked endpoint that probed in the
// window and never succeeded: no tcpcheck.status of 1, no httpcheck.status of
// 1 in the 2xx class (httpcheck reports one point per status class and probe;
// the 2xx one is the probe's verdict). A request that got no response at all
// is reported only as httpcheck.error, so its points count as failed probes
// too. value is the number of failed points in the window.
func telemetryCheckFailingSQL(env string, t AlertTelemetryTarget, p map[string]float64) string {
	cond := fmt.Sprintf(`ServiceName = %s
      AND TimeUnix >= now() - INTERVAL %s MINUTE%s`, sqlQuote(t.Service), num(p[pWindow]), strings.ReplaceAll(telemetryEnvCond(env, t), "\n", "\n    "))
	return fmt.Sprintf(`WITH probes AS (
    SELECT Attributes['tcpcheck.endpoint'] AS check, Value = 1 AS ok
    FROM otel_metrics_gauge
    WHERE MetricName = '%[1]s'
      AND %[4]s
    UNION ALL
    SELECT Attributes['http.url'] AS check, Value = 1 AS ok
    FROM otel_metrics_sum
    WHERE MetricName = '%[2]s'
      AND Attributes['http.status_class'] = '2xx'
      AND %[4]s
    UNION ALL
    SELECT Attributes['http.url'] AS check, false AS ok
    FROM otel_metrics_sum
    WHERE MetricName = '%[3]s'
      AND %[4]s
)
SELECT %[5]s AS service, check, toFloat64(count()) AS value
FROM probes
WHERE check != ''
GROUP BY check
HAVING countIf(ok) = 0
ORDER BY check`, metricTCPCheckStatus, metricHTTPCheckStatus, metricHTTPCheckError, cond, sqlQuote(t.Service))
}
