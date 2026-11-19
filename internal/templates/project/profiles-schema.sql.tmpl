-- Local profiles schema, applied idempotently by docker-compose.yml's
-- `profiles-schema` service on every `forge env up`. YOURS: forge scaffolds it
-- once and never rewrites it.
--
-- Four tables, all in the `default` database next to ClickStack's own tables:
--
--   otel_profiles       RAW. One row per sample, written by the collector's
--                       clickhouse exporter. 24h TTL. Whole-profile and
--                       by-trace lookups (TraceId / SpanId / ProfileId).
--   profile_stacks      A dictionary of distinct stacks per service, keyed by the
--                       exporter's StackHash. 30d TTL.
--   profile_samples_1m  Per-minute totals per (service, env, type, stack). 30d TTL.
--                       This is what a flamegraph over a time range reads.
--   profile_pgo_edges   Per-hour CPU call-site edge weights per (namespace,
--                       service, env, version) for profile-guided builds. 30d TTL.
--
-- The raw table's columns and types must match the exporter's INSERT (contrib
-- v0.162.0, exporter/clickhouseexporter profiles_table.sql); only the TTL
-- differs. The text indexes need ClickHouse >= 26.2.

CREATE TABLE IF NOT EXISTS default.otel_profiles
(
    Timestamp DateTime64(9) CODEC(Delta, ZSTD(1)),
    ProfileId String CODEC(ZSTD(1)),
    SampleType LowCardinality(String) CODEC(ZSTD(1)),
    SampleUnit LowCardinality(String) CODEC(ZSTD(1)),
    ServiceName LowCardinality(String) CODEC(ZSTD(1)),
    ResourceAttributes Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    ScopeName String CODEC(ZSTD(1)),
    ScopeVersion String CODEC(ZSTD(1)),
    ProfileAttributes Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    SampleAttributes Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    StackHash UInt64 CODEC(ZSTD(1)),
    Addresses Array(UInt64) CODEC(ZSTD(1)),
    FunctionNames Array(LowCardinality(String)) CODEC(ZSTD(1)),
    FileNames Array(LowCardinality(String)) CODEC(ZSTD(1)),
    LineNumbers Array(Int32) CODEC(ZSTD(1)),
    MappingFileNames Array(LowCardinality(String)) CODEC(ZSTD(1)),
    Values Array(Int64) CODEC(ZSTD(1)),
    TimestampsUnixNano Array(UInt64) CODEC(ZSTD(1)),
    DurationNano UInt64 CODEC(ZSTD(1)),
    Period Int64 CODEC(ZSTD(1)),
    PeriodType LowCardinality(String) CODEC(ZSTD(1)),
    PeriodUnit LowCardinality(String) CODEC(ZSTD(1)),
    TraceId String CODEC(ZSTD(1)),
    SpanId String CODEC(ZSTD(1)),
    `__otel_materialized_k8s.cluster.name` LowCardinality(String) MATERIALIZED ResourceAttributes['k8s.cluster.name'] CODEC(ZSTD(1)),
    `__otel_materialized_k8s.container.name` LowCardinality(String) MATERIALIZED ResourceAttributes['k8s.container.name'] CODEC(ZSTD(1)),
    `__otel_materialized_k8s.deployment.name` LowCardinality(String) MATERIALIZED ResourceAttributes['k8s.deployment.name'] CODEC(ZSTD(1)),
    `__otel_materialized_k8s.namespace.name` LowCardinality(String) MATERIALIZED ResourceAttributes['k8s.namespace.name'] CODEC(ZSTD(1)),
    `__otel_materialized_k8s.node.name` LowCardinality(String) MATERIALIZED ResourceAttributes['k8s.node.name'] CODEC(ZSTD(1)),
    `__otel_materialized_k8s.pod.name` LowCardinality(String) MATERIALIZED ResourceAttributes['k8s.pod.name'] CODEC(ZSTD(1)),
    `__otel_materialized_k8s.pod.uid` LowCardinality(String) MATERIALIZED ResourceAttributes['k8s.pod.uid'] CODEC(ZSTD(1)),
    `__otel_materialized_deployment.environment.name` LowCardinality(String) MATERIALIZED ResourceAttributes['deployment.environment.name'] CODEC(ZSTD(1)),
    `__otel_materialized_service.namespace` LowCardinality(String) MATERIALIZED ResourceAttributes['service.namespace'] CODEC(ZSTD(1)),
    `__otel_materialized_service.instance.id` LowCardinality(String) MATERIALIZED ResourceAttributes['service.instance.id'] CODEC(ZSTD(1)),
    INDEX idx_profile_id ProfileId TYPE text(tokenizer = 'array'),
    INDEX idx_trace_id TraceId TYPE text(tokenizer = 'array'),
    INDEX idx_res_attr_key mapKeys(ResourceAttributes) TYPE text(tokenizer = 'array'),
    INDEX idx_res_attr_value mapValues(ResourceAttributes) TYPE text(tokenizer = 'array'),
    INDEX idx_sample_attr_key mapKeys(SampleAttributes) TYPE text(tokenizer = 'array'),
    INDEX idx_sample_attr_value mapValues(SampleAttributes) TYPE text(tokenizer = 'array'),
    INDEX idx_function_names FunctionNames TYPE text(tokenizer = 'splitByNonAlpha')
)
ENGINE = MergeTree
PARTITION BY toDate(Timestamp)
ORDER BY (ServiceName, SampleType, toDateTime(Timestamp))
TTL toDateTime(Timestamp) + INTERVAL 24 HOUR
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS default.profile_stacks
(
    ServiceName   LowCardinality(String),
    StackHash     UInt64,
    FunctionNames Array(LowCardinality(String)) CODEC(ZSTD(3)),
    FileNames     Array(LowCardinality(String)) CODEC(ZSTD(3)),
    LastSeen      DateTime CODEC(Delta, ZSTD(1))
)
ENGINE = ReplacingMergeTree(LastSeen)
ORDER BY (ServiceName, StackHash)
TTL LastSeen + INTERVAL 30 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS default.profile_stacks_mv TO default.profile_stacks AS
SELECT ServiceName, StackHash,
       any(FunctionNames) AS FunctionNames, any(FileNames) AS FileNames,
       max(toDateTime(Timestamp)) AS LastSeen
FROM default.otel_profiles
GROUP BY ServiceName, StackHash;

CREATE TABLE IF NOT EXISTS default.profile_samples_1m
(
    ServiceName LowCardinality(String),
    Env         LowCardinality(String),
    SampleType  LowCardinality(String),
    SampleUnit  LowCardinality(String),
    Minute      DateTime CODEC(Delta, ZSTD(1)),
    StackHash   UInt64,
    Value       Int64,
    Samples     UInt64
)
ENGINE = SummingMergeTree((Value, Samples))
PARTITION BY toDate(Minute)
ORDER BY (ServiceName, SampleType, Env, Minute, StackHash)
TTL Minute + INTERVAL 30 DAY
SETTINGS ttl_only_drop_parts = 1;

-- `samples` is the count-only twin of `cpu` that Go CPU profiles carry. pkg/observe
-- drops it before export; the filter keeps other producers from doubling storage.
CREATE MATERIALIZED VIEW IF NOT EXISTS default.profile_samples_1m_mv TO default.profile_samples_1m AS
SELECT ServiceName,
       ResourceAttributes['deployment.environment.name'] AS Env,
       SampleType, SampleUnit,
       toStartOfMinute(Timestamp) AS Minute,
       StackHash,
       sum(arraySum(Values)) AS Value,
       count() AS Samples
FROM default.otel_profiles
WHERE SampleType != 'samples'
GROUP BY ServiceName, Env, SampleType, SampleUnit, Minute, StackHash;

-- Call-site edges for profile-guided builds (`forge pgo pull`), per hour.
-- pkg/observe puts two attributes on every CPU sample:
--   forge.profile.start_lines  each frame's Function.StartLine, comma-separated,
--                              aligned with FunctionNames (the exporter drops it)
--   forge.profile.leaf_frames  how many leading frames belong to the stack's two
--                              leaf-most locations (inlined frames included)
-- The view counts edges the way Go's PGO graph does (internal/profile
-- NewGraph): only among those leaf frames, frame i+1 the caller of frame i. A
-- frame is a node (Address, LineNumber, FunctionName, StartLine); a sample adds
-- its weight once per distinct (caller node, callee node) pair and never for a
-- node paired with itself. Only then is a pair projected to (caller name,
-- caller line - caller start line, callee name) and summed, so one call
-- reached through two different node pairs in a sample counts twice, as in Go.
-- Unnamed (unsymbolized) frames cannot match a call site and are left out. A
-- row without both attributes (an older pkg/observe, another producer) or with
-- arrays that do not align contributes nothing.
CREATE TABLE IF NOT EXISTS default.profile_pgo_edges
(
    ServiceNamespace LowCardinality(String),
    ServiceName      LowCardinality(String),
    Env              LowCardinality(String),
    ServiceVersion   LowCardinality(String),
    Hour             DateTime,
    Caller           LowCardinality(String),
    Callee           LowCardinality(String),
    CallSiteOffset   Int32,
    Weight           Int64
)
ENGINE = SummingMergeTree(Weight)
PARTITION BY toDate(Hour)
ORDER BY (ServiceNamespace, ServiceName, Env, Hour, Caller, Callee, CallSiteOffset, ServiceVersion)
TTL Hour + INTERVAL 30 DAY
SETTINGS ttl_only_drop_parts = 1;

CREATE MATERIALIZED VIEW IF NOT EXISTS default.profile_pgo_edges_mv TO default.profile_pgo_edges AS
SELECT ServiceNamespace, ServiceName, Env, ServiceVersion, Hour,
       Pair.1.3 AS Caller, Pair.2.3 AS Callee, toInt32(Pair.1.2 - Pair.1.4) AS CallSiteOffset,
       sum(Value) AS Weight
FROM
(
    SELECT ResourceAttributes['service.namespace'] AS ServiceNamespace,
           ServiceName,
           ResourceAttributes['deployment.environment.name'] AS Env,
           ResourceAttributes['service.version'] AS ServiceVersion,
           toStartOfHour(Timestamp) AS Hour,
           arraySum(Values) AS Value,
           -- one node per frame: (address, line, name, start line)
           arrayMap((a, l, n, s) -> (a, toInt64(l), n, s), Addresses, LineNumbers, FunctionNames,
               arrayMap(s -> toInt64OrZero(s), splitByChar(',', SampleAttributes['forge.profile.start_lines']))) AS Nodes,
           least(toUInt64OrZero(SampleAttributes['forge.profile.leaf_frames']), length(FunctionNames)) AS Leaf
    FROM default.otel_profiles
    WHERE SampleType = 'cpu' AND SampleUnit = 'nanoseconds'
      AND mapContains(SampleAttributes, 'forge.profile.start_lines')
      AND mapContains(SampleAttributes, 'forge.profile.leaf_frames')
      AND length(LineNumbers) = length(FunctionNames)
      AND length(Addresses) = length(FunctionNames)
      AND length(splitByChar(',', SampleAttributes['forge.profile.start_lines'])) = length(FunctionNames)
)
-- (caller node, callee node) pairs: distinct, never a node with itself, named
ARRAY JOIN arrayFilter(p -> p.1.3 != '' AND p.2.3 != '',
    arrayDistinct(arrayFilter(p -> p.1 != p.2,
        arrayMap(i -> (Nodes[i + 1], Nodes[i]), range(1, Leaf))))) AS Pair
GROUP BY ServiceNamespace, ServiceName, Env, ServiceVersion, Hour, Caller, Callee, CallSiteOffset;
