// Copyright (c) 2025 Reliant Labs

/**
 * The wire mapping: what api.ts sends control-plane and how it reads the
 * answer back into the console's model.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

import { SortOrder } from "@/gen/controlplane/services/observability/v1/observability_pb";

import { composeLogQuery, getServiceOverview, getTrace, listTraces, msOf, queryPanel, searchLogs, severityOf, statusOf } from "../api";
import { obsErrorKind, obsRetry } from "../errors";

const client = {
  searchLogs: vi.fn(),
  listTraces: vi.fn(),
  queryPanel: vi.fn(),
  getTrace: vi.fn(),
  getServiceOverview: vi.fn(),
};

vi.mock("../../client", () => ({ getControlPlaneClient: () => client }));

const env = { orgId: "org-1", environmentId: "env-1" };
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
          links: [],
          source: "",
          operatorData: false,
        },
      ],
    });
    const trace = await getTrace({ ...env, traceId: "t", window });
    expect(trace.spans[0]).toMatchObject({ kind: "server", status: "error", durationMs: 0.042 });
    expect(trace.spans[0].startMs - window.startMs).toBeCloseTo(0.25, 6);
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
  });
});

describe("errors", () => {
  it("reads FailedPrecondition as 'not set up' and never retries an answer the server meant", () => {
    const notSetUp = new ConnectError("not set up", Code.FailedPrecondition);
    expect(obsErrorKind(notSetUp)).toBe("not-set-up");
    expect(obsRetry(0, notSetUp)).toBe(false);
    expect(obsErrorKind(new ConnectError("quota", Code.ResourceExhausted))).toBe("too-broad");
    expect(obsRetry(0, new ConnectError("down", Code.Unavailable))).toBe(true);
    expect(obsRetry(1, new ConnectError("down", Code.Unavailable))).toBe(false);
  });
});
