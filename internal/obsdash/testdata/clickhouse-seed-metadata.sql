TRUNCATE TABLE otel_metrics_gauge; TRUNCATE TABLE otel_logs;

-- Customer metadata for env 'env-1', as the node agent writes it: restarts every 5 minutes, memory
-- utilization every minute, Kubernetes events as logs. Nothing carries deployment.environment.name.

-- crashy: one pod, restarts 2 -> 3 -> 5 (grew by 3).
INSERT INTO otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value)
SELECT map('reliant.env.id','env-1','k8s.workload.name','crashy','k8s.container.name','app','k8s.pod.uid','u-crashy','k8s.pod.name','crashy-1'),
       'crashy', 'k8s.container.restarts', map('k8s.container.last_terminated_reason','Error'), now() - INTERVAL t MINUTE, v
FROM (SELECT arrayJoin([(14,2),(9,3),(4,5)]) AS p, p.1 AS t, p.2 AS v);

-- twice: 2 restarts only. Below the default of 3.
INSERT INTO otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value)
SELECT map('reliant.env.id','env-1','k8s.workload.name','twice','k8s.container.name','app','k8s.pod.uid','u-twice','k8s.pod.name','twice-1'),
       'twice', 'k8s.container.restarts', map('k8s.container.last_terminated_reason','Error'), now() - INTERVAL t MINUTE, v
FROM (SELECT arrayJoin([(14,0),(9,1),(4,2)]) AS p, p.1 AS t, p.2 AS v);

-- old: a high restart count that is not moving. Lifetime restarts are not a crash loop.
INSERT INTO otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value)
SELECT map('reliant.env.id','env-1','k8s.workload.name','old','k8s.container.name','app','k8s.pod.uid','u-old','k8s.pod.name','old-1'),
       'old', 'k8s.container.restarts', map('k8s.container.last_terminated_reason','OOMKilled'), now() - INTERVAL t MINUTE, 40
FROM (SELECT arrayJoin([14,9,4]) AS t);

-- rolled: the pod was replaced (a new pod uid starts at 0 while the old one stopped reporting at 8).
-- Per-pod growth is 0 for both; a sum of raw values or a series drop must not read as a restart.
INSERT INTO otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value)
SELECT map('reliant.env.id','env-1','k8s.workload.name','rolled','k8s.container.name','app','k8s.pod.uid','u-rolled-a','k8s.pod.name','rolled-a'),
       'rolled', 'k8s.container.restarts', map(), now() - INTERVAL t MINUTE, 8
FROM (SELECT arrayJoin([14,9]) AS t);
INSERT INTO otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value)
SELECT map('reliant.env.id','env-1','k8s.workload.name','rolled','k8s.container.name','app','k8s.pod.uid','u-rolled-b','k8s.pod.name','rolled-b'),
       'rolled', 'k8s.container.restarts', map(), now() - INTERVAL t MINUTE, 0
FROM (SELECT arrayJoin([4]) AS t);

-- oomer: one fresh restart whose last exit was OOMKilled. A single restart is not a crash loop.
INSERT INTO otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value)
SELECT map('reliant.env.id','env-1','k8s.workload.name','oomer','k8s.container.name','app','k8s.pod.uid','u-oomer','k8s.pod.name','oomer-1'),
       'oomer', 'k8s.container.restarts', map('k8s.container.last_terminated_reason', if(t > 6, '', 'OOMKilled')), now() - INTERVAL t MINUTE, if(t > 6, 0, 1)
FROM (SELECT arrayJoin([9,4]) AS t);

-- oomres: the reason arrives as a RESOURCE attribute instead of a data-point one (K2 allows either).
INSERT INTO otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value)
SELECT map('reliant.env.id','env-1','k8s.workload.name','oomres','k8s.container.name','app','k8s.pod.uid','u-oomres','k8s.pod.name','oomres-1','k8s.container.last_terminated_reason','OOMKilled'),
       'oomres', 'k8s.container.restarts', map(), now() - INTERVAL t MINUTE, if(t > 6, 0, 1)
FROM (SELECT arrayJoin([9,4]) AS t);

-- other: the same crash loop in ANOTHER env.
INSERT INTO otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value)
SELECT map('reliant.env.id','env-2','k8s.workload.name','other','k8s.container.name','app','k8s.pod.uid','u-other','k8s.pod.name','other-1'),
       'other', 'k8s.container.restarts', map('k8s.container.last_terminated_reason','OOMKilled'), now() - INTERVAL t MINUTE, v
FROM (SELECT arrayJoin([(14,0),(9,4),(4,9)]) AS p, p.1 AS t, p.2 AS v);

-- memory: hot sits at 95% of its limit for the whole window; spike averages 55% with one 99% sample;
-- fine is at 50%; hot2 is hot in env-2 only.
INSERT INTO otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value)
SELECT map('reliant.env.id','env-1','k8s.workload.name','hot','k8s.container.name','app','k8s.pod.uid','u-hot','k8s.pod.name','hot-1'),
       'hot', 'k8s.container.memory_limit_utilization', map(), now() - INTERVAL t MINUTE, 0.95 FROM (SELECT number t FROM numbers(5));
INSERT INTO otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value)
SELECT map('reliant.env.id','env-1','k8s.workload.name','spike','k8s.container.name','app','k8s.pod.uid','u-spike','k8s.pod.name','spike-1'),
       'spike', 'k8s.container.memory_limit_utilization', map(), now() - INTERVAL t MINUTE, if(t = 2, 0.99, 0.5) FROM (SELECT number t FROM numbers(5));
INSERT INTO otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value)
SELECT map('reliant.env.id','env-1','k8s.workload.name','fine','k8s.container.name','app','k8s.pod.uid','u-fine','k8s.pod.name','fine-1'),
       'fine', 'k8s.container.memory_limit_utilization', map(), now() - INTERVAL t MINUTE, 0.5 FROM (SELECT number t FROM numbers(5));
INSERT INTO otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value)
SELECT map('reliant.env.id','env-2','k8s.workload.name','hot2','k8s.container.name','app','k8s.pod.uid','u-hot2','k8s.pod.name','hot2-1'),
       'hot2', 'k8s.container.memory_limit_utilization', map(), now() - INTERVAL t MINUTE, 0.97 FROM (SELECT number t FROM numbers(5));
-- a workload with a memory limit utilization series but nothing else: the working-set gauge must not be mistaken for it.
INSERT INTO otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value)
SELECT map('reliant.env.id','env-1','k8s.workload.name','bytes','k8s.container.name','app','k8s.pod.uid','u-bytes','k8s.pod.name','bytes-1'),
       'bytes', 'container.memory.working_set', map(), now() - INTERVAL t MINUTE, 900000000 FROM (SELECT number t FROM numbers(5));

-- Events (otel_logs). The event's own text is the Body.
INSERT INTO otel_logs (Timestamp, ServiceName, SeverityText, Body, ResourceAttributes, LogAttributes)
SELECT now() - INTERVAL m MINUTE, wl, 'Warning', body,
       map('reliant.env.id', env, 'k8s.workload.name', wl, 'k8s.object.kind', 'Pod'),
       map('k8s.event.reason', reason, 'k8s.event.type', 'Warning', 'k8s.event.count', '1')
FROM (SELECT arrayJoin([
    ('env-1', 'backoff',   'BackOff',          'Back-off restarting failed container app in pod backoff-1', 3),
    ('env-1', 'backoff',   'BackOff',          'Back-off restarting failed container app in pod backoff-1', 2),
    ('env-1', 'pulling',   'BackOff',          'Back-off pulling image "registry.example/pulling:1"', 4),
    ('env-1', 'imgfail',   'Failed',           'Failed to pull image "registry.example/imgfail:9": not found', 5),
    ('env-1', 'imgfail',   'ErrImagePull',     'rpc error: code = NotFound desc = failed to pull and unpack image', 5),
    ('env-1', 'quietpull', 'Pulling',          'Pulling image "registry.example/quietpull:1"', 3),
    ('env-1', 'quietpull', 'Pulled',           'Successfully pulled image "registry.example/quietpull:1" in 2s', 2),
    ('env-1', 'evictee',   'Evicted',          'The node was low on resource: memory.', 6),
    ('env-1', 'oldevict',  'Evicted',          'The node was low on resource: memory.', 90),
    ('env-1', 'oomevent',  'OOMKilling',       'Memory cgroup out of memory: Killed process 1 (app)', 4),
    ('env-2', 'elsewhere', 'Evicted',          'The node was low on resource: memory.', 3),
    ('env-2', 'elsewhere', 'ErrImagePull',     'failed to pull image', 3)
  ]) AS e, e.1 AS env, e.2 AS wl, e.3 AS reason, e.4 AS body, e.5 AS m);

-- a workload event row with no workload name must not become a blank label.
INSERT INTO otel_logs (Timestamp, ServiceName, SeverityText, Body, ResourceAttributes, LogAttributes)
VALUES (now() - INTERVAL 2 MINUTE, '', 'Warning', 'The node was low on resource: memory.',
        map('reliant.env.id','env-1','k8s.object.kind','Node'), map('k8s.event.reason','Evicted','k8s.event.type','Warning'));
