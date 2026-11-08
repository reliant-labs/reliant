// Copyright (c) 2025 Reliant Labs

/**
 * The demo's busy app (busyApp.ts): shop's telemetry at a real app's volume,
 * deterministic, with the story's shape — payments failing after v1.8.3, the
 * toured trace intact, multi-service traces of 10–40 spans, logs linked to
 * their traces, pages and filters that work.
 */

import { describe, expect, it } from "vitest";

import { FIXTURE_TRACE_ID } from "../../../Observability/__fixtures__/fixtureApi";
import { BUSY_SERVICES, createDemoFixtures, DEMO_RELEASE_AGO_MS } from "../busyApp";

const NOW = Date.UTC(2026, 10, 6, 15, 0, 0);
const HOUR = 3_600_000;
const scope = { orgId: "org-fixture", projectId: "shop", envIds: [] as string[], filters: [] };
const day = { startMs: NOW - 24 * HOUR, endMs: NOW };
const hour = { startMs: NOW - HOUR, endMs: NOW };
const traces = (window: typeof day, extra: Record<string, unknown> = {}) => ({
  ...scope,
  window,
  rootSpanName: "",
  services: [],
  errorsOnly: false,
  minDurationMs: 0,
  limit: 5000,
  ...extra,
});

describe("the demo's busy app", () => {
  const api = createDemoFixtures(NOW);

  it("has hundreds of traces an hour across every shop service, with a long-tailed latency", async () => {
    const res = await api.listTraces(traces(hour));
    expect(res.traces.length).toBeGreaterThan(500);
    expect(new Set(res.traces.flatMap((t) => t.services))).toEqual(new Set([...BUSY_SERVICES]));
    expect(res.red!.p50Ms).toBeLessThan(60);
    expect(res.red!.p99Ms).toBeGreaterThan(res.red!.p50Ms * 10);
    expect(new Set(res.traces.map((t) => t.rootOperation)).size).toBeGreaterThanOrEqual(8);
  });

  it("is deterministic, and a request keeps its identity as the window moves", async () => {
    const a = await api.listTraces(traces(hour));
    const b = await createDemoFixtures(NOW).listTraces(traces(hour));
    expect(b.traces).toEqual(a.traces);
    const later = await api.listTraces(traces({ startMs: hour.startMs + 600_000, endMs: hour.endMs + 600_000 }));
    const shared = a.traces.find((t) => t.startMs > hour.startMs + 700_000 && t.traceId !== FIXTURE_TRACE_ID)!;
    expect(later.traces.find((t) => t.traceId === shared.traceId)).toEqual(shared);
  });

  it("fails on the payments path after v1.8.3 reached prod, rarely before", async () => {
    const res = await api.listTraces(traces(day));
    const releaseAt = NOW - DEMO_RELEASE_AGO_MS;
    const payments = res.traces.filter((t) => t.services.includes("payments") && t.traceId !== FIXTURE_TRACE_ID);
    const rate = (list: typeof payments) => list.filter((t) => t.errorCount > 0).length / Math.max(1, list.length);
    const before = payments.filter((t) => t.startMs < releaseAt);
    const after = payments.filter((t) => t.startMs >= releaseAt);
    expect(rate(after)).toBeGreaterThan(rate(before) * 5);
    const others = res.traces.filter((t) => !t.services.includes("payments"));
    expect(rate(others)).toBeLessThan(0.02);
  });

  it("keeps the toured trace intact and first", async () => {
    const res = await api.listTraces(traces(hour));
    expect(res.traces[0].traceId).toBe(FIXTURE_TRACE_ID);
    const toured = await api.getTrace({ ...scope, window: hour, traceId: FIXTURE_TRACE_ID });
    expect(toured.spans.map((s) => s.spanId)).toContain("e1");
  });

  it("crosses api → NATS → worker → inventory with 10–40 spans on multi-service traces", async () => {
    const res = await api.listTraces(traces(hour));
    const crossing = res.traces.filter((t) => t.services.includes("inventory") && t.services.includes("worker") && t.traceId !== FIXTURE_TRACE_ID);
    expect(crossing.length).toBeGreaterThan(20);
    for (const t of crossing.slice(0, 20)) {
      const trace = await api.getTrace({ ...scope, window: hour, traceId: t.traceId });
      expect(trace.spans.length).toBe(t.spanCount);
      expect(trace.spans.length).toBeGreaterThanOrEqual(10);
      expect(trace.spans.length).toBeLessThanOrEqual(40);
      const ids = new Set(trace.spans.map((s) => s.spanId));
      for (const s of trace.spans) if (s.parentSpanId) expect(ids.has(s.parentSpanId)).toBe(true);
      expect(trace.spans.some((s) => s.name === "nats publish orders.created")).toBe(true);
      expect(trace.spans.some((s) => s.service === "inventory" && s.kind === "server")).toBe(true);
    }
  });

  it("pages traces with a cursor, and filters by error, duration and route", async () => {
    const first = await api.listTraces(traces(day, { limit: 100 }));
    expect(first.traces.length).toBe(100);
    expect(first.nextCursor).not.toBe("");
    const second = await api.listTraces(traces(day, { limit: 100, cursor: first.nextCursor }));
    expect(second.traces[0].startMs).toBeLessThanOrEqual(first.traces[99].startMs);
    expect(new Set([...first.traces, ...second.traces].map((t) => t.traceId)).size).toBe(200);
    const errors = await api.listTraces(traces(hour, { errorsOnly: true }));
    expect(errors.traces.every((t) => t.errorCount > 0)).toBe(true);
    const slow = await api.listTraces(traces(hour, { minDurationMs: 500 }));
    expect(slow.traces.every((t) => t.durationMs >= 500)).toBe(true);
    const checkout = await api.listTraces(traces(hour, { rootSpanName: "Checkout" }));
    expect(checkout.traces.every((t) => t.rootOperation.includes("Checkout"))).toBe(true);
  });

  it("has thousands of log lines, linked to their traces, with error bursts on payments", async () => {
    const logs = { ...scope, window: hour, query: "", services: [], severities: [], limit: 100_000 };
    const all = await api.searchLogs(logs);
    expect(all.records.length).toBeGreaterThan(2_000);
    const errors = all.records.filter((r) => r.severity === "error");
    expect(errors.length).toBeGreaterThan(20);
    expect(errors.every((r) => r.service === "payments" || r.traceId === FIXTURE_TRACE_ID)).toBe(true);
    const oneTrace = all.records.find((r) => r.traceId && r.traceId !== FIXTURE_TRACE_ID)!.traceId;
    const scoped = await api.searchLogs({ ...logs, traceId: oneTrace });
    expect(scoped.records.length).toBeGreaterThan(0);
    expect(scoped.records.every((r) => r.traceId === oneTrace)).toBe(true);
    const page = await api.searchLogs({ ...logs, limit: 200 });
    expect(page.records.length).toBe(200);
    expect(page.nextCursor).not.toBe("");
  });

  it("draws every service's RED with errors and latency stepping up at the release", async () => {
    const res = await api.getServiceOverview({ ...scope, window: hour });
    expect(res.services.map((s) => s.name)).toEqual([...BUSY_SERVICES]);
    const payments = res.services.find((s) => s.name === "payments")!;
    const releaseAt = NOW - DEMO_RELEASE_AGO_MS;
    const at = payments.timestamps.findIndex((t) => t * 1000 >= releaseAt);
    const rate = (from: number, to: number) => {
      const sum = (xs: (number | null)[]) => xs.reduce<number>((a, b) => a + (b ?? 0), 0);
      return sum(payments.errorTrend.slice(from, to)) / sum(payments.requestTrend.slice(from, to));
    };
    expect(rate(at, 30)).toBeGreaterThan(rate(0, at) * 5);
  });

  it("has a profile per service", async () => {
    const req = { ...scope, window: hour, profileType: "cpu" };
    const names = await Promise.all(["api", "inventory", "worker"].map(async (service) => JSON.parse((await api.getFlamegraph({ ...req, service })).speedscopeJson).name));
    expect(names).toEqual(["api cpu", "inventory cpu", "worker cpu"]);
  });

  it("lists more issues, one regressed by v1.8.3 and resolved in v1.8.4", async () => {
    const res = await api.listIssues({ ...scope, window: day, statuses: [], limit: 50 } as never);
    expect(res.issues.length).toBeGreaterThanOrEqual(6);
    const idem = await api.getIssue({ ...scope, issueId: "iss-payments-idempotency" } as never);
    expect(idem.issue).toMatchObject({ status: "resolved", resolvedVersion: "v1.8.4", lastVersion: "v1.8.3" });
  });
});
