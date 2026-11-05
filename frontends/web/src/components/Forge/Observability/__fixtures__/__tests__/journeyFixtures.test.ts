// Copyright (c) 2025 Reliant Labs

/**
 * The fixture story the forge journey tells (journeys-spec.md §3.1): the
 * CreateOrder trace is listed as failing, it logged four lines on its own
 * spans across both services, and it ran the release its issue is new in.
 */

import { describe, expect, it } from "vitest";

import { DEMO_ISSUE_ID, DEMO_TRACE_ID } from "@/components/Onboarding/journeys/forge";

import { createFixtureApi, fixtureTrace, FIXTURE_TRACE_ID } from "../fixtureApi";

const window = { startMs: Date.now() - 3_600_000, endMs: Date.now() };
const scope = { orgId: "org-fixture", projectId: "shop", envIds: [] as string[], filters: [] };

describe("the forge journey's fixture story", () => {
  it("uses the fixture's ids", () => {
    expect(DEMO_TRACE_ID).toBe(FIXTURE_TRACE_ID);
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

  it("has four log lines on the trace, on its own spans, across both services", async () => {
    const { records } = await createFixtureApi().searchLogs({ ...scope, window, services: [], severities: [], traceId: FIXTURE_TRACE_ID, query: "" } as never);
    const oldestFirst = [...records].sort((a, b) => a.timeMs - b.timeMs);
    expect(oldestFirst.map((record) => [record.spanId, record.severity, record.service, record.body])).toEqual([
      ["a1", "info", "api", "CreateOrder started"],
      ["c1", "warn", "api", "payments Charge attempt 1 failed, retrying"],
      ["c2", "error", "api", "payments Charge failed: context deadline exceeded"],
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
