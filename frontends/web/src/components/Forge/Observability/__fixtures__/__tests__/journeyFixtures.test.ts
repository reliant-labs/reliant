// Copyright (c) 2025 Reliant Labs

/**
 * The fixture story the forge journey tells (journeys-spec.md §3.1): the
 * CreateOrder trace is listed as failing, it logged five lines on its own
 * spans across three services, it ran the release its issue is new in, and
 * it crosses services the way forge's instrumentation carries a trace (the
 * demo's fg-hops / fg-inside steps).
 */

import { describe, expect, it } from "vitest";

import { DEMO_INSIDE_SPAN_IDS, DEMO_INVENTORY_SPAN_ID, DEMO_ISSUE_ID, DEMO_TRACE_ID } from "@/components/Onboarding/journeys/forge";

import { createFixtureApi as createAnyFixtureApi, fixtureTrace as anyFixtureTrace, FIXTURE_CROSS_SERVICE_SPAN_COUNT, FIXTURE_TRACE_ID } from "../fixtureApi";
import { fixtureShopGraph } from "../fixtureGraph";

// A fixed hour, not the wall clock: the fixture places every record relative to the window it is asked for.
const END_MS = Date.UTC(2026, 0, 15, 12, 0, 0);
const window = { startMs: END_MS - 3_600_000, endMs: END_MS };
const scope = { orgId: "org-fixture", projectId: "shop", envIds: [] as string[], filters: [] };

// The demo console's fixtures (Forge/Demo/demoData/demoApi.ts): the toured trace crosses into inventory.
const createFixtureApi = () => createAnyFixtureApi({ crossService: true });
const fixtureTrace = (traceId: string, now: number) => anyFixtureTrace(traceId, now, { crossService: true });

describe("the forge journey's fixture story", () => {
  it("uses the fixture's ids", () => {
    expect(DEMO_TRACE_ID).toBe(FIXTURE_TRACE_ID);
    const ids = new Set(fixtureTrace(FIXTURE_TRACE_ID, window.endMs).spans.map((span) => span.spanId));
    for (const id of [DEMO_INVENTORY_SPAN_ID, ...Object.values(DEMO_INSIDE_SPAN_IDS)]) expect(ids.has(id), id).toBe(true);
  });

  it("has a parent for every span but the root", () => {
    const spans = fixtureTrace(FIXTURE_TRACE_ID, window.endMs).spans;
    const ids = new Set(spans.map((span) => span.spanId));
    expect(spans.filter((span) => !span.parentSpanId).map((span) => span.spanId)).toEqual(["a1"]);
    for (const span of spans) if (span.parentSpanId) expect(ids.has(span.parentSpanId), span.spanId).toBe(true);
    expect(spans).toHaveLength(FIXTURE_CROSS_SERVICE_SPAN_COUNT);
  });

  it("crosses into inventory as a server span under the worker's Connect client span", () => {
    const spans = new Map(fixtureTrace(FIXTURE_TRACE_ID, window.endMs).spans.map((span) => [span.spanId, span]));
    const hop = spans.get(DEMO_INVENTORY_SPAN_ID)!;
    expect(hop).toMatchObject({ service: "inventory", kind: "server" });
    expect(spans.get(hop.parentSpanId)).toMatchObject({ service: "worker", kind: "client", name: hop.name });
    // Inside a service: the method decorator under the RPC, a statement with its literals replaced.
    expect(spans.get(DEMO_INSIDE_SPAN_IDS.method)).toMatchObject({ service: "api", kind: "internal", parentSpanId: "a1" });
    expect(spans.get("e3")?.attributes["db.query.text"]).toMatch(/= \?/);
  });

  it("lists the toured trace with the services, span count and builds its detail has", async () => {
    const { traces } = await createFixtureApi().listTraces({ ...scope, window, limit: 50 } as never);
    const toured = traces.find((trace) => trace.traceId === FIXTURE_TRACE_ID)!;
    const spans = fixtureTrace(FIXTURE_TRACE_ID, window.endMs).spans;
    expect(new Set(toured.services)).toEqual(new Set(spans.map((span) => span.service)));
    expect(toured.spanCount).toBe(spans.length);
    expect(new Set(toured.backendBuilds.map((build) => build.service))).toEqual(new Set(spans.map((span) => span.service)));
  });

  it("leaves the trace other tests pin exactly as it was without crossService", () => {
    const plain = anyFixtureTrace(FIXTURE_TRACE_ID, window.endMs).spans.map((span) => span.spanId);
    expect(plain).toEqual(["a1", "b1", "b2", "b3", "c1", "c2", "b4", "d1", "d2"]);
  });

  it("draws shop's graph with the hops the trace makes", () => {
    const graph = fixtureShopGraph(window);
    const edges = graph.edges.map((edge) => `${edge.source}->${edge.target}`);
    expect(edges).toEqual(expect.arrayContaining(["api->nats", "nats->worker", "worker->inventory", "inventory->postgres"]));
    for (const edge of graph.edges) {
      expect(graph.nodes.some((node) => node.id === edge.source), edge.id).toBe(true);
      expect(graph.nodes.some((node) => node.id === edge.target), edge.id).toBe(true);
    }
  });

  it("lists the toured trace as failing, as its detail's failed spans say", async () => {
    const { traces } = await createFixtureApi().listTraces({ ...scope, window, limit: 50 } as never);
    const toured = traces.find((trace) => trace.traceId === FIXTURE_TRACE_ID);
    expect(toured?.errorCount).toBeGreaterThan(0);
    expect(fixtureTrace(FIXTURE_TRACE_ID, window.endMs).spans.filter((span) => span.status === "error").length).toBeGreaterThan(0);
    // Errors only still lists it, at the top.
    const errorsOnly = await createFixtureApi().listTraces({ ...scope, window, limit: 50, errorsOnly: true } as never);
    expect(errorsOnly.traces[0]?.traceId).toBe(FIXTURE_TRACE_ID);
  });

  it("has five log lines on the trace, on its own spans, across three services", async () => {
    const { records } = await createFixtureApi().searchLogs({ ...scope, window, services: [], severities: [], traceId: FIXTURE_TRACE_ID, query: "" } as never);
    const oldestFirst = [...records].sort((a, b) => a.timeMs - b.timeMs);
    expect(oldestFirst.map((record) => [record.spanId, record.severity, record.service, record.body])).toEqual([
      ["a1", "info", "api", "CreateOrder started"],
      ["c1", "warn", "api", "payments Charge attempt 1 failed, retrying"],
      ["c2", "error", "api", "payments Charge failed: context deadline exceeded"],
      ["e2", "info", "inventory", "reserved 2 items for order ord_7Q2K"],
      ["d1", "info", "worker", "orders.created processed, inventory updated"],
    ]);
    const spans = new Map(fixtureTrace(FIXTURE_TRACE_ID, window.endMs).spans.map((span) => [span.spanId, span]));
    for (const record of records) expect(spans.get(record.spanId)?.service).toBe(record.service);
  });

  it("keeps every other log line off the toured trace", async () => {
    const { records } = await createFixtureApi().searchLogs({ ...scope, window, services: [], severities: [], query: "" } as never);
    const others = records.filter((record) => !record.id.startsWith("fixture:trace-"));
    expect(others.some((record) => record.traceId === FIXTURE_TRACE_ID)).toBe(false);
    for (const record of others) if (record.traceId) expect(record.traceId).toMatch(/^[0-9a-f]{32}$/);
  });

  it("runs the trace on the release its issue is new in", async () => {
    const api = createFixtureApi();
    const detail = await api.getIssue({ issueId: DEMO_ISSUE_ID } as never);
    expect(detail.issue.firstRelease).toBe("v1.8.3");
    expect(detail.latestEvent?.traceId).toBe(FIXTURE_TRACE_ID);
    const versions = new Set(fixtureTrace(FIXTURE_TRACE_ID, window.endMs).spans.map((span) => span.resource["service.version"]));
    expect([...versions]).toEqual([detail.issue.firstRelease]);
    const { traces } = await api.listTraces({ ...scope, window, limit: 50 } as never);
    const toured = traces.find((trace) => trace.traceId === FIXTURE_TRACE_ID);
    expect(new Set(toured?.backendBuilds.map((build) => build.version))).toEqual(new Set(["v1.8.3"]));
  });
});
