package obsdash

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/alerting"
	"github.com/reliant-labs/forge/pkg/release"
)

// ── uptime checks ────────────────────────────────────────────────────────────
//
// The control plane's prober writes three gauges per check into the env's
// telemetry (release.UptimeMetric*), one sample per probe, tagged with the check's
// name and the env (deployment.environment.name, like every other rule's
// data). Both defaults read the env's own checks only (AlertInput.UptimeChecks),
// so a check the env opted out of can never alert, and both carry the
// alerting.Why payload: the last status, the latency, the certificate's expiry
// and when the failure began, as findings of the shared document.
//
// Each default is two rules (warning, then critical) whose bands do not
// overlap, so an escalation resolves the warning and raises the critical one,
// which is a new notification, as it should be.

// uptimeStaleMinutes is how old the newest probe may be before UptimeCheckFailing
// stops judging: a prober that died is the heartbeat's alert, not every check's.
// It is above the longest interval a check may declare (release.MaxUptimeInterval).
const uptimeStaleMinutes = 15

func uptimeCheckList(in AlertInput) string {
	q := make([]string, len(in.UptimeChecks))
	for i, c := range in.UptimeChecks {
		q[i] = sqlQuote(c)
	}
	return strings.Join(q, ", ")
}

// uptimeSeries is the scan both defaults share: every sample of the three
// gauges for the env's checks over the window, one row per check, the samples
// in time order, split per metric. A sample is (metric, time, value, http
// status, error type).
func uptimeSeries(env string, in AlertInput, window float64) string {
	return fmt.Sprintf(`WITH series AS (
    SELECT Attributes['%[1]s'] AS check,
           arraySort(x -> x.2, groupArray((MetricName, TimeUnix, Value, Attributes['%[2]s'], Attributes['%[3]s']))) AS pts
    FROM otel_metrics_gauge
    WHERE MetricName IN ('%[4]s', '%[5]s', '%[6]s')
      AND TimeUnix >= now() - INTERVAL %[7]s MINUTE
      AND ResourceAttributes['deployment.environment.name'] = %[8]s
      AND Attributes['%[1]s'] IN (%[9]s)
    GROUP BY check
), judged AS (
    SELECT check,
           arrayFilter(p -> p.1 = '%[4]s', pts) AS ups,
           arrayFilter(p -> p.1 = '%[5]s', pts) AS durs,
           arrayFilter(p -> p.1 = '%[6]s', pts) AS certs
    FROM series
)`, release.UptimeAttrCheck, release.UptimeAttrStatus, release.UptimeAttrError,
		release.UptimeMetricUp, release.UptimeMetricDuration, release.UptimeMetricCertExpiry,
		num(window), sqlQuote(env), uptimeCheckList(in))
}

// uptimeFindings is the SQL of the why payload's context.findings: the last
// probe's status (or how it failed), its latency and the certificate's days to
// expiry, each present only when the check reported it. It needs `ups`, `durs`
// and `certs` in scope, and `n`, the number of probes in `ups`. A finding is
// (code, ref, value, limit, message).
func uptimeFindings(warnDays float64) string {
	const tuple = `tuple(%s, check, %s, %s, %s)`
	status := fmt.Sprintf(tuple, `'last_status'`, `toFloat64OrZero(ups[n].4)`, `toFloat64(0)`,
		`if(ups[n].5 != '', concat('the last probe failed: ', ups[n].5), concat('the last response was HTTP ', ups[n].4))`)
	latency := fmt.Sprintf(tuple, `'latency_seconds'`, `durs[length(durs)].3`, `toFloat64(0)`,
		`concat('the last probe took ', toString(round(durs[length(durs)].3, 3)), 's')`)
	expiry := fmt.Sprintf(tuple, `'tls_expiry_days'`, `round(certs[length(certs)].3 / 86400, 1)`, fmt.Sprintf("toFloat64(%s)", num(warnDays)),
		`concat('the certificate expires in ', toString(round(certs[length(certs)].3 / 86400, 1)), ' days')`)
	return fmt.Sprintf(`arrayConcat(
                   if(n > 0 AND (ups[n].4 != '' OR ups[n].5 != ''), [%s], []),
                   if(length(durs) > 0, [%s], []),
                   if(length(certs) > 0, [%s], []))`, status, latency, expiry)
}

const uptimeFindingType = "Array(Tuple(code String, ref String, value Float64, `limit` Float64, message String))"

func uptimeNext(env string) string {
	return fmt.Sprintf("['forge obs metrics query %[1]s %[2]s --group-by %[4]s --since 3h', 'forge obs metrics query %[1]s %[3]s --group-by %[4]s --since 3h']",
		env, release.UptimeMetricUp, release.UptimeMetricDuration, release.UptimeAttrCheck)
}

// uptimeFailingSQL fires per check on the run of failed probes at the end of
// its series: N in a row (warning, UptimeCheckFailing) up to M (critical,
// UptimeCheckDown, from M in a row). `value` is the length of the run.
func uptimeFailingSQL(env string, in AlertInput, p map[string]float64) string {
	band := fmt.Sprintf("failures >= %s AND failures < %s", num(p[pFailures]), num(p[pCriticalFailures]))
	if p[pCritical] == 1 {
		band = fmt.Sprintf("failures >= %s", num(p[pCriticalFailures]))
	}
	whyType := fmt.Sprintf("Tuple(detector String, signal String, dedupe_key String, observed Float64, unit String, method String, thresholds Tuple(%s Float64, %s Float64), window String, since String, context Tuple(findings %s), next Array(String))",
		pFailures, pCriticalFailures, uptimeFindingType)
	return fmt.Sprintf(`%s, counted AS (
    SELECT check, ups, durs, certs,
           length(ups) AS n,
           arrayLastIndex(p -> p.3 > 0, ups) AS last_up,
           n - last_up AS failures
    FROM judged
)
SELECT check,
       failures AS value,
       toJSONString(CAST(tuple(
           '%s', 'consecutive_failures', concat('%s/', check, '/-'),
           toFloat64(failures), 'probes',
           'probes since the last passing one, read from the up gauge',
           tuple(toFloat64(%s), toFloat64(%s)),
           '%sm',
           formatDateTime(ups[last_up + 1].2, '%%Y-%%m-%%dT%%H:%%i:%%SZ', 'UTC'),
           tuple(%s),
           %s
       ), '%s')) AS why
FROM counted
WHERE n > 0 AND %s
  AND ups[n].2 >= now() - INTERVAL %d MINUTE
ORDER BY check`,
		uptimeSeries(env, in, p[pWindow]),
		alerting.DetectorUptimeCheck, alerting.DetectorUptimeCheck,
		num(p[pFailures]), num(p[pCriticalFailures]), num(p[pWindow]),
		uptimeFindings(0), uptimeNext(env), whyType, band, uptimeStaleMinutes)
}

// certificateExpiringSQL fires per check whose certificate has CriticalDays or
// fewer left (critical, CertificateExpiringSoon, an expired one included), or
// WarnDays or fewer (warning, CertificateExpiring). `value` is the days left.
func certificateExpiringSQL(env string, in AlertInput, p map[string]float64) string {
	band := fmt.Sprintf("days <= %s AND days > %s", num(p[pWarnDays]), num(p[pCriticalDays]))
	if p[pCritical] == 1 {
		band = fmt.Sprintf("days <= %s", num(p[pCriticalDays]))
	}
	whyType := fmt.Sprintf("Tuple(detector String, signal String, dedupe_key String, observed Float64, unit String, method String, thresholds Tuple(%s Float64, %s Float64), window String, context Tuple(findings %s), next Array(String))",
		pWarnDays, pCriticalDays, uptimeFindingType)
	return fmt.Sprintf(`%s, expiring AS (
    SELECT check, ups, durs, certs,
           length(ups) AS n,
           round(certs[length(certs)].3 / 86400, 1) AS days
    FROM judged
    WHERE length(certs) > 0
)
SELECT check,
       days AS value,
       toJSONString(CAST(tuple(
           '%s', 'tls_expiry', concat('%s/', check, '/-'),
           days, 'days',
           'days until the certificate the last probe saw expires',
           tuple(toFloat64(%s), toFloat64(%s)),
           '%sm',
           tuple(%s),
           %s
       ), '%s')) AS why
FROM expiring
WHERE %s
ORDER BY check`,
		uptimeSeries(env, in, p[pWindow]),
		alerting.DetectorCertificateExpiring, alerting.DetectorCertificateExpiring,
		num(p[pWarnDays]), num(p[pCriticalDays]), num(p[pWindow]),
		uptimeFindings(p[pWarnDays]), uptimeNext(env), whyType, band)
}
