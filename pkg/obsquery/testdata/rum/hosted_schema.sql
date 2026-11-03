-- The browser tables of a hosted observability database, for pkg/obsquery's integration tests.
-- GENERATED from control-plane internal/obstenant/schema/010_tenant_create.sql (tables and
-- materialized views, TTLs dropped, {{DB}} = the hosted database) and forge internal/obsbackend/obstest
-- exporter_schema.sql (otel_logs, otel_traces). mv_in_rum_replay is the landing view of
-- 010 without its org filter and DEFINER. Statements are separated by a blank line.
CREATE DATABASE {{DB}};

-- The ClickHouse exporter's tables (v0.162.0) as the local ClickStack creates them, trimmed to

CREATE TABLE {{DB}}.otel_logs
(
    `Timestamp` DateTime64(9) COMMENT 'Event timestamp with nanosecond precision' CODEC(Delta(8), ZSTD(1)),
    `TraceId` String COMMENT 'W3C trace identifier' CODEC(ZSTD(1)),
    `SpanId` String COMMENT 'W3C span identifier' CODEC(ZSTD(1)),
    `TraceFlags` UInt8 COMMENT 'W3C trace flags',
    `SeverityText` LowCardinality(String) COMMENT 'Log severity as text' CODEC(ZSTD(1)),
    `SeverityNumber` UInt8 COMMENT 'Log severity as number (1-24)',
    `ServiceName` LowCardinality(String) COMMENT 'Service that emitted the log' CODEC(ZSTD(1)),
    `Body` String COMMENT 'Log message body' CODEC(ZSTD(1)),
    `ResourceSchemaUrl` LowCardinality(String) COMMENT 'Schema URL for the resource' CODEC(ZSTD(1)),
    `ResourceAttributes` Map(LowCardinality(String), String) COMMENT 'Resource attributes as key-value pairs' CODEC(ZSTD(1)),
    `ScopeSchemaUrl` LowCardinality(String) COMMENT 'Schema URL for the instrumentation scope' CODEC(ZSTD(1)),
    `ScopeName` String COMMENT 'Instrumentation scope name' CODEC(ZSTD(1)),
    `ScopeVersion` LowCardinality(String) COMMENT 'Instrumentation scope version' CODEC(ZSTD(1)),
    `ScopeAttributes` Map(LowCardinality(String), String) COMMENT 'Instrumentation scope attributes' CODEC(ZSTD(1)),
    `LogAttributes` Map(LowCardinality(String), String) COMMENT 'Log record attributes' CODEC(ZSTD(1)),
    `EventName` String COMMENT 'Event name for log records representing events' CODEC(ZSTD(1)),
    `__otel_materialized_k8s.cluster.name` LowCardinality(String) MATERIALIZED ResourceAttributes['k8s.cluster.name'] CODEC(ZSTD(1)),
    `__otel_materialized_k8s.container.name` LowCardinality(String) MATERIALIZED ResourceAttributes['k8s.container.name'] CODEC(ZSTD(1)),
    `__otel_materialized_k8s.deployment.name` LowCardinality(String) MATERIALIZED ResourceAttributes['k8s.deployment.name'] CODEC(ZSTD(1)),
    `__otel_materialized_k8s.namespace.name` LowCardinality(String) MATERIALIZED ResourceAttributes['k8s.namespace.name'] CODEC(ZSTD(1)),
    `__otel_materialized_k8s.node.name` LowCardinality(String) MATERIALIZED ResourceAttributes['k8s.node.name'] CODEC(ZSTD(1)),
    `__otel_materialized_k8s.pod.name` LowCardinality(String) MATERIALIZED ResourceAttributes['k8s.pod.name'] CODEC(ZSTD(1)),
    `__otel_materialized_k8s.pod.uid` LowCardinality(String) MATERIALIZED ResourceAttributes['k8s.pod.uid'] CODEC(ZSTD(1)),
    `__otel_materialized_deployment.environment.name` LowCardinality(String) MATERIALIZED ResourceAttributes['deployment.environment.name'] CODEC(ZSTD(1))
)
ENGINE = MergeTree
PARTITION BY toDate(Timestamp)
ORDER BY (toStartOfFiveMinutes(Timestamp), ServiceName, Timestamp)
SETTINGS index_granularity = 8192;

CREATE TABLE {{DB}}.otel_traces
(
    `Timestamp` DateTime64(9) CODEC(Delta(8), ZSTD(1)),
    `TraceId` String CODEC(ZSTD(1)),
    `SpanId` String CODEC(ZSTD(1)),
    `ParentSpanId` String CODEC(ZSTD(1)),
    `TraceState` String CODEC(ZSTD(1)),
    `SpanName` LowCardinality(String) CODEC(ZSTD(1)),
    `SpanKind` LowCardinality(String) CODEC(ZSTD(1)),
    `ServiceName` LowCardinality(String) CODEC(ZSTD(1)),
    `ResourceAttributes` Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    `ScopeName` String CODEC(ZSTD(1)),
    `ScopeVersion` String CODEC(ZSTD(1)),
    `SpanAttributes` Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    `Duration` UInt64 CODEC(ZSTD(1)),
    `StatusCode` LowCardinality(String) CODEC(ZSTD(1)),
    `StatusMessage` String CODEC(ZSTD(1)),
    `Events.Timestamp` Array(DateTime64(9)) CODEC(ZSTD(1)),
    `Events.Name` Array(LowCardinality(String)) CODEC(ZSTD(1)),
    `Events.Attributes` Array(Map(LowCardinality(String), String)) CODEC(ZSTD(1)),
    `Links.TraceId` Array(String) CODEC(ZSTD(1)),
    `Links.SpanId` Array(String) CODEC(ZSTD(1)),
    `Links.TraceState` Array(String) CODEC(ZSTD(1)),
    `Links.Attributes` Array(Map(LowCardinality(String), String)) CODEC(ZSTD(1))
)
ENGINE = MergeTree
PARTITION BY toDate(Timestamp)
ORDER BY (ServiceName, SpanName, toDateTime(Timestamp))
SETTINGS index_granularity = 8192;

CREATE TABLE IF NOT EXISTS {{DB}}.rum_replay
(
    `Timestamp` DateTime64(9) CODEC(Delta(8), ZSTD(1)),
    `SessionId` String CODEC(ZSTD(1)),
    `ServiceName` LowCardinality(String) CODEC(ZSTD(1)),
    `ServiceVersion` LowCardinality(String) CODEC(ZSTD(1)),
    `EnvId` LowCardinality(String) CODEC(ZSTD(1)),
    `EventIndex` UInt32 CODEC(ZSTD(1)),
    `Chunk` UInt16 CODEC(ZSTD(1)),
    `TotalChunks` UInt16 CODEC(ZSTD(1)),
    `Body` String CODEC(ZSTD(3))
)
ENGINE = MergeTree
PARTITION BY toDate(Timestamp)
ORDER BY (SessionId, Timestamp, EventIndex, Chunk)
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS {{DB}}.rum_sessions
(
    `ServiceName` LowCardinality(String),
    `SessionId` String CODEC(ZSTD(1)),
    `Day` Date,
    `Start` SimpleAggregateFunction(min, DateTime64(3)),
    `End` SimpleAggregateFunction(max, DateTime64(3)),
    `ServiceVersion` SimpleAggregateFunction(max, String),
    `EnvId` SimpleAggregateFunction(max, String),
    `UserId` SimpleAggregateFunction(max, String),
    `EntryPath` AggregateFunction(argMin, String, DateTime64(9)),
    `PageViews` SimpleAggregateFunction(sum, UInt64),
    `Interactions` SimpleAggregateFunction(sum, UInt64),
    `Errors` SimpleAggregateFunction(sum, UInt64),
    `HasReplay` SimpleAggregateFunction(max, UInt8)
)
ENGINE = AggregatingMergeTree
PARTITION BY Day
ORDER BY (ServiceName, SessionId)
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS {{DB}}.error_events
(
    `Timestamp` DateTime64(9) CODEC(Delta(8), ZSTD(1)),
    `ServiceName` LowCardinality(String) CODEC(ZSTD(1)),
    `ServiceVersion` LowCardinality(String) CODEC(ZSTD(1)),
    `Frontend` LowCardinality(String) CODEC(ZSTD(1)),
    `EnvId` LowCardinality(String) CODEC(ZSTD(1)),
    `SessionId` String CODEC(ZSTD(1)),
    `TraceId` String CODEC(ZSTD(1)),
    `SpanId` String CODEC(ZSTD(1)),
    `SpanName` LowCardinality(String) CODEC(ZSTD(1)),
    `ExceptionType` LowCardinality(String) CODEC(ZSTD(1)),
    `ExceptionMessage` String CODEC(ZSTD(1)),
    `MessageNorm` String CODEC(ZSTD(1)),
    `StackRaw` String CODEC(ZSTD(3)),
    `StackText` String CODEC(ZSTD(3)),
    `UrlPath` String CODEC(ZSTD(1)),
    `RawStackHash` UInt64 CODEC(ZSTD(1)),
    INDEX idx_session_id SessionId TYPE bloom_filter(0.01) GRANULARITY 1
)
ENGINE = MergeTree
PARTITION BY toDate(Timestamp)
ORDER BY (ServiceName, toStartOfHour(Timestamp), RawStackHash)
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS {{DB}}.error_stacks
(
    `ServiceName` LowCardinality(String),
    `ServiceVersion` LowCardinality(String),
    `RawStackHash` UInt64,
    `Fingerprint` String,
    `FpVersion` UInt8,
    `Title` String CODEC(ZSTD(1)),
    `Culprit` String CODEC(ZSTD(1)),
    `FramesJson` String CODEC(ZSTD(3)),
    `Symbolicated` UInt8,
    `NoStack` UInt8,
    `UpdatedAt` DateTime64(3)
)
ENGINE = ReplacingMergeTree(UpdatedAt)
ORDER BY (ServiceName, ServiceVersion, RawStackHash)
SETTINGS index_granularity = 8192;

CREATE MATERIALIZED VIEW {{DB}}.mv_in_rum_replay TO {{DB}}.rum_replay
AS SELECT
    Timestamp,
    ResourceAttributes['rum.sessionId'] AS SessionId,
    ServiceName,
    ResourceAttributes['service.version'] AS ServiceVersion,
    ResourceAttributes['reliant.env.id'] AS EnvId,
    toUInt32OrZero(LogAttributes['rr-web.event']) AS EventIndex,
    toUInt16OrZero(LogAttributes['rr-web.chunk']) AS Chunk,
    toUInt16OrZero(LogAttributes['rr-web.total-chunks']) AS TotalChunks,
    Body
FROM {{DB}}.otel_logs
WHERE ScopeName = 'rum.rr-web';

CREATE MATERIALIZED VIEW IF NOT EXISTS {{DB}}.mv_error_events TO {{DB}}.error_events
AS WITH
    arrayFilter((a, n) -> n = 'exception', `Events.Attributes`, `Events.Name`) AS exs,
    exs[-1] AS ex,
    if(ex['exception.type'] != '', ex['exception.type'], SpanAttributes['error.object']) AS etype,
    if(ex['exception.message'] != '', ex['exception.message'], SpanAttributes['error.message']) AS emsg,
    ex['exception.parsed_stacktrace'] AS estack,
    substring(replaceRegexpAll(replaceRegexpAll(replaceRegexpAll(replaceRegexpAll(replaceRegexpAll(
        splitByChar('\n', emsg)[1],
        '[a-zA-Z][a-zA-Z0-9+.-]*://[^\\s\\x27")]+', '<url>'),
        '[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}', '<uuid>'),
        '\\x27[^\\x27]*\\x27|"[^"]*"', '<str>'),
        '\\b[0-9a-fA-F]{8,}\\b', '<hex>'),
        '\\b[0-9]+\\b', '<n>'), 1, 200) AS norm
SELECT
    Timestamp,
    ServiceName,
    ResourceAttributes['service.version'] AS ServiceVersion,
    ResourceAttributes['forge.frontend'] AS Frontend,
    ResourceAttributes['reliant.env.id'] AS EnvId,
    ResourceAttributes['rum.sessionId'] AS SessionId,
    TraceId,
    SpanId,
    SpanName,
    etype AS ExceptionType,
    substring(emsg, 1, 1024) AS ExceptionMessage,
    norm AS MessageNorm,
    estack AS StackRaw,
    if(SpanAttributes['error.stack'] != '', SpanAttributes['error.stack'], ex['exception.stacktrace']) AS StackText,
    path(SpanAttributes['location.href']) AS UrlPath,
    cityHash64(etype, norm, replaceRegexpAll(estack, '"filename":"[a-zA-Z][a-zA-Z0-9+.-]*://[^/"]*', '"filename":"')) AS RawStackHash
FROM {{DB}}.otel_traces
WHERE ResourceAttributes['rum.sessionId'] != ''
  AND StatusCode = 'Error'
  AND length(exs) > 0
  AND (SpanName IN ('onerror', 'unhandledrejection', 'console.error') OR ScopeName = 'record-exception');

CREATE MATERIALIZED VIEW IF NOT EXISTS {{DB}}.mv_rum_sessions_spans TO {{DB}}.rum_sessions
AS SELECT
    ServiceName,
    ResourceAttributes['rum.sessionId'] AS SessionId,
    toDate(Timestamp) AS Day,
    toDateTime64(min(Timestamp), 3) AS Start,
    toDateTime64(max(Timestamp), 3) AS End,
    max(ResourceAttributes['service.version']) AS ServiceVersion,
    max(ResourceAttributes['reliant.env.id']) AS EnvId,
    max(SpanAttributes['userId']) AS UserId,
    argMinIfState(path(SpanAttributes['location.href']), Timestamp, SpanAttributes['location.href'] != '') AS EntryPath,
    countIf(SpanName IN ('documentLoad', 'routeChange')) AS PageViews,
    countIf(SpanAttributes['component'] = 'user-interaction' AND SpanName = 'mousedown') AS Interactions,
    countIf(StatusCode = 'Error' AND has(`Events.Name`, 'exception') AND (SpanName IN ('onerror', 'unhandledrejection', 'console.error') OR ScopeName = 'record-exception')) AS Errors
FROM {{DB}}.otel_traces
WHERE ResourceAttributes['rum.sessionId'] != ''
GROUP BY ServiceName, SessionId, Day;

CREATE MATERIALIZED VIEW IF NOT EXISTS {{DB}}.mv_rum_sessions_replay TO {{DB}}.rum_sessions
AS SELECT
    ServiceName,
    SessionId,
    toDate(Timestamp) AS Day,
    toDateTime64(min(Timestamp), 3) AS Start,
    toDateTime64(max(Timestamp), 3) AS End,
    max(ServiceVersion) AS ServiceVersion,
    max(EnvId) AS EnvId,
    toUInt8(1) AS HasReplay
FROM {{DB}}.rum_replay
WHERE SessionId != ''
GROUP BY ServiceName, SessionId, Day;
