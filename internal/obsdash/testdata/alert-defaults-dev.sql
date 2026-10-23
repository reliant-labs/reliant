-- DeployFailed (deploy-failed) severity=critical for=5m interval=1m
SELECT 1 AS value WHERE 0 /* reliant:platform deploy-failed env:{reliant_env_id:String} */

-- FrontendJSErrorSpike (frontend-js-errors) severity=warning for=10m interval=1m
SELECT ServiceName AS service,
       countIf(Timestamp >= now() - INTERVAL 10 MINUTE) AS value
FROM otel_traces
WHERE ServiceName = 'shop-frontend'
  AND Timestamp >= now() - INTERVAL 10 MINUTE - INTERVAL 60 MINUTE
  AND ResourceAttributes['deployment.environment.name'] = 'dev'
  AND ResourceAttributes['rum.sessionId'] != ''
  AND lower(StatusCode) = 'error'
  AND SpanAttributes['component'] NOT IN ('fetch', 'xml-http-request')
  AND (SpanName != 'console.error' OR has(Events.Name, 'exception'))
GROUP BY ServiceName
HAVING value >= 10
   AND value > 3 * countIf(Timestamp < now() - INTERVAL 10 MINUTE) * 10 / 60
ORDER BY service

-- FrontendLCPPoor (frontend-lcp-poor) severity=info for=10m interval=5m
SELECT ServiceName AS service,
       quantile(0.75)(toFloat64OrZero(SpanAttributes['lcp'])) AS value,
       count() AS samples
FROM otel_traces
WHERE ServiceName = 'shop-frontend'
  AND Timestamp >= now() - INTERVAL 30 MINUTE
  AND ResourceAttributes['deployment.environment.name'] = 'dev'
  AND ResourceAttributes['rum.sessionId'] != ''
  AND SpanName = 'webvitals'
  AND SpanAttributes['lcp'] != ''
GROUP BY ServiceName
HAVING samples >= 100 AND value > 4000
ORDER BY service

-- IssueRegressed (issue-regressed) severity=warning for=0s interval=1m
SELECT 1 AS value WHERE 0 /* reliant:platform issue-regressed env:{reliant_env_id:String} */

-- IssueSpike (issue-spike) severity=warning for=0s interval=5m
SELECT 1 AS value WHERE 0 /* reliant:platform issue-spike env:{reliant_env_id:String} */

-- NewIssue (new-issue) severity=warning for=0s interval=1m
SELECT 1 AS value WHERE 0 /* reliant:platform new-issue env:{reliant_env_id:String} */

-- ObservabilityQuotaOver100 (observability-quota) severity=warning for=0s interval=1m
SELECT 1 AS value WHERE 0 /* reliant:platform observability-quota:over_100 env:{reliant_env_id:String} */

-- ObservabilityQuotaOver150 (observability-quota) severity=critical for=0s interval=1m
SELECT 1 AS value WHERE 0 /* reliant:platform observability-quota:over_150 env:{reliant_env_id:String} */

-- ServiceErrorRateHigh (error-rate) severity=critical for=5m interval=1m
WITH series AS (
    SELECT ServiceName AS service,
           MetricName AS metric,
           Attributes['error_class'] AS error_class,
           arrayMap(x -> x.2, arraySort(x -> x.1, groupArray((TimeUnix, Value)))) AS vals,
           any(AggregationTemporality) AS temporality
    FROM otel_metrics_sum
    WHERE MetricName IN ('rpc.server.calls', 'rpc.server.errors')
      AND ((AggregationTemporality = 1 AND TimeUnix >= now() - INTERVAL 5 MINUTE) OR (AggregationTemporality != 1 AND TimeUnix >= now() - INTERVAL 5 MINUTE - INTERVAL 2 MINUTE))
      AND ResourceAttributes['deployment.environment.name'] = 'dev'
      AND splitByChar('/', Attributes['procedure'])[2] IN ('billing.v1.BillingService', 'orders.v1.OrdersService')
    GROUP BY ServiceName, ResourceAttributes, Attributes, MetricName
), increase AS (
    SELECT service, metric, error_class,
           if(temporality = 1, arraySum(vals), arraySum((v, d) -> if(d < 0, v, d), vals, arrayDifference(vals))) AS n
    FROM series
)
SELECT service,
       sumIf(n, metric = 'rpc.server.errors' AND error_class NOT IN ('user', 'canceled')) / sumIf(n, metric = 'rpc.server.calls') AS value
FROM increase
GROUP BY service
HAVING sumIf(n, metric = 'rpc.server.calls') >= 20 AND value > 0.05
ORDER BY service

-- ServiceLatencyP95High (p95-latency) severity=warning for=10m interval=1m
WITH series AS (
    SELECT ServiceName AS service,
           ExplicitBounds AS bounds,
           arrayMap(x -> x.2, arraySort(x -> x.1, groupArray((TimeUnix, arrayMap(c -> toFloat64(c), BucketCounts))))) AS snaps,
           any(AggregationTemporality) AS temporality
    FROM otel_metrics_histogram
    WHERE MetricName = 'rpc.server.duration'
      AND ((AggregationTemporality = 1 AND TimeUnix >= now() - INTERVAL 5 MINUTE) OR (AggregationTemporality != 1 AND TimeUnix >= now() - INTERVAL 5 MINUTE - INTERVAL 2 MINUTE))
      AND ResourceAttributes['deployment.environment.name'] = 'dev'
      AND splitByChar('/', Attributes['procedure'])[2] IN ('billing.v1.BillingService', 'orders.v1.OrdersService')
    GROUP BY ServiceName, ResourceAttributes, Attributes, ExplicitBounds
), increase AS (
    SELECT service, bounds,
           if(temporality = 1,
              arrayReduce('sumForEach', snaps),
              arrayReduce('sumForEach', arrayMap((cur, prev) -> arrayMap((c, q) -> if(c < q, c, c - q), cur, prev),
                                                 arraySlice(snaps, 2), arraySlice(snaps, 1, length(snaps) - 1)))) AS inc
    FROM series
    WHERE length(snaps) >= 2 OR temporality = 1
), merged AS (
    SELECT service, bounds, sumForEach(inc) AS counts FROM increase GROUP BY service, bounds
)
SELECT service,
       quantilePrometheusHistogram(0.95)(le, cum) AS value,
       max(cum) AS requests
FROM (
    SELECT service,
           arrayJoin(arrayZip(arrayPushBack(bounds, inf), arrayCumSum(counts))) AS pair,
           pair.1 AS le, pair.2 AS cum
    FROM merged
)
GROUP BY service
HAVING requests >= 20 AND value > 1
ORDER BY service

-- ServiceSilent (service-silent) severity=warning for=0s interval=1m
WITH moves AS (
    SELECT service, arrayJoin(t) AS t
    FROM (
        SELECT ServiceName AS service,
               arraySort(x -> x.1, groupArray((TimeUnix, Value))) AS pts,
               arrayMap(x -> x.1, pts) AS ts,
               arrayMap(x -> x.2, pts) AS vs,
               if(any(AggregationTemporality) = 1, vs, arrayDifference(vs)) AS moved,
               arrayFilter((tm, d) -> d != 0, ts, moved) AS t
        FROM otel_metrics_sum
        WHERE MetricName = 'rpc.server.calls'
          AND TimeUnix >= now() - INTERVAL 24 HOUR - INTERVAL 10 MINUTE - INTERVAL 1 MINUTE
          AND ResourceAttributes['deployment.environment.name'] = 'dev'
          AND splitByChar('/', Attributes['procedure'])[2] IN ('billing.v1.BillingService', 'orders.v1.OrdersService')
        GROUP BY ServiceName, ResourceAttributes, Attributes
    )
)
SELECT service, dateDiff('second', max(t), now()) AS value
FROM moves
GROUP BY service
HAVING max(t) < now() - INTERVAL 10 MINUTE
   AND uniqExactIf(toStartOfHour(t), t < now() - INTERVAL 10 MINUTE) >= 18
ORDER BY service

-- WorkloadCrashLooping (crash-loop) severity=critical for=0s interval=1m
WITH grown_restarts AS (
    SELECT ResourceAttributes['k8s.workload.name'] AS workload,
           max(Value) - min(Value) AS grown
    FROM otel_metrics_gauge
    WHERE MetricName = 'k8s.container.restarts'
      AND TimeUnix >= now() - INTERVAL 10 MINUTE - INTERVAL 6 MINUTE
      AND ResourceAttributes['reliant.env.id'] = {reliant_env_id:String}
      AND ResourceAttributes['k8s.workload.name'] != ''
    GROUP BY workload, ResourceAttributes['k8s.container.name'], ResourceAttributes['k8s.pod.uid'], ResourceAttributes['k8s.pod.name']
), backoff_events AS (
    SELECT ResourceAttributes['k8s.workload.name'] AS workload, count() AS n
    FROM otel_logs
    WHERE Timestamp >= now() - INTERVAL 10 MINUTE
      AND ResourceAttributes['reliant.env.id'] = {reliant_env_id:String}
      AND ResourceAttributes['k8s.workload.name'] != ''
      AND (LogAttributes['k8s.event.reason'] = 'CrashLoopBackOff' OR (LogAttributes['k8s.event.reason'] = 'BackOff' AND positionCaseInsensitive(Body, 'restarting') > 0))
    GROUP BY workload
)
SELECT workload, greatest(total_restarts, toFloat64(total_backoffs)) AS value
FROM (
    SELECT workload, sum(grown) AS total_restarts, sum(ev) AS total_backoffs
    FROM (
        SELECT workload, grown, 0 AS ev FROM grown_restarts
        UNION ALL
        SELECT workload, 0 AS grown, n AS ev FROM backoff_events
    )
    GROUP BY workload
)
WHERE total_restarts >= 3 OR total_backoffs >= 1
ORDER BY workload

-- WorkloadEvicted (evicted) severity=warning for=0s interval=1m
SELECT workload, n AS value
FROM (
    SELECT ResourceAttributes['k8s.workload.name'] AS workload, count() AS n
    FROM otel_logs
    WHERE Timestamp >= now() - INTERVAL 15 MINUTE
      AND ResourceAttributes['reliant.env.id'] = {reliant_env_id:String}
      AND ResourceAttributes['k8s.workload.name'] != ''
      AND (LogAttributes['k8s.event.reason'] = 'Evicted')
    GROUP BY workload
)
WHERE n >= 1
ORDER BY workload

-- WorkloadImagePullFailed (image-pull-failed) severity=warning for=5m interval=1m
SELECT workload, n AS value
FROM (
    SELECT ResourceAttributes['k8s.workload.name'] AS workload, count() AS n
    FROM otel_logs
    WHERE Timestamp >= now() - INTERVAL 15 MINUTE
      AND ResourceAttributes['reliant.env.id'] = {reliant_env_id:String}
      AND ResourceAttributes['k8s.workload.name'] != ''
      AND (LogAttributes['k8s.event.reason'] IN ('ErrImagePull', 'ImagePullBackOff', 'ErrImageNeverPull', 'InspectFailed') OR (LogAttributes['k8s.event.reason'] IN ('Failed', 'BackOff') AND positionCaseInsensitive(Body, 'pull') > 0 AND positionCaseInsensitive(Body, 'image') > 0))
    GROUP BY workload
)
WHERE n >= 1
ORDER BY workload

-- WorkloadMemoryNearLimit (memory-near-limit) severity=warning for=10m interval=1m
SELECT workload, container, max(used) AS value
FROM (
    SELECT ResourceAttributes['k8s.workload.name'] AS workload,
           ResourceAttributes['k8s.container.name'] AS container,
           avg(Value) AS used
    FROM otel_metrics_gauge
    WHERE MetricName = 'k8s.container.memory_limit_utilization'
      AND TimeUnix >= now() - INTERVAL 5 MINUTE
      AND ResourceAttributes['reliant.env.id'] = {reliant_env_id:String}
      AND ResourceAttributes['k8s.workload.name'] != ''
    GROUP BY workload, container, ResourceAttributes['k8s.pod.uid'], ResourceAttributes['k8s.pod.name']
)
GROUP BY workload, container
HAVING value > 0.9
ORDER BY workload, container

-- WorkloadOOMKilled (oom-killed) severity=critical for=0s interval=1m
WITH grown_restarts AS (
    SELECT ResourceAttributes['k8s.workload.name'] AS workload,
           max(Value) - min(Value) AS grown,
           argMax(if(mapContains(Attributes, 'k8s.container.last_terminated_reason'), Attributes['k8s.container.last_terminated_reason'], ResourceAttributes['k8s.container.last_terminated_reason']), TimeUnix) AS reason
    FROM otel_metrics_gauge
    WHERE MetricName = 'k8s.container.restarts'
      AND TimeUnix >= now() - INTERVAL 10 MINUTE - INTERVAL 6 MINUTE
      AND ResourceAttributes['reliant.env.id'] = {reliant_env_id:String}
      AND ResourceAttributes['k8s.workload.name'] != ''
    GROUP BY workload, ResourceAttributes['k8s.container.name'], ResourceAttributes['k8s.pod.uid'], ResourceAttributes['k8s.pod.name']
), oom_events AS (
    SELECT ResourceAttributes['k8s.workload.name'] AS workload, count() AS n
    FROM otel_logs
    WHERE Timestamp >= now() - INTERVAL 10 MINUTE
      AND ResourceAttributes['reliant.env.id'] = {reliant_env_id:String}
      AND ResourceAttributes['k8s.workload.name'] != ''
      AND (LogAttributes['k8s.event.reason'] = 'OOMKilling' OR LogAttributes['k8s.container.last_terminated_reason'] = 'OOMKilled')
    GROUP BY workload
)
SELECT workload, max(oom) AS value
FROM (
    SELECT workload, grown AS oom FROM grown_restarts WHERE grown > 0 AND reason = 'OOMKilled'
    UNION ALL
    SELECT workload, toFloat64(n) AS oom FROM oom_events
)
GROUP BY workload
ORDER BY workload

