// Copyright (c) 2025 Reliant Labs

/**
 * The wire mapping: what api.ts sends control-plane and how it reads the
 * answer back into the console's model.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  AttributeOp,
  Signal,
  SortOrder,
  WorkloadHealth,
  WorkloadJobResult,
  WorkloadMetric,
  WorkloadPodState,
  WorkloadReleaseBasis,
} from "@/gen/controlplane/services/observability/v1/observability_pb";
import { WorkloadKind } from "@/gen/controlplane/controlplane/v1/deploy_pb";

import {
  composeLogQuery,
  getServiceOverview,
  getTrace,
  getWorkload,
  listAttributeValues,
  listTraces,
  listWorkloads,
  msOf,
  queryPanel,
  searchLogs,
  severityOf,
  statusOf,
  wireScope,
} from "../api";
import { obsErrorKind, obsRetry } from "../errors";

const client = {
  searchLogs: vi.fn(),
  listTraces: vi.fn(),
  queryPanel: vi.fn(),
  getTrace: vi.fn(),
  getServiceOverview: vi.fn(),
  listAttributeValues: vi.fn(),
  listWorkloads: vi.fn(),
  getWorkload: vi.fn(),
};

vi.mock("../../client", () => ({ getControlPlaneClient: () => client }));

const env = { orgId: "org-1", envIds: ["env-1"], filters: [] };
const window = { startMs: Date.UTC(2026, 9, 12, 6), endMs: Date.UTC(2026, 9, 12, 7) };
const ts = (ms: number, extraNanos = 0) =>
  create(TimestampSchema, { seconds: BigInt(Math.floor(ms / 1000)), nanos: (ms % 1000) * 1e6 + extraNanos });

beforeEach(() => vi.clearAllMocks());

describe("request shape", () => {
  it("scopes SearchLogs by org, env, services and window, newest first", async () => {
    client.searchLogs.mockResolvedValue({ logs: [], nextPageToken: "" });
    await searchLogs({ ...env, window, query: "timeout", services: ["api"], severities: ["warn", "error"], limit: 200 });
    const req = client.searchLogs.mock.calls[0][0];
    expect(req.scope).toMatchObject({ orgId: "org-1", envIds: ["env-1"], services: ["api"] });
    expect(msOf(req.scope.timeRange.start)).toBe(window.startMs);
    expect(msOf(req.scope.timeRange.end)).toBe(window.endMs);
    expect(req).toMatchObject({ query: "timeout", severities: ["WARN", "ERROR"], order: SortOrder.NEWEST_FIRST, pageSize: 200, pageToken: "" });
  });

  it("appends only hex trace/span ids to the query, and keeps a reader's OR from swallowing them", () => {
    expect(composeLogQuery("", "4bf92f35", "00f067aa")).toBe("trace_id:4bf92f35 span_id:00f067aa");
    expect(composeLogQuery("a OR b", "abc")).toBe("(a OR b) trace_id:abc");
    expect(composeLogQuery("x", "abc OR 1=1")).toBe("x");
  });
});

describe("response mapping", () => {
  it("reads severity from the OTel number, falling back to the producer's text", () => {
    expect(severityOf(17, "")).toBe("error");
    expect(severityOf(13, "whatever")).toBe("warn");
    expect(severityOf(0, "WARNING")).toBe("warn");
    expect(severityOf(0, "panic")).toBe("fatal");
    expect(severityOf(0, "")).toBe("unknown");
  });

  it("maps log records, keeping operator provenance", async () => {
    client.searchLogs.mockResolvedValue({
      logs: [
        {
          timestamp: ts(window.endMs - 1000),
          severityText: "ERROR",
          severityNumber: 17,
          service: "reliant-daemon",
          body: "boom",
          traceId: "",
          spanId: "",
          attributes: { a: "1" },
          resourceAttributes: { r: "2" },
          envId: "env-1",
          source: "daemon",
          operatorData: true,
        },
      ],
      nextPageToken: "next",
    });
    const result = await searchLogs({ ...env, window, query: "", services: [], severities: [], limit: 1 });
    expect(result.nextCursor).toBe("next");
    expect(result.records[0]).toMatchObject({
      timeMs: window.endMs - 1000,
      severity: "error",
      service: "reliant-daemon",
      attributes: { a: "1" },
      resource: { r: "2" },
      source: "daemon",
      operatorData: true,
    });
  });

  it("keeps sub-millisecond span timing and normalizes status codes", async () => {
    client.getTrace.mockResolvedValue({
      traceId: "t",
      truncated: false,
      relatedTraces: [{ traceId: "incoming", spanId: "server", linkedSpanId: "s" }],
      relatedTracesTruncated: true,
      spans: [
        {
          spanId: "s",
          parentSpanId: "",
          name: "op",
          kind: "SPAN_KIND_SERVER",
          service: "api",
          startTime: ts(window.startMs, 250_000),
          durationNs: 42_000n,
          statusCode: "STATUS_CODE_ERROR",
          statusMessage: "bad",
          attributes: {},
          resourceAttributes: {},
          events: [],
          links: [{ traceId: "outgoing", spanId: "browser" }],
          source: "",
          operatorData: false,
        },
      ],
    });
    const trace = await getTrace({ ...env, traceId: "t", window });
    expect(trace.spans[0]).toMatchObject({ kind: "server", status: "error", durationMs: 0.042 });
    expect(trace.spans[0].startMs - window.startMs).toBeCloseTo(0.25, 6);
    expect(trace.spans[0].parentSpanId).toBe("");
    expect(trace.spans[0].links).toEqual([{ traceId: "outgoing", spanId: "browser" }]);
    expect(trace.relatedTraces).toEqual([{ traceId: "incoming", spanId: "server", linkedSpanId: "s" }]);
    expect(trace.relatedTracesTruncated).toBe(true);
    expect(statusOf("Ok")).toBe("ok");
    expect(statusOf("")).toBe("unset");
  });

  it("derives per-bucket rate and error rate from the overview's counts", async () => {
    const t = (min: number) => ({ time: ts(window.startMs + min * 60_000), value: 0 });
    client.getServiceOverview.mockResolvedValue({
      trendStepSeconds: 60,
      services: [
        {
          service: "api",
          requestCount: 180n,
          ratePerSecond: 1.5,
          errorCount: 6n,
          errorRate: 6 / 180,
          p50Ms: 10,
          p95Ms: 40,
          p99Ms: 90,
          errorLogCount: 3n,
          requestTrend: [{ ...t(0), value: 120 }, { ...t(1), value: 60 }, { ...t(2), value: 0 }],
          errorTrend: [{ ...t(1), value: 6 }],
          source: "",
          operatorData: false,
        },
      ],
    });
    const { services } = await getServiceOverview({ ...env, window });
    expect(services[0]).toMatchObject({ requestCount: 180, errorCount: 6, errorLogCount: 3 });
    expect(services[0].rateSeries).toEqual([2, 1, 0]);
    expect(services[0].errorRateSeries).toEqual([0, 0.1, null]);
  });
});

describe("traces and panels", () => {
  it("sends ListTraces filters only when set, and reads the root span's duration", async () => {
    client.listTraces.mockResolvedValue({
      traces: [{ traceId: "t", startTime: ts(window.startMs), durationMs: 300, rootDurationMs: 120, rootService: "api", rootSpanName: "op", spanCount: 3, errorCount: 0, services: ["api"], source: "", operatorData: false }],
      nextPageToken: "",
      redGroups: [],
    });
    const result = await listTraces({ ...env, window, rootSpanName: "", services: [], errorsOnly: false, minDurationMs: 0, limit: 50 });
    const req = client.listTraces.mock.calls[0][0];
    expect(req.errorsOnly).toBeUndefined();
    expect(req.minDurationMs).toBeUndefined();
    expect(req.rootSpanName).toBeUndefined();
    expect(result.traces[0]).toMatchObject({ durationMs: 300, rootDurationMs: 120 });
    expect(result.red).toBeNull();
  });

  it("sends a panel to QueryPanel as-is, with the service filter as scope, and maps lines, numbers and rows", async () => {
    client.queryPanel.mockResolvedValue({
      panelId: "p",
      kind: "line",
      unit: "percent",
      stepSeconds: 60,
      truncated: false,
      columns: ["requests"],
      rows: [{ group: { "span.name": "a/B" }, values: [12] }],
      series: [{ label: "ratio", group: { "service.name": "api" }, points: [{ time: ts(window.startMs), value: 0.02 }], value: 0 }],
      funnelSteps: [],
      issues: [],
      sessions: [],
    });
    const panel = {
      id: "p",
      title: "Error rate",
      kind: "line",
      signal: "traces",
      ratio: true,
      series: [
        { label: "errors", agg: "count", where: [{ field: "span.status", op: "eq", value: "error" }] },
        { label: "requests", agg: "count" },
      ],
      groupBy: ["service.name"],
      unit: "percent" as const,
    };
    const result = await queryPanel({ ...env, window, panel, services: ["api"], stepSeconds: 0 });
    const req = client.queryPanel.mock.calls[0][0];
    expect(req.scope).toMatchObject({ orgId: "org-1", envIds: ["env-1"], services: ["api"] });
    expect(req.panel).toMatchObject({ id: "p", ratio: true, groupBy: ["service.name"], series: [{ agg: "count", where: [{ field: "span.status", op: "eq", value: "error", values: [] }] }, { label: "requests" }] });
    expect(result.series[0]).toMatchObject({ label: "ratio", group: { "service.name": "api" }, timestamps: [window.startMs / 1000], values: [0.02] });
    expect(result.rows[0]).toEqual({ group: { "span.name": "a/B" }, values: [12] });
    expect(result.columns).toEqual(["requests"]);
  });

  it("preserves SQL numeric column metadata separately from string group attributes", async () => {
    client.queryPanel.mockResolvedValue({
      panelId: "event-mix", kind: "sql", unit: "", stepSeconds: 0, truncated: true,
      columns: ["events", "users"],
      rows: [{ group: { event: "page_visited", clients: "electron,web" }, values: [120, Number.NaN] }],
      series: [], funnelSteps: [], issues: [], sessions: [],
    });
    const result = await queryPanel({
      ...env, window, services: [], stepSeconds: 0,
      panel: { id: "event-mix", title: "Event mix", kind: "sql", signal: "traces", series: [], sql: "SELECT EventName AS event, count() AS events, uniqExact(UserId) AS users, arrayStringConcat(groupUniqArray(Client), ',') AS clients FROM product_events GROUP BY EventName" },
    });
    expect(result).toMatchObject({
      kind: "sql", truncated: true, columns: ["events", "users"], series: [],
      rows: [{ group: { event: "page_visited", clients: "electron,web" }, values: [120, null] }],
    });
    expect(client.queryPanel.mock.calls[0][0].panel).toMatchObject({ kind: "sql", series: [], groupBy: [], sql: expect.stringContaining("AS clients") });
  });

  it("preserves a SQL NULL placeholder separately from a measured zero", async () => {
    client.queryPanel.mockResolvedValue({ panelId: "nullable", kind: "sql", unit: "", stepSeconds: 0, truncated: false,
      columns: ["unmeasured", "actual_zero", "count"], rows: [{ group: {}, values: [0, 0, 12], missingValueIndexes: [0] }],
      series: [], funnelSteps: [], issues: [], sessions: [],
    });
    const result = await queryPanel({ ...env, window, services: [], stepSeconds: 0,
      panel: { id: "nullable", title: "Nullable", kind: "sql", signal: "", series: [], sql: "SELECT NULL, 0, 12" },
    });
    expect(result.rows[0].values).toEqual([null, 0, 12]);
  });

  it("sends a funnel's steps and a sessions panel's has-error, and maps the browser kinds back (C7)", async () => {
    client.queryPanel.mockResolvedValue({
      panelId: "f",
      kind: "funnel",
      unit: "",
      stepSeconds: 0,
      truncated: false,
      columns: [],
      rows: [],
      series: [],
      funnelSteps: [{ event: "view", count: 10n, rateFromFirst: 1, rateFromPrevious: 1 }, { event: "buy", count: 4n, rateFromFirst: 0.4, rateFromPrevious: 0.4 }],
      issues: [{ serviceName: "web", stackHash: "18446744073709551615", exceptionType: "TypeError", message: "x", urlPath: "/c", serviceVersion: "b1", events: 3n, sessions: 2n, firstSeen: ts(window.startMs), lastSeen: ts(window.endMs), sampleTraceId: "t", sampleSessionId: "s" }],
      sessions: [{ sessionId: "s", serviceName: "web", serviceVersion: "b1", envId: "env-1", userId: "", entryPath: "/", start: ts(window.startMs), end: ts(window.endMs), pageViews: 5n, interactions: 9n, errors: 1n, hasReplay: true }],
    });
    const result = await queryPanel({
      ...env,
      window,
      panel: { id: "f", title: "Funnel", kind: "funnel", signal: "traces", series: [], funnel: { steps: ["view", "buy"], window: "1h" } },
      services: [],
      stepSeconds: 0,
    });
    expect(client.queryPanel.mock.calls[0][0].panel).toMatchObject({ kind: "funnel", funnel: { steps: ["view", "buy"], window: "1h", by: "" }, hasError: false });
    expect(result.funnelSteps).toEqual([
      { event: "view", count: 10, rateFromFirst: 1, rateFromPrevious: 1 },
      { event: "buy", count: 4, rateFromFirst: 0.4, rateFromPrevious: 0.4 },
    ]);
    expect(result.issues[0]).toMatchObject({ stackHash: "18446744073709551615", events: 3, sessions: 2, firstSeenMs: window.startMs, lastSeenMs: window.endMs });
    expect(result.sessions[0]).toMatchObject({ pageViews: 5, errors: 1, hasReplay: true, startMs: window.startMs });
  });
});

describe("errors", () => {
  it("reads FailedPrecondition as 'not set up' and never retries an answer the server meant", () => {
    const notSetUp = new ConnectError("not set up", Code.FailedPrecondition);
    expect(obsErrorKind(notSetUp)).toBe("not-set-up");
    expect(obsRetry(0, notSetUp)).toBe(false);
    // Only the hourly allowance is "over-quota"; any other ResourceExhausted is "busy" (quota.test.ts).
    expect(obsErrorKind(new ConnectError("quota", Code.ResourceExhausted))).toBe("busy");
    expect(obsErrorKind(new ConnectError("this organization's hourly observability query quota is used up", Code.ResourceExhausted))).toBe("over-quota");
    expect(obsErrorKind(new ConnectError("slow", Code.DeadlineExceeded))).toBe("too-broad");
    expect(obsRetry(0, new ConnectError("down", Code.Unavailable))).toBe(true);
    expect(obsRetry(1, new ConnectError("down", Code.Unavailable))).toBe(false);
  });
});

describe("query scope v2 (contract K1)", () => {
  it("sends the project, every env id and the attribute filters as data, with `in` and `exists` shaped for the server", () => {
    const scope = wireScope(
      {
        orgId: "org-1",
        projectId: "shop",
        envIds: ["env-1", "env-2"],
        filters: [
          { key: "service.version", op: "in", values: ["v1", "v2"] },
          { key: "http.route", op: "contains", value: "orders" },
          { key: "k8s.container.name", op: "exists", value: "ignored" },
          { key: "status", op: "neq", value: "error" },
        ],
      },
      window,
      ["api"]
    );
    expect(scope).toMatchObject({ orgId: "org-1", projectId: "shop", envIds: ["env-1", "env-2"], services: ["api"] });
    expect(scope.filters).toEqual([
      { key: "service.version", op: AttributeOp.IN, value: "", values: ["v1", "v2"] },
      { key: "http.route", op: AttributeOp.CONTAINS, value: "orders", values: [] },
      { key: "k8s.container.name", op: AttributeOp.EXISTS, value: "", values: [] },
      { key: "status", op: AttributeOp.NEQ, value: "error", values: [] },
    ]);
  });

  it("leaves project_id unset rather than empty: empty would mean the project-less environments only", () => {
    expect(wireScope({ orgId: "o", envIds: [], filters: [] }, window).projectId).toBeUndefined();
    expect(wireScope({ orgId: "o", projectId: "", envIds: [], filters: [] }, window).projectId).toBeUndefined();
  });

  it("passes group_by to the overview, the trace list and a panel, and reads the groups back", async () => {
    client.getServiceOverview.mockResolvedValue({
      trendStepSeconds: 60,
      services: [{ service: "api", group: { "deployment.environment.name": "prod" }, requestCount: 1n, ratePerSecond: 0, errorCount: 0n, errorRate: 0, p50Ms: 0, p95Ms: 0, p99Ms: 0, errorLogCount: 0n, requestTrend: [], errorTrend: [], source: "", operatorData: false }],
    });
    const overview = await getServiceOverview({ ...env, window, groupBy: ["deployment.environment.name"] });
    expect(client.getServiceOverview.mock.calls[0][0].groupBy).toEqual(["deployment.environment.name"]);
    expect(overview.services[0].group).toEqual({ "deployment.environment.name": "prod" });

    const red = { traceCount: 10n, errorTraceCount: 1n, ratePerSecond: 0.5, errorRate: 0.1, p50Ms: 1, p95Ms: 2, p99Ms: 3 };
    client.listTraces.mockResolvedValue({ traces: [], nextPageToken: "", red, redGroups: [{ group: { "service.version": "v2" }, red }, { group: {} }] });
    const traces = await listTraces({ ...env, window, rootSpanName: "", services: [], errorsOnly: false, minDurationMs: 0, limit: 50, groupBy: ["service.version"] });
    expect(client.listTraces.mock.calls[0][0].groupBy).toEqual(["service.version"]);
    // A group without a summary is dropped, not drawn as zeros.
    expect(traces.redGroups).toEqual([{ group: { "service.version": "v2" }, red: expect.objectContaining({ traceCount: 10, errorRate: 0.1 }) }]);

    client.queryPanel.mockResolvedValue({ panelId: "p", kind: "line", unit: "", stepSeconds: 60, truncated: false, columns: [], rows: [], series: [], funnelSteps: [], issues: [], sessions: [] });
    const panel = { id: "p", title: "t", kind: "line", signal: "traces", series: [{ label: "n", agg: "count" }], groupBy: ["service.name"] };
    await queryPanel({ ...env, window, panel, services: [], stepSeconds: 0, groupBy: ["deployment.environment.name", "service.version"] });
    const req = client.queryPanel.mock.calls[0][0];
    // The override rides beside the panel; the panel's own group-by is sent unchanged.
    expect(req.groupBy).toEqual(["deployment.environment.name", "service.version"]);
    expect(req.panel.groupBy).toEqual(["service.name"]);
  });

  it("asks ListAttributeValues for a key's values with the prefix and signal", async () => {
    client.listAttributeValues.mockResolvedValue({ values: [{ value: "v1.8.3", count: 42n }], truncated: true });
    const result = await listAttributeValues({ ...env, window, key: "service.version", prefix: "v1", signal: "traces" });
    expect(client.listAttributeValues.mock.calls[0][0]).toMatchObject({ key: "service.version", prefix: "v1", signal: Signal.TRACES, limit: 50 });
    expect(result).toEqual({ values: [{ value: "v1.8.3", count: 42 }], truncated: true });
    await listAttributeValues({ ...env, window, key: "service.name", prefix: "" });
    expect(client.listAttributeValues.mock.calls[1][0]).toMatchObject({ prefix: undefined, signal: undefined });
  });
});

describe("workloads (customer metadata, contract K2)", () => {
  it("maps a workload, keeping an unset limit or request as null rather than zero", async () => {
    client.listWorkloads.mockResolvedValue({
      truncated: false,
      workloads: [
        {
          envId: "env-1",
          envName: "prod",
          workload: "api",
          namespace: "shop",
          serviceVersion: "v1.8.3",
          replicasObserved: 3,
          restartsInWindow: 4n,
          latestWarning: { time: ts(window.endMs - 60_000), envId: "env-1", type: "Warning", reason: "BackOff", message: "m", count: 7n, objectKind: "Pod", objectName: "api-1", container: "api" },
          containers: [
            {
              container: "api",
              imageName: "ghcr.io/acme/api",
              imageTag: "v1.8.3",
              restartsInWindow: 4n,
              lastTerminatedReason: "OOMKilled",
              lastTerminatedAt: ts(window.endMs - 120_000),
              cpu: { latest: 0.4, p95: 0.6, limitUtilizationLatest: 0.4, limit: 1 },
              memory: { latest: 500, p95: 510 },
            },
          ],
          pods: [],
        },
      ],
      jobs: [],
      jobsTruncated: false,
    });
    const { workloads } = await listWorkloads({ ...env, window });
    const [w] = workloads;
    expect(w).toMatchObject({ envName: "prod", restartsInWindow: 4, latestWarning: { reason: "BackOff", count: 7 } });
    expect(w.containers[0]).toMatchObject({ lastTerminatedReason: "OOMKilled", lastTerminatedAtMs: window.endMs - 120_000 });
    expect(w.containers[0].cpu).toEqual({
      latest: 0.4,
      p95: 0.6,
      limitUtilizationLatest: 0.4,
      limitUtilizationP95: null,
      requestUtilizationLatest: null,
      requestUtilizationP95: null,
      limit: 1,
      request: null,
    });
    expect(w.containers[0].memory?.limit).toBeNull();
  });

  it("maps a workload's series by metric and drops a metric this build does not know", async () => {
    client.getWorkload.mockResolvedValue({
      stepSeconds: 60,
      truncated: false,
      series: [
        { envId: "env-1", container: "api", metric: WorkloadMetric.MEMORY_LIMIT_UTILIZATION, unit: "1", points: [{ time: ts(window.startMs), value: 0.9 }] },
        { envId: "env-1", container: "api", metric: 99, unit: "?", points: [] },
      ],
      events: [],
      imageChanges: [],
      versionChanges: [{ envId: "env-1", serviceVersion: "v2", firstSeen: ts(window.startMs), lastSeen: ts(window.endMs) }],
      workloads: [],
    });
    const detail = await getWorkload({ ...env, window, workload: "api" });
    expect(client.getWorkload.mock.calls[0][0]).toMatchObject({ workload: "api", container: "", stepSeconds: 0 });
    expect(detail.series).toEqual([
      { envId: "env-1", container: "api", metric: "memory_limit_utilization", unit: "1", timestamps: [window.startMs / 1000], values: [0.9] },
    ]);
    expect(detail.versionChanges[0]).toEqual({ envId: "env-1", serviceVersion: "v2", firstSeenMs: window.startMs, lastSeenMs: window.endMs });
  });
});

describe("workloads: the server's verdict, pods, jobs and the declaration", () => {
  const wire = (over: Record<string, unknown> = {}) => ({
    envId: "env-1",
    envName: "prod",
    workload: "api",
    namespace: "shop",
    serviceVersion: "",
    replicasObserved: 1,
    restartsInWindow: 14n,
    containers: [],
    kind: WorkloadKind.STATEFUL_SET,
    health: WorkloadHealth.CRASH_LOOPING,
    healthReason: "restarted 14x, last exit OOMKilled",
    readyReplicas: 0,
    desiredReplicas: 2,
    releaseVersion: "v1.7.44",
    releaseBasis: WorkloadReleaseBasis.PROMOTION,
    pods: [],
    ...over,
  });

  it("maps kind, the health verdict and its reason, replicas and the release with its basis", async () => {
    client.listWorkloads.mockResolvedValue({ truncated: false, workloads: [wire(), wire({ health: 42, kind: 0, readyReplicas: undefined, desiredReplicas: undefined, releaseVersion: "", releaseBasis: WorkloadReleaseBasis.DIGEST })], jobs: [], jobsTruncated: false });
    const { workloads } = await listWorkloads({ ...env, window });
    expect(workloads[0]).toMatchObject({
      kind: "statefulset",
      health: "crash_looping",
      healthReason: "restarted 14x, last exit OOMKilled",
      readyReplicas: 0,
      desiredReplicas: 2,
      releaseVersion: "v1.7.44",
      releaseBasis: "promotion",
      pods: [],
      declared: null,
    });
    // A verdict this build does not know reads as unknown; unset replicas stay null, not 0;
    // no release means no basis, whatever the wire says.
    expect(workloads[1]).toMatchObject({ kind: "unknown", health: "unknown", readyReplicas: null, desiredReplicas: null, releaseBasis: "none" });
  });

  it("maps Job runs, keeping a running one's finish and duration unknown", async () => {
    client.listWorkloads.mockResolvedValue({
      truncated: false,
      workloads: [],
      jobsTruncated: true,
      jobs: [
        { envId: "env-1", envName: "prod", name: "cp-migrate-b9e1", workload: "cp-migrate", result: WorkloadJobResult.FAILED, reason: "BackoffLimitExceeded", releaseVersion: "v1.7.44", startedAt: ts(window.startMs), finishedAt: ts(window.startMs + 41_000), durationSeconds: 41n },
        { envId: "env-1", envName: "prod", name: "cp-provision-1", workload: "", result: WorkloadJobResult.RUNNING, reason: "", releaseVersion: "", startedAt: ts(window.endMs), durationSeconds: 0n },
      ],
    });
    const res = await listWorkloads({ ...env, window });
    expect(res.jobsTruncated).toBe(true);
    expect(res.jobs[0]).toEqual({
      envId: "env-1",
      envName: "prod",
      name: "cp-migrate-b9e1",
      workload: "cp-migrate",
      result: "failed",
      reason: "BackoffLimitExceeded",
      releaseVersion: "v1.7.44",
      startedAtMs: window.startMs,
      finishedAtMs: window.startMs + 41_000,
      durationSeconds: 41,
    });
    // A run with no declared workload name is filed under its own name.
    expect(res.jobs[1]).toMatchObject({ workload: "cp-provision-1", result: "running", finishedAtMs: null, durationSeconds: 0 });
  });

  it("maps GetWorkload's summaries: pods (an unreported node stays empty) and the declared spec, by name only", async () => {
    client.getWorkload.mockResolvedValue({
      stepSeconds: 60,
      truncated: false,
      series: [],
      events: [],
      imageChanges: [],
      versionChanges: [],
      workloads: [
        wire({
          pods: [
            { name: "api-1", uid: "u1", state: WorkloadPodState.RUNNING, restarts: 14n, lastExitReason: "OOMKilled", lastExitAt: ts(window.endMs - 60_000), node: "", firstSeen: ts(window.startMs), lastSeen: ts(window.endMs), ageIsLowerBound: true },
            { name: "api-0", uid: "u0", state: WorkloadPodState.GONE, restarts: 0n, lastExitReason: "", node: "node-a", ageIsLowerBound: false },
          ],
          declared: {
            name: "api",
            kind: WorkloadKind.STATEFUL_SET,
            specRecorded: true,
            replicas: 2,
            cpuRequestMillicores: 250n,
            cpuLimitMillicores: 1000n,
            memoryRequestBytes: 268435456n,
            memoryLimitBytes: 1073741824n,
            ports: [{ name: "http", port: 8080, protocol: "tcp" }],
            envNames: ["DATABASE_URL", "LOG_LEVEL"],
            image: "ghcr.io/acme/api@sha256:abc",
            sourcePath: "deploy/kcl/prod/main.k",
            sourceRepo: "https://github.com/acme/shop",
            sourceCommit: "992bb09",
            bundleId: "b1",
          },
        }),
      ],
    });
    const detail = await getWorkload({ ...env, window, workload: "api" });
    const [w] = detail.workloads;
    expect(w.pods[0]).toMatchObject({ state: "running", restarts: 14, lastExitReason: "OOMKilled", node: "", firstSeenMs: window.startMs, ageIsLowerBound: true });
    expect(w.pods[1]).toMatchObject({ state: "gone", lastExitAtMs: null, firstSeenMs: null, node: "node-a" });
    expect(w.declared).toEqual({
      name: "api",
      kind: "statefulset",
      specRecorded: true,
      replicas: 2,
      cpuRequestMillicores: 250,
      cpuLimitMillicores: 1000,
      memoryRequestBytes: 268435456,
      memoryLimitBytes: 1073741824,
      ports: [{ name: "http", port: 8080, protocol: "tcp" }],
      envNames: ["DATABASE_URL", "LOG_LEVEL"],
      image: "ghcr.io/acme/api@sha256:abc",
      sourcePath: "deploy/kcl/prod/main.k",
      sourceRepo: "https://github.com/acme/shop",
      sourceCommit: "992bb09",
      bundleId: "b1",
    });
  });
});
