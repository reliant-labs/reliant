DROP TABLE IF EXISTS otel_logs;
DROP TABLE IF EXISTS otel_metrics_gauge;
CREATE TABLE otel_logs (Timestamp DateTime64(9), ResourceAttributes Map(String,String), LogAttributes Map(String,String), Body String, ServiceName String DEFAULT '', SeverityText String DEFAULT '') ENGINE = MergeTree ORDER BY Timestamp;
-- db-migrate-up run "db-migrate-up-0123456789": SAME name in prod and staging. prod succeeds in 30s; staging fails.
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 20 MINUTE, map('reliant.env.id','prod','k8s.workload.name','db-migrate-up-0123456789','k8s.object.kind','Job'), map('k8s.event.reason','SuccessfulCreate'), '';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 20 MINUTE + INTERVAL 30 SECOND, map('reliant.env.id','prod','k8s.workload.name','db-migrate-up-0123456789','k8s.object.kind','Job'), map('k8s.event.reason','Completed'), '';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 19 MINUTE, map('reliant.env.id','stg','k8s.workload.name','db-migrate-up-0123456789','k8s.object.kind','Job'), map('k8s.event.reason','SuccessfulCreate'), '';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 15 MINUTE, map('reliant.env.id','stg','k8s.workload.name','db-migrate-up-0123456789','k8s.object.kind','Job'), map('k8s.event.reason','BackoffLimitExceeded'), '';
-- a cron's second run, and a pod event about the same Job name that must NOT count
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 10 MINUTE, map('reliant.env.id','prod','k8s.workload.name','cloudsql-backup-prune-29012345','k8s.object.kind','Job'), map('k8s.event.reason','SuccessfulCreate'), '';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 10 MINUTE + INTERVAL 90 SECOND, map('reliant.env.id','prod','k8s.workload.name','cloudsql-backup-prune-29012345','k8s.object.kind','Job'), map('k8s.event.reason','Completed'), '';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 10 MINUTE, map('reliant.env.id','prod','k8s.workload.name','cloudsql-backup-prune-29012345','k8s.object.kind','Pod'), map('k8s.event.reason','SuccessfulCreate'), '';
-- something that is not one of our jobs
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 10 MINUTE, map('reliant.env.id','prod','k8s.workload.name','other-thing-0123456789','k8s.object.kind','Job'), map('k8s.event.reason','SuccessfulCreate'), '';
-- a job whose NEWEST run failed after an older successful one, and one whose newest run is still running:
-- last_duration_s must be the newest run's (NULL), never the older run's 40s.
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 3 HOUR, map('reliant.env.id','prod','k8s.workload.name','dbrole-provision-reliant-aaaaaaaaaa','k8s.object.kind','Job'), map('k8s.event.reason','SuccessfulCreate'), '';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 3 HOUR + INTERVAL 40 SECOND, map('reliant.env.id','prod','k8s.workload.name','dbrole-provision-reliant-aaaaaaaaaa','k8s.object.kind','Job'), map('k8s.event.reason','Completed'), '';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 1 HOUR, map('reliant.env.id','prod','k8s.workload.name','dbrole-provision-reliant-bbbbbbbbbb','k8s.object.kind','Job'), map('k8s.event.reason','SuccessfulCreate'), '';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 1 HOUR + INTERVAL 5 MINUTE, map('reliant.env.id','prod','k8s.workload.name','dbrole-provision-reliant-bbbbbbbbbb','k8s.object.kind','Job'), map('k8s.event.reason','DeadlineExceeded'), '';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 4 HOUR, map('reliant.env.id','prod','k8s.workload.name','nightly-cccccccccc','k8s.object.kind','Job'), map('k8s.event.reason','SuccessfulCreate'), '';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 4 HOUR + INTERVAL 70 SECOND, map('reliant.env.id','prod','k8s.workload.name','nightly-cccccccccc','k8s.object.kind','Job'), map('k8s.event.reason','Completed'), '';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 2 MINUTE, map('reliant.env.id','prod','k8s.workload.name','nightly-dddddddddd','k8s.object.kind','Job'), map('k8s.event.reason','SuccessfulCreate'), '';
-- ONE run, several SuccessfulCreate events: the Job controller emits one per POD, so a parallel Job (3 pods)
-- and a pod replaced after a failure both repeat it. "indexer" is ONE execution: 1 run, never 4.
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 30 MINUTE, map('reliant.env.id','prod','k8s.workload.name','indexer-eeeeeeeeee','k8s.object.kind','Job'), map('k8s.event.reason','SuccessfulCreate'), 'pod 1';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 30 MINUTE + INTERVAL 1 SECOND, map('reliant.env.id','prod','k8s.workload.name','indexer-eeeeeeeeee','k8s.object.kind','Job'), map('k8s.event.reason','SuccessfulCreate'), 'pod 2';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 30 MINUTE + INTERVAL 2 SECOND, map('reliant.env.id','prod','k8s.workload.name','indexer-eeeeeeeeee','k8s.object.kind','Job'), map('k8s.event.reason','SuccessfulCreate'), 'pod 3';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 30 MINUTE + INTERVAL 20 SECOND, map('reliant.env.id','prod','k8s.workload.name','indexer-eeeeeeeeee','k8s.object.kind','Job'), map('k8s.event.reason','SuccessfulCreate'), 'replacement pod';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 30 MINUTE + INTERVAL 80 SECOND, map('reliant.env.id','prod','k8s.workload.name','indexer-eeeeeeeeee','k8s.object.kind','Job'), map('k8s.event.reason','Completed'), '';
-- the same pods' own events carry the bare forge name (the node agent reads the pod's app.kubernetes.io/name label first)
-- and must not count as runs either
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 30 MINUTE, map('reliant.env.id','prod','k8s.workload.name','indexer','k8s.object.kind','Pod','k8s.pod.name','indexer-eeeeeeeeee-x7k2p'), map('k8s.event.reason','SuccessfulCreate'), '';
-- A standalone Job is named from its pod-spec hash alone, so deleting and recreating an unchanged Job reuses the name.
-- "reindex" ran twice under ONE name in prod, with two distinct Job UIDs (the event's involved-object UID):
-- the first run failed after 2 minutes, the second succeeded after 50 seconds. Two executions, never one run that
-- both failed and succeeded with a duration spanning both.
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 50 MINUTE, map('reliant.env.id','prod','k8s.workload.name','reindex-ffffffffff','k8s.object.kind','Job','k8s.object.uid','uid-reindex-1'), map('k8s.event.reason','SuccessfulCreate'), '';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 50 MINUTE + INTERVAL 120 SECOND, map('reliant.env.id','prod','k8s.workload.name','reindex-ffffffffff','k8s.object.kind','Job','k8s.object.uid','uid-reindex-1'), map('k8s.event.reason','BackoffLimitExceeded'), '';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 40 MINUTE, map('reliant.env.id','prod','k8s.workload.name','reindex-ffffffffff','k8s.object.kind','Job','k8s.object.uid','uid-reindex-2'), map('k8s.event.reason','SuccessfulCreate'), '';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 40 MINUTE + INTERVAL 50 SECOND, map('reliant.env.id','prod','k8s.workload.name','reindex-ffffffffff','k8s.object.kind','Job','k8s.object.uid','uid-reindex-2'), map('k8s.event.reason','Completed'), '';
-- The same recreate WITHOUT a UID, which is what the node agent stores today (it drops k8s.object.uid): the create
-- that follows a terminal event begins the next execution. "rebuild" failed (1 minute), was recreated, and its second
-- execution used two pods (two creates) and completed in 70 seconds.
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 70 MINUTE, map('reliant.env.id','prod','k8s.workload.name','rebuild-9999999999','k8s.object.kind','Job'), map('k8s.event.reason','SuccessfulCreate'), '';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 70 MINUTE + INTERVAL 60 SECOND, map('reliant.env.id','prod','k8s.workload.name','rebuild-9999999999','k8s.object.kind','Job'), map('k8s.event.reason','DeadlineExceeded'), '';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 65 MINUTE, map('reliant.env.id','prod','k8s.workload.name','rebuild-9999999999','k8s.object.kind','Job'), map('k8s.event.reason','SuccessfulCreate'), 'pod 1';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 65 MINUTE + INTERVAL 5 SECOND, map('reliant.env.id','prod','k8s.workload.name','rebuild-9999999999','k8s.object.kind','Job'), map('k8s.event.reason','SuccessfulCreate'), 'pod 2';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 65 MINUTE + INTERVAL 70 SECOND, map('reliant.env.id','prod','k8s.workload.name','rebuild-9999999999','k8s.object.kind','Job'), map('k8s.event.reason','Completed'), '';
-- Warning events, which come from two kinds of object: the Job itself (named for the Job) and its pods (named for the
-- forge job, because the node agent reads the pod's app.kubernetes.io/name label first).
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 49 MINUTE, map('reliant.env.id','prod','k8s.workload.name','reindex-ffffffffff','k8s.object.kind','Job','k8s.object.uid','uid-reindex-1'), map('k8s.event.reason','BackoffLimitExceeded','k8s.event.type','Warning'), '';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 49 MINUTE, map('reliant.env.id','prod','k8s.workload.name','indexer','k8s.object.kind','Pod'), map('k8s.event.reason','FailedScheduling','k8s.event.type','Warning'), '';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 48 MINUTE, map('reliant.env.id','prod','k8s.workload.name','indexer','k8s.object.kind','Pod'), map('k8s.event.reason','BackOff','k8s.event.type','Warning'), '';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 48 MINUTE, map('reliant.env.id','prod','k8s.workload.name','other-thing-0123456789','k8s.object.kind','Job'), map('k8s.event.reason','BackoffLimitExceeded','k8s.event.type','Warning'), '';
INSERT INTO otel_logs (Timestamp, ResourceAttributes, LogAttributes, Body) SELECT now() - INTERVAL 48 MINUTE, map('reliant.env.id','prod','k8s.workload.name','indexer','k8s.object.kind','Pod'), map('k8s.event.reason','Pulled','k8s.event.type','Normal'), '';
-- Every source a Jobs panel reads, for two jobs, so a selection is checked on ALL of them:
--   indexer  reports as service.name "search-indexer" (its OTEL_SERVICE_NAME); its pods are "indexer"; its Job
--            objects are "indexer-eeeeeeeeee" (seeded above, with the Warning events)
--   reindex  reports as "reindex"; its Job objects are "reindex-ffffffffff"
-- Error-level application logs carry the service.name; restarts are node metrics keyed by the workload name.
CREATE TABLE otel_metrics_gauge (ResourceAttributes Map(String,String), MetricName String, TimeUnix DateTime64(9), Value Float64) ENGINE = MergeTree ORDER BY TimeUnix;
INSERT INTO otel_logs (Timestamp, ServiceName, SeverityText, Body, ResourceAttributes, LogAttributes) VALUES
  (now() - INTERVAL 29 MINUTE, 'search-indexer', 'ERROR', 'indexer failed to open shard', map('reliant.env.id','prod'), map()),
  (now() - INTERVAL 28 MINUTE, 'search-indexer', 'INFO',  'indexer started', map('reliant.env.id','prod'), map()),
  (now() - INTERVAL 45 MINUTE, 'reindex', 'ERROR', 'reindex hit the backoff limit', map('reliant.env.id','prod'), map()),
  (now() - INTERVAL 45 MINUTE, 'unrelated-service', 'ERROR', 'another service failed', map('reliant.env.id','prod'), map()),
  (now() - INTERVAL 45 MINUTE, 'indexer', 'ERROR', 'the workload name is not a service.name here', map('reliant.env.id','prod'), map());
INSERT INTO otel_metrics_gauge VALUES
  (map('reliant.env.id','prod','k8s.workload.name','indexer','k8s.container.name','main'), 'k8s.container.restarts', now() - INTERVAL 40 MINUTE, 0),
  (map('reliant.env.id','prod','k8s.workload.name','indexer','k8s.container.name','main'), 'k8s.container.restarts', now() - INTERVAL 20 MINUTE, 3),
  (map('reliant.env.id','prod','k8s.workload.name','reindex','k8s.container.name','main'), 'k8s.container.restarts', now() - INTERVAL 20 MINUTE, 1),
  (map('reliant.env.id','prod','k8s.workload.name','other-thing','k8s.container.name','main'), 'k8s.container.restarts', now() - INTERVAL 20 MINUTE, 9),
  (map('reliant.env.id','prod','k8s.workload.name','indexer','k8s.container.name','main'), 'k8s.container.cpu_limit_utilization', now() - INTERVAL 20 MINUTE, 0.5);
