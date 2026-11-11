// Copyright (c) 2025 Reliant Labs

/**
 * shop's request-level telemetry (busyApp.ts): a real app's volume,
 * deterministic, with the story's one incident — CreateOrder and Checkout
 * failing on the payment provider in prod after v1.8.3, nothing failing
 * anywhere else — the toured trace intact, multi-service traces of 10–40
 * spans, logs linked to their traces, pages and filters that work, and
 * profiles that differ by service and type, in the type's unit.
 */

import { describe, expect, it } from "vitest";

import { BUSY_SERVICES, createBusyApp, DEMO_PROFILE_TYPES, DEMO_RELEASE_AGO_MS } from "../busyApp";
import { DEMO_INCIDENT, DEMO_TOURED_TRACE, demoStats } from "../shopStory";

const NOW = Date.UTC(2026, 10, 6, 15, 0, 0);
const HOUR = 3_600_000;
const TOURED = DEMO_TOURED_TRACE.traceId;
const scope = { orgId: "org-acme", projectId: "shop", envIds: [] as string[], filters: [] };
const prod = { ...scope, envIds: ["env-prod"] };
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

describe("shop's busy app", () => {
  const api = createBusyApp(NOW);

  it("has hundreds of traces an hour across every shop service, newest first, with a long-tailed latency", async () => {
    const res = await api.listTraces(traces(hour));
    expect(res.traces.length).toBeGreaterThan(500);
    expect(new Set(res.traces.flatMap((t) => t.services))).toEqual(new Set([...BUSY_SERVICES, "shop-web"]));
    const starts = res.traces.map((t) => t.startMs);
    expect(starts).toEqual([...starts].sort((a, b) => b - a));
    expect(res.red!.p50Ms).toBeLessThan(60);
    expect(res.red!.p99Ms).toBeGreaterThan(res.red!.p50Ms * 10);
    expect(new Set(res.traces.map((t) => t.rootOperation)).size).toBe(9);
  });

  it("is deterministic, and a request keeps its identity as the window moves", async () => {
    const a = await api.listTraces(traces(hour));
    const b = await createBusyApp(NOW).listTraces(traces(hour));
    expect(b.traces).toEqual(a.traces);
    const later = await createBusyApp(NOW + 600_000).listTraces(traces({ startMs: hour.startMs + 600_000, endMs: hour.endMs + 600_000 }));
    const shared = a.traces.find((t) => t.startMs > hour.startMs + 700_000 && t.startMs < NOW - DEMO_RELEASE_AGO_MS && t.traceId !== TOURED)!;
    expect(later.traces.find((t) => t.traceId === shared.traceId)).toEqual(shared);
  });

  it("fails only on prod's charging routes after v1.8.3, at the 3s deadline, with the incident's error", async () => {
    const res = await api.listTraces(traces(day));
    const releaseAt = NOW - DEMO_RELEASE_AGO_MS;
    const failed = res.traces.filter((t) => t.errorCount > 0);
    expect(failed.length).toBeGreaterThan(10);
    for (const t of failed) {
      expect(t.startMs).toBeGreaterThanOrEqual(releaseAt);
      expect(t.rootOperation).toMatch(/CreateOrder|Checkout/);
      expect(t.durationMs).toBeGreaterThanOrEqual(DEMO_INCIDENT.deadlineMs);
    }
    const staging = await api.listTraces({ ...traces(day), envIds: ["env-staging"] });
    expect(staging.traces.length).toBeGreaterThan(0);
    expect(staging.traces.every((t) => t.errorCount === 0)).toBe(true);
    const one = await api.getTrace({ ...scope, window: day, traceId: failed.find((t) => t.traceId !== TOURED)!.traceId });
    expect(one.spans.filter((s) => s.status === "error").map((s) => s.statusMessage)).toContain(DEMO_INCIDENT.error);
  });

  it("keeps the toured trace intact: prod's newest failing CreateOrder", async () => {
    const res = await api.listTraces(traces(hour, { errorsOnly: true, rootSpanName: "CreateOrder" }));
    expect(res.traces[0].traceId).toBe(TOURED);
    const toured = await api.getTrace({ ...scope, window: hour, traceId: TOURED });
    expect(toured.spans.map((s) => s.spanId)).toEqual(expect.arrayContaining(["a1", "a2", "b1", "d1", "d3", "e1", "e2", "e3"]));
    expect(res.traces[0].spanCount).toBe(toured.spans.length);
    const logs = await api.searchLogs({ ...scope, window: hour, query: "", services: [], severities: [], traceId: TOURED, limit: 100 });
    expect(logs.records).toHaveLength(5);
    expect(new Set(logs.records.map((r) => r.service))).toEqual(new Set(["api", "worker", "inventory"]));
  });

  it("crosses api → NATS → worker → inventory with 10–40 spans on multi-service traces", async () => {
    const res = await api.listTraces(traces(hour));
    const crossing = res.traces.filter((t) => t.services.includes("inventory") && t.services.includes("worker") && t.traceId !== TOURED);
    expect(crossing.length).toBeGreaterThan(20);
    for (const t of crossing.slice(0, 20)) {
      const trace = await api.getTrace({ ...scope, window: hour, traceId: t.traceId });
      expect(trace.spans.length).toBe(t.spanCount);
      expect(trace.spans.length).toBeGreaterThanOrEqual(10);
      expect(trace.spans.length).toBeLessThanOrEqual(45);
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
    // A day is more than the slots built: the counts still cover the whole day.
    expect(first.red!.traceCount).toBeGreaterThan(15_000);
    const errors = await api.listTraces(traces(hour, { errorsOnly: true }));
    expect(errors.traces.every((t) => t.errorCount > 0)).toBe(true);
    const slow = await api.listTraces(traces(hour, { minDurationMs: 500 }));
    expect(slow.traces.every((t) => t.durationMs >= 500)).toBe(true);
    const checkout = await api.listTraces(traces(hour, { rootSpanName: "Checkout" }));
    expect(checkout.traces.every((t) => t.rootOperation.includes("Checkout"))).toBe(true);
  });

  it("has thousands of log lines, each saying what its request did, linked to their traces", async () => {
    const logs = { ...scope, window: hour, query: "", services: [], severities: [], limit: 100_000 };
    const all = await api.searchLogs(logs);
    expect(all.records.length).toBeGreaterThan(2_000);
    const times = all.records.map((r) => r.timeMs);
    expect(times).toEqual([...times].sort((a, b) => b - a));
    const errors = all.records.filter((r) => r.severity === "error");
    expect(errors.length).toBeGreaterThan(8);
    expect(new Set(errors.map((r) => r.body))).toEqual(new Set([DEMO_INCIDENT.error]));
    expect(all.records.some((r) => r.body.startsWith(":") || /payments payments/.test(r.body))).toBe(false);
    expect(all.records.some((r) => r.body === "GET /products/{slug} started")).toBe(true);
    const oneTrace = all.records.find((r) => r.traceId && r.traceId !== TOURED)!.traceId;
    const scoped = await api.searchLogs({ ...logs, traceId: oneTrace });
    expect(scoped.records.length).toBeGreaterThan(0);
    expect(scoped.records.every((r) => r.traceId === oneTrace)).toBe(true);
    const page = await api.searchLogs({ ...logs, limit: 200 });
    expect(page.records.length).toBe(200);
    expect(page.nextCursor).not.toBe("");
  });

  it("draws every service's RED from the model, with api's errors stepping up at the release", async () => {
    const res = await api.getServiceOverview({ ...prod, window: hour });
    expect(res.services.map((s) => s.name)).toEqual([...BUSY_SERVICES]);
    const apiRow = res.services.find((s) => s.name === "api")!;
    const model = demoStats({ ...hour, envIds: ["env-prod"], service: "api", now: NOW });
    expect(Math.abs(apiRow.requestCount - model.requests) / model.requests).toBeLessThan(0.02);
    expect(Math.abs(apiRow.errorRate - model.errorRate)).toBeLessThan(0.005);
    const sum = (xs: (number | null)[]) => xs.reduce<number>((a, b) => a + (b ?? 0), 0);
    // The headline and its sparkline agree.
    expect(sum(apiRow.errorTrend)).toBe(apiRow.errorCount);
    const at = apiRow.timestamps.findIndex((t) => t * 1000 >= NOW - DEMO_RELEASE_AGO_MS);
    expect(sum(apiRow.errorTrend.slice(0, at - 1))).toBe(0);
    expect(sum(apiRow.errorTrend.slice(at + 1))).toBeGreaterThan(0);
    expect(res.services.find((s) => s.name === "inventory")!.errorCount).toBe(0);
    // Not every node looks the same.
    expect(new Set(res.services.map((s) => Math.round(s.p95Ms))).size).toBe(3);
  });

  it("has a profile per service and per type, in the type's unit", async () => {
    const req = { ...scope, window: hour };
    const seen = new Set<string>();
    for (const service of BUSY_SERVICES) {
      for (const profileType of DEMO_PROFILE_TYPES) {
        const flame = await api.getFlamegraph({ ...req, service, profileType });
        expect(JSON.parse(flame.speedscopeJson).name).toBe(`${service} ${profileType}`);
        expect(flame.unit).toBe(profileType === "cpu" ? "nanoseconds" : profileType.endsWith("_space") ? "bytes" : "none");
        expect(flame.availableProfileTypes).toEqual([...DEMO_PROFILE_TYPES]);
        seen.add(flame.speedscopeJson);
      }
    }
    expect(seen.size).toBe(BUSY_SERVICES.length * DEMO_PROFILE_TYPES.length);
  });
});

// The tour's "One request, end to end" step tells the visitor to open the
// toured trace "at the top" of the traces view, which renders only the rows on
// screen (about 20 on a desktop). Generated traffic follows the time of day, so
// this must hold at every hour, not only at NOW (RC-04): with the trace a minute
// old it ranked 13th to 46th, and journey-forge.spec.ts failed from 05:00Z on.
describe("the toured trace", () => {
  it("is in the traces view's first ten rows at every hour of the day", async () => {
    for (let h = 0; h < 24; h++) {
      const at = Date.UTC(2026, 10, 11, h, 7, 0);
      for (const envIds of [[], ["env-prod"]]) {
        // The last five minutes: only what started after the toured trace can rank above it,
        // and an hour-long window per hour of the day made this test slow under load.
        const res = await createBusyApp(at).listTraces(traces({ startMs: at - 5 * 60_000, endMs: at }, { envIds, limit: 100 }));
        const rank = res.traces.findIndex((t) => t.traceId === TOURED);
        expect(rank, `at ${h}:07 UTC in ${JSON.stringify(envIds)}`).toBeGreaterThanOrEqual(0);
        expect(rank, `at ${h}:07 UTC in ${JSON.stringify(envIds)}`).toBeLessThan(10);
      }
    }
  });
});
