// Copyright (c) 2025 Reliant Labs

/**
 * The metrics explorer's wire mapping: the catalog, the metric detail,
 * exemplars, and the new QueryPanel fields (series_meta, series_total,
 * warnings, partial and null points).
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";

import { MetricTemporality, MetricType } from "@/gen/controlplane/services/observability/v1/observability_pb";

import { msOf, queryPanel } from "../api";
import { catalogGroup } from "../metricsTypes";
import { getMetric, listMetricCatalog, listMetricExemplars } from "../metricsApi";

const client = { listMetrics: vi.fn(), getMetric: vi.fn(), listMetricExemplars: vi.fn(), queryPanel: vi.fn() };
vi.mock("../../client", () => ({ getControlPlaneClient: () => client }));

const env = { orgId: "org-1", envIds: ["env-1"], filters: [] };
const window = { startMs: Date.UTC(2026, 9, 12, 6), endMs: Date.UTC(2026, 9, 12, 7) };
const ts = (ms: number) => create(TimestampSchema, { seconds: BigInt(Math.floor(ms / 1000)), nanos: (ms % 1000) * 1e6 });

beforeEach(() => vi.clearAllMocks());

const wireInfo = (over: Record<string, unknown> = {}) => ({
  name: "orders.created",
  type: MetricType.SUM,
  unit: "{order}",
  description: "Orders accepted.",
  monotonic: true,
  temporality: MetricTemporality.CUMULATIVE,
  services: ["api", "worker"],
  serviceCount: 3,
  lastSeen: ts(window.endMs - 60_000),
  seriesEstimate: 1204n,
  queryable: true,
  builtin: false,
  declared: true,
  seen: true,
  stability: "stable",
  declaredAttributes: [],
  ...over,
});

describe("the catalog", () => {
  it("scopes ListMetrics, sends the name filter and limit, and maps every catalog field", async () => {
    client.listMetrics.mockResolvedValue({ metrics: [wireInfo()], truncated: true });
    const res = await listMetricCatalog({ ...env, window, name: "orders", limit: 50 });
    const req = client.listMetrics.mock.calls[0][0];
    expect(req.scope).toMatchObject({ orgId: "org-1", envIds: ["env-1"] });
    expect(msOf(req.scope.timeRange.end)).toBe(window.endMs);
    expect(req).toMatchObject({ nameContains: "orders", limit: 50 });
    expect(res.truncated).toBe(true);
    expect(res.metrics[0]).toEqual({
      name: "orders.created",
      kind: "sum",
      unit: "{order}",
      description: "Orders accepted.",
      monotonic: true,
      temporality: "cumulative",
      services: ["api", "worker"],
      serviceCount: 3,
      lastSeenMs: window.endMs - 60_000,
      seriesEstimate: 1204,
      queryable: true,
      builtin: false,
      declared: true,
      seen: true,
      stability: "stable",
      group: "declared",
    });
  });

  it("sends no name filter when none is typed", async () => {
    client.listMetrics.mockResolvedValue({ metrics: [], truncated: false });
    await listMetricCatalog({ ...env, window });
    expect(client.listMetrics.mock.calls[0][0]).toMatchObject({ nameContains: undefined, limit: 0 });
  });

  it.each([
    [MetricType.GAUGE, "gauge"],
    [MetricType.SUM, "sum"],
    [MetricType.HISTOGRAM, "histogram"],
    [MetricType.EXPONENTIAL_HISTOGRAM, "exp_histogram"],
    [MetricType.SUMMARY, "summary"],
    [MetricType.UNSPECIFIED, "unknown"],
    [99 as MetricType, "unknown"],
  ])("reads wire kind %s as %s", async (type, kind) => {
    client.listMetrics.mockResolvedValue({ metrics: [wireInfo({ type })], truncated: false });
    expect((await listMetricCatalog({ ...env, window })).metrics[0]!.kind).toBe(kind);
  });

  it.each([
    [MetricTemporality.DELTA, "delta"],
    [MetricTemporality.CUMULATIVE, "cumulative"],
    [MetricTemporality.UNSPECIFIED, "unknown"],
    [42 as MetricTemporality, "unknown"],
  ])("reads wire temporality %s as %s", async (temporality, want) => {
    client.listMetrics.mockResolvedValue({ metrics: [wireInfo({ temporality })], truncated: false });
    expect((await listMetricCatalog({ ...env, window })).metrics[0]!.temporality).toBe(want);
  });

  it("maps a declared metric never seen: no last-seen time, no series", async () => {
    client.listMetrics.mockResolvedValue({ metrics: [wireInfo({ lastSeen: undefined, seen: false, seriesEstimate: 0n })], truncated: false });
    expect((await listMetricCatalog({ ...env, window })).metrics[0]).toMatchObject({ lastSeenMs: null, seen: false, seriesEstimate: 0, declared: true });
  });
});

describe("catalogGroup", () => {
  it.each([
    ["rpc.server.duration", true, "runtime"],
    ["rpc.server.duration", false, "runtime"],
    ["go.memory.used", false, "runtime"],
    ["forge.server.drain.duration", true, "runtime"],
    ["orders.created", true, "declared"],
    ["orders.created", false, "other"],
    ["k8s.container.cpu.usage", false, "infra"],
    ["container.cpu.time", false, "infra"],
    ["rpcx.thing", false, "other"],
    ["go", false, "other"],
    ["", false, "other"],
  ] as const)("%s (declared=%s) is %s", (name, declared, group) => {
    expect(catalogGroup(name, declared)).toBe(group);
  });
});

describe("the metric detail", () => {
  it("asks for the metric by name and kind, and maps attributes with int64 estimates", async () => {
    client.getMetric.mockResolvedValue({
      info: wireInfo({ type: MetricType.HISTOGRAM, name: "rpc.server.duration" }),
      intervalSeconds: 60,
      mixedTemporality: true,
      attributes: [
        { key: "service.name", resource: true, distinctEstimate: 3n, enumerated: [], highCardinality: false },
        { key: "status", resource: false, distinctEstimate: 3n, enumerated: ["ok", "error"], highCardinality: false },
        { key: "user.id", resource: false, distinctEstimate: 4200n, enumerated: [], highCardinality: true },
      ],
      suggestedBreakdowns: ["status", "service.name"],
    });
    const res = await getMetric({ ...env, window, name: "rpc.server.duration", kind: "histogram" });
    expect(client.getMetric.mock.calls[0][0]).toMatchObject({ metricName: "rpc.server.duration", metricType: MetricType.HISTOGRAM });
    expect(res.info).toMatchObject({ name: "rpc.server.duration", kind: "histogram", group: "runtime" });
    expect(res).toMatchObject({ intervalSeconds: 60, mixedTemporality: true, suggestedBreakdowns: ["status", "service.name"] });
    expect(res.attributes[2]).toEqual({ key: "user.id", resource: false, distinctEstimate: 4200, enumerated: [], highCardinality: true });
    expect(res.attributes[1]!.enumerated).toEqual(["ok", "error"]);
  });

  it("leaves the kind unset so the server looks it up from the org's data", async () => {
    client.getMetric.mockResolvedValue({ info: wireInfo(), intervalSeconds: 0, mixedTemporality: false, attributes: [], suggestedBreakdowns: [] });
    await getMetric({ ...env, window, name: "orders.created" });
    expect(client.getMetric.mock.calls[0][0].metricType).toBeUndefined();
  });
});

describe("exemplars", () => {
  it("sends the metric, filters, group, bucket and floor, and maps trace_stored", async () => {
    client.listMetricExemplars.mockResolvedValue({
      exemplars: [
        { time: ts(window.endMs - 5 * 60_000), value: 1.25, traceId: "abc", spanId: "def", attributes: { "rpc.method": "Checkout" }, traceStored: true },
        { time: ts(window.endMs - 6 * 60_000), value: 0.75, traceId: "123", spanId: "456", attributes: {}, traceStored: false },
      ],
    });
    const bucket = { startMs: window.endMs - 10 * 60_000, endMs: window.endMs - 9 * 60_000 };
    const res = await listMetricExemplars({
      ...env,
      window,
      name: "rpc.server.duration",
      kind: "histogram",
      where: [{ field: "metric.attr.status", op: "eq", value: "ok" }],
      group: { "service.name": "api" },
      range: bucket,
      minValue: 0.5,
      limit: 20,
    });
    const req = client.listMetricExemplars.mock.calls[0][0];
    expect(req).toMatchObject({
      metricName: "rpc.server.duration",
      metricType: MetricType.HISTOGRAM,
      where: [{ field: "metric.attr.status", op: "eq", value: "ok", values: [] }],
      group: { "service.name": "api" },
      minValue: 0.5,
      limit: 20,
    });
    expect(msOf(req.timeRange.start)).toBe(bucket.startMs);
    expect(msOf(req.timeRange.end)).toBe(bucket.endMs);
    expect(res).toEqual([
      { timeMs: window.endMs - 5 * 60_000, value: 1.25, traceId: "abc", spanId: "def", attributes: { "rpc.method": "Checkout" }, traceStored: true },
      { timeMs: window.endMs - 6 * 60_000, value: 0.75, traceId: "123", spanId: "456", attributes: {}, traceStored: false },
    ]);
  });

  it("leaves the bucket unset to read the whole window", async () => {
    client.listMetricExemplars.mockResolvedValue({ exemplars: [] });
    await listMetricExemplars({ ...env, window, name: "m" });
    const req = client.listMetricExemplars.mock.calls[0][0];
    expect(req.timeRange).toBeUndefined();
    expect(req.minValue).toBeUndefined();
  });
});

/** What a decoded QueryPanelResponse always carries: every repeated field is present, if empty. */
const EMPTY_PANEL = { kind: "", unit: "", stepSeconds: 0, truncated: false, columns: [], rows: [], series: [], funnelSteps: [], issues: [], sessions: [], seriesMeta: [], seriesTotal: 0, warnings: [], compiledSql: [] };

describe("QueryPanel's metrics fields", () => {
  const panel = {
    id: "p",
    title: "Latency",
    kind: "line",
    signal: "metrics",
    formula: "A / B",
    limit: 5,
    series: [
      { label: "A", agg: "quantile", quantile: 0.95, field: "rpc.server.duration", metricType: "histogram" },
      { label: "B", agg: "rate", field: "rpc.server.calls", metricType: "sum" },
    ],
  };

  it("sends the formula, limit and explain flag", async () => {
    client.queryPanel.mockResolvedValue({ ...EMPTY_PANEL, kind: "line" });
    await queryPanel({ ...env, window, panel, services: [], stepSeconds: 0, explain: true });
    const req = client.queryPanel.mock.calls[0][0];
    expect(req.panel).toMatchObject({ formula: "A / B", limit: 5, sql: "" });
    expect(req.explain).toBe(true);
  });

  it("maps series_meta, series_total, warnings, partial points and null gaps", async () => {
    client.queryPanel.mockResolvedValue({
      panelId: "p",
      kind: "line",
      unit: "",
      stepSeconds: 60,
      truncated: true,
      columns: [],
      rows: [],
      series: [
        {
          label: "A",
          group: {},
          points: [
            { time: ts(window.startMs), value: 0, partial: false, null: true },
            { time: ts(window.startMs + 60_000), value: 0.4, partial: false, null: false },
            { time: ts(window.startMs + 120_000), value: 0.5, partial: true, null: false },
          ],
          value: 0,
        },
      ],
      funnelSteps: [],
      issues: [],
      sessions: [],
      seriesMeta: [
        { label: "A", metric: "rpc.server.duration", metricType: MetricType.HISTOGRAM, monotonic: false, temporality: MetricTemporality.CUMULATIVE, aggregation: "quantile", quantile: 0.95, valueUnit: "s" },
        { label: "formula", metric: "", metricType: MetricType.UNSPECIFIED, monotonic: false, temporality: MetricTemporality.UNSPECIFIED, aggregation: "", quantile: 0, valueUnit: "" },
        { label: "B", metric: "rpc.server.calls", metricType: MetricType.SUM, monotonic: true, temporality: MetricTemporality.CUMULATIVE, aggregation: "rate", quantile: 0, valueUnit: "{call}/s" },
      ],
      seriesTotal: 1204,
      warnings: ["mixed_bounds", "step_raised"],
      compiledSql: [{ sql: "SELECT 1 WHERE x = {a:String}", params: { a: "b" } }],
    });
    const res = await queryPanel({ ...env, window, panel, services: [], stepSeconds: 0 });
    expect(res.series[0]!.values).toEqual([null, 0.4, 0.5]);
    expect(res.series[0]!.partial).toEqual([false, false, true]);
    expect(res.seriesTotal).toBe(1204);
    expect(res.warnings).toEqual(["mixed_bounds", "step_raised"]);
    expect(res.seriesMeta).toEqual([
      { label: "A", metric: "rpc.server.duration", kind: "histogram", agg: "quantile", quantile: 0.95, valueUnit: "s" },
      { label: "formula", metric: "", kind: "unknown", agg: "", quantile: undefined, valueUnit: "" },
      { label: "B", metric: "rpc.server.calls", kind: "sum", agg: "rate", quantile: undefined, valueUnit: "{call}/s" },
    ]);
    expect(res.compiledSql).toEqual([{ sql: "SELECT 1 WHERE x = {a:String}", params: { a: "b" } }]);
  });

  it("gives a non-metrics answer empty metadata and no partial flags", async () => {
    client.queryPanel.mockResolvedValue({
      ...EMPTY_PANEL,
      kind: "line",
      series: [{ label: "x", group: {}, points: [{ time: ts(window.startMs), value: 1, partial: false, null: false }], value: 0 }],
    });
    const res = await queryPanel({ ...env, window, panel: { ...panel, signal: "traces" }, services: [], stepSeconds: 0 });
    expect(res).toMatchObject({ seriesMeta: [], seriesTotal: 0, warnings: [], compiledSql: [] });
    expect(res.series[0]!.partial).toBeUndefined();
  });
});
