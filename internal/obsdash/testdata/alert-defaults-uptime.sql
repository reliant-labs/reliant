-- CertificateExpiring (certificate-expiring) severity=warning for=10m interval=5m
WITH series AS (
    SELECT Attributes['check'] AS check,
           arraySort(x -> x.2, groupArray((MetricName, TimeUnix, Value, Attributes['http.response.status_code'], Attributes['error.type']))) AS pts
    FROM otel_metrics_gauge
    WHERE MetricName IN ('uptime.check.up', 'uptime.check.duration', 'tls.cert.expiry_seconds')
      AND TimeUnix >= now() - INTERVAL 30 MINUTE
      AND ResourceAttributes['deployment.environment.name'] = 'prod'
      AND Attributes['check'] IN ('checkout', 'web')
    GROUP BY check
), judged AS (
    SELECT check,
           arrayFilter(p -> p.1 = 'uptime.check.up', pts) AS ups,
           arrayFilter(p -> p.1 = 'uptime.check.duration', pts) AS durs,
           arrayFilter(p -> p.1 = 'tls.cert.expiry_seconds', pts) AS certs
    FROM series
), expiring AS (
    SELECT check, ups, durs, certs,
           length(ups) AS n,
           round(certs[length(certs)].3 / 86400, 1) AS days
    FROM judged
    WHERE length(certs) > 0
)
SELECT check,
       days AS value,
       toJSONString(CAST(tuple(
           'certificate_expiring', 'tls_expiry', concat('certificate_expiring/', check, '/-'),
           days, 'days',
           'days until the certificate the last probe saw expires',
           tuple(toFloat64(14), toFloat64(3)),
           '30m',
           tuple(arrayConcat(
                   if(n > 0 AND (ups[n].4 != '' OR ups[n].5 != ''), [tuple('last_status', check, toFloat64OrZero(ups[n].4), toFloat64(0), if(ups[n].5 != '', concat('the last probe failed: ', ups[n].5), concat('the last response was HTTP ', ups[n].4)))], []),
                   if(length(durs) > 0, [tuple('latency_seconds', check, durs[length(durs)].3, toFloat64(0), concat('the last probe took ', toString(round(durs[length(durs)].3, 3)), 's'))], []),
                   if(length(certs) > 0, [tuple('tls_expiry_days', check, round(certs[length(certs)].3 / 86400, 1), toFloat64(14), concat('the certificate expires in ', toString(round(certs[length(certs)].3 / 86400, 1)), ' days'))], []))),
           ['forge obs metrics query prod uptime.check.up --group-by check --since 3h', 'forge obs metrics query prod uptime.check.duration --group-by check --since 3h']
       ), 'Tuple(detector String, signal String, dedupe_key String, observed Float64, unit String, method String, thresholds Tuple(warn_days Float64, critical_days Float64), window String, context Tuple(findings Array(Tuple(code String, ref String, value Float64, `limit` Float64, message String))), next Array(String))')) AS why
FROM expiring
WHERE days <= 14 AND days > 3
ORDER BY check

-- CertificateExpiringSoon (certificate-expiring) severity=critical for=10m interval=5m
WITH series AS (
    SELECT Attributes['check'] AS check,
           arraySort(x -> x.2, groupArray((MetricName, TimeUnix, Value, Attributes['http.response.status_code'], Attributes['error.type']))) AS pts
    FROM otel_metrics_gauge
    WHERE MetricName IN ('uptime.check.up', 'uptime.check.duration', 'tls.cert.expiry_seconds')
      AND TimeUnix >= now() - INTERVAL 30 MINUTE
      AND ResourceAttributes['deployment.environment.name'] = 'prod'
      AND Attributes['check'] IN ('checkout', 'web')
    GROUP BY check
), judged AS (
    SELECT check,
           arrayFilter(p -> p.1 = 'uptime.check.up', pts) AS ups,
           arrayFilter(p -> p.1 = 'uptime.check.duration', pts) AS durs,
           arrayFilter(p -> p.1 = 'tls.cert.expiry_seconds', pts) AS certs
    FROM series
), expiring AS (
    SELECT check, ups, durs, certs,
           length(ups) AS n,
           round(certs[length(certs)].3 / 86400, 1) AS days
    FROM judged
    WHERE length(certs) > 0
)
SELECT check,
       days AS value,
       toJSONString(CAST(tuple(
           'certificate_expiring', 'tls_expiry', concat('certificate_expiring/', check, '/-'),
           days, 'days',
           'days until the certificate the last probe saw expires',
           tuple(toFloat64(14), toFloat64(3)),
           '30m',
           tuple(arrayConcat(
                   if(n > 0 AND (ups[n].4 != '' OR ups[n].5 != ''), [tuple('last_status', check, toFloat64OrZero(ups[n].4), toFloat64(0), if(ups[n].5 != '', concat('the last probe failed: ', ups[n].5), concat('the last response was HTTP ', ups[n].4)))], []),
                   if(length(durs) > 0, [tuple('latency_seconds', check, durs[length(durs)].3, toFloat64(0), concat('the last probe took ', toString(round(durs[length(durs)].3, 3)), 's'))], []),
                   if(length(certs) > 0, [tuple('tls_expiry_days', check, round(certs[length(certs)].3 / 86400, 1), toFloat64(14), concat('the certificate expires in ', toString(round(certs[length(certs)].3 / 86400, 1)), ' days'))], []))),
           ['forge obs metrics query prod uptime.check.up --group-by check --since 3h', 'forge obs metrics query prod uptime.check.duration --group-by check --since 3h']
       ), 'Tuple(detector String, signal String, dedupe_key String, observed Float64, unit String, method String, thresholds Tuple(warn_days Float64, critical_days Float64), window String, context Tuple(findings Array(Tuple(code String, ref String, value Float64, `limit` Float64, message String))), next Array(String))')) AS why
FROM expiring
WHERE days <= 3
ORDER BY check

-- UptimeCheckDown (uptime-check-failing) severity=critical for=0s interval=1m
WITH series AS (
    SELECT Attributes['check'] AS check,
           arraySort(x -> x.2, groupArray((MetricName, TimeUnix, Value, Attributes['http.response.status_code'], Attributes['error.type']))) AS pts
    FROM otel_metrics_gauge
    WHERE MetricName IN ('uptime.check.up', 'uptime.check.duration', 'tls.cert.expiry_seconds')
      AND TimeUnix >= now() - INTERVAL 30 MINUTE
      AND ResourceAttributes['deployment.environment.name'] = 'prod'
      AND Attributes['check'] IN ('checkout', 'web')
    GROUP BY check
), judged AS (
    SELECT check,
           arrayFilter(p -> p.1 = 'uptime.check.up', pts) AS ups,
           arrayFilter(p -> p.1 = 'uptime.check.duration', pts) AS durs,
           arrayFilter(p -> p.1 = 'tls.cert.expiry_seconds', pts) AS certs
    FROM series
), counted AS (
    SELECT check, ups, durs, certs,
           length(ups) AS n,
           arrayLastIndex(p -> p.3 > 0, ups) AS last_up,
           n - last_up AS failures
    FROM judged
)
SELECT check,
       failures AS value,
       toJSONString(CAST(tuple(
           'uptime_check', 'consecutive_failures', concat('uptime_check/', check, '/-'),
           toFloat64(failures), 'probes',
           'probes since the last passing one, read from the up gauge',
           tuple(toFloat64(3), toFloat64(10)),
           '30m',
           formatDateTime(ups[last_up + 1].2, '%Y-%m-%dT%H:%i:%SZ', 'UTC'),
           tuple(arrayConcat(
                   if(n > 0 AND (ups[n].4 != '' OR ups[n].5 != ''), [tuple('last_status', check, toFloat64OrZero(ups[n].4), toFloat64(0), if(ups[n].5 != '', concat('the last probe failed: ', ups[n].5), concat('the last response was HTTP ', ups[n].4)))], []),
                   if(length(durs) > 0, [tuple('latency_seconds', check, durs[length(durs)].3, toFloat64(0), concat('the last probe took ', toString(round(durs[length(durs)].3, 3)), 's'))], []),
                   if(length(certs) > 0, [tuple('tls_expiry_days', check, round(certs[length(certs)].3 / 86400, 1), toFloat64(0), concat('the certificate expires in ', toString(round(certs[length(certs)].3 / 86400, 1)), ' days'))], []))),
           ['forge obs metrics query prod uptime.check.up --group-by check --since 3h', 'forge obs metrics query prod uptime.check.duration --group-by check --since 3h']
       ), 'Tuple(detector String, signal String, dedupe_key String, observed Float64, unit String, method String, thresholds Tuple(failures Float64, critical_failures Float64), window String, since String, context Tuple(findings Array(Tuple(code String, ref String, value Float64, `limit` Float64, message String))), next Array(String))')) AS why
FROM counted
WHERE n > 0 AND failures >= 10
  AND ups[n].2 >= now() - INTERVAL 15 MINUTE
ORDER BY check

-- UptimeCheckFailing (uptime-check-failing) severity=warning for=0s interval=1m
WITH series AS (
    SELECT Attributes['check'] AS check,
           arraySort(x -> x.2, groupArray((MetricName, TimeUnix, Value, Attributes['http.response.status_code'], Attributes['error.type']))) AS pts
    FROM otel_metrics_gauge
    WHERE MetricName IN ('uptime.check.up', 'uptime.check.duration', 'tls.cert.expiry_seconds')
      AND TimeUnix >= now() - INTERVAL 30 MINUTE
      AND ResourceAttributes['deployment.environment.name'] = 'prod'
      AND Attributes['check'] IN ('checkout', 'web')
    GROUP BY check
), judged AS (
    SELECT check,
           arrayFilter(p -> p.1 = 'uptime.check.up', pts) AS ups,
           arrayFilter(p -> p.1 = 'uptime.check.duration', pts) AS durs,
           arrayFilter(p -> p.1 = 'tls.cert.expiry_seconds', pts) AS certs
    FROM series
), counted AS (
    SELECT check, ups, durs, certs,
           length(ups) AS n,
           arrayLastIndex(p -> p.3 > 0, ups) AS last_up,
           n - last_up AS failures
    FROM judged
)
SELECT check,
       failures AS value,
       toJSONString(CAST(tuple(
           'uptime_check', 'consecutive_failures', concat('uptime_check/', check, '/-'),
           toFloat64(failures), 'probes',
           'probes since the last passing one, read from the up gauge',
           tuple(toFloat64(3), toFloat64(10)),
           '30m',
           formatDateTime(ups[last_up + 1].2, '%Y-%m-%dT%H:%i:%SZ', 'UTC'),
           tuple(arrayConcat(
                   if(n > 0 AND (ups[n].4 != '' OR ups[n].5 != ''), [tuple('last_status', check, toFloat64OrZero(ups[n].4), toFloat64(0), if(ups[n].5 != '', concat('the last probe failed: ', ups[n].5), concat('the last response was HTTP ', ups[n].4)))], []),
                   if(length(durs) > 0, [tuple('latency_seconds', check, durs[length(durs)].3, toFloat64(0), concat('the last probe took ', toString(round(durs[length(durs)].3, 3)), 's'))], []),
                   if(length(certs) > 0, [tuple('tls_expiry_days', check, round(certs[length(certs)].3 / 86400, 1), toFloat64(0), concat('the certificate expires in ', toString(round(certs[length(certs)].3 / 86400, 1)), ' days'))], []))),
           ['forge obs metrics query prod uptime.check.up --group-by check --since 3h', 'forge obs metrics query prod uptime.check.duration --group-by check --since 3h']
       ), 'Tuple(detector String, signal String, dedupe_key String, observed Float64, unit String, method String, thresholds Tuple(failures Float64, critical_failures Float64), window String, since String, context Tuple(findings Array(Tuple(code String, ref String, value Float64, `limit` Float64, message String))), next Array(String))')) AS why
FROM counted
WHERE n > 0 AND failures >= 3 AND failures < 10
  AND ups[n].2 >= now() - INTERVAL 15 MINUTE
ORDER BY check

