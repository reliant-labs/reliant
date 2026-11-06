// Copyright (c) 2025 Reliant Labs

/**
 * The demo's data layer on forge's mock transport:
 *
 *  - the `demo` scenario, read through the console's REAL client (api.ts and
 *    its mappers, over connect.ts's routed transport), answers exactly what
 *    the model fixtures answer — the wire encoding loses nothing;
 *  - the transport refuses every write before a handler or the gateway sees it;
 *  - DEMO_DATA_SOURCE=remote asks the gateway first and falls back to the
 *    sample data, saying so, when it fails;
 *  - the demo console's API uses the wire only on the demo host.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError, createClient, type Transport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { IssuesService } from "@/gen/controlplane/services/issues/v1/issues_pb";
import { ListTracesResponseSchema, ObservabilityService } from "@/gen/controlplane/services/observability/v1/observability_pb";
import { installShellTransport } from "@/lib/connect";
import { resetShellForTests } from "@/lib/shell";
import { controlPlaneObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { setRuntimeConfig } from "@/test/runtimeConfig";

import { FIXTURE_SIGNALS } from "../../../Observability/__fixtures__/fixtureSignals";
import { createDemoApi } from "../demoApi";
import { createDemoFixtures } from "../busyApp";
import { getDemoDataStatus, resetDemoDataStatusForTests } from "../demoDataStatus";
import { DEMO_SCENARIO_RPCS, splitLogQuery } from "../demoScenario";
import { createDemoTransport, demoDataConfig, isDemoRead } from "../demoTransport";

const END_MS = Date.UTC(2026, 0, 15, 12, 0, 0);
const window = { startMs: END_MS - 3_600_000, endMs: END_MS };
const scope = { orgId: "org-fixture", projectId: "shop", envIds: [] as string[], filters: [] };
const TRACE = "4bf92f3577b34da6a3ce929d0e0e4736";

/** Numbers to 3 decimals (a Timestamp's nanos round-trip a millisecond fraction to within 1e-9); empty `links` dropped. */
function normalize(value: unknown): unknown {
  if (typeof value === "number") return Math.round(value * 1000) / 1000;
  if (Array.isArray(value)) return value.map(normalize);
  if (value && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [key, v] of Object.entries(value)) {
      if (key === "links" && Array.isArray(v) && v.length === 0) continue;
      out[key] = normalize(v);
    }
    return out;
  }
  return value;
}

beforeEach(() => {
  resetDemoDataStatusForTests();
  installShellTransport(createDemoTransport({ source: "fixtures", url: "" }));
});

afterEach(() => {
  installShellTransport(null);
  resetShellForTests();
  setRuntimeConfig({});
});

describe("the demo scenario, through the console's real client", () => {
  const fixtures = createDemoFixtures();
  const wire = controlPlaneObservabilityApi;

  it("answers ListTraces, GetTrace and SearchLogs exactly as the fixtures do", async () => {
    const traces = { ...scope, window, rootSpanName: "", services: [], errorsOnly: false, minDurationMs: 0, limit: 50 };
    expect(normalize(await wire.listTraces(traces))).toEqual(normalize(await fixtures.listTraces(traces)));

    const trace = { ...scope, window, traceId: TRACE };
    // The wire always carries the related-traces fields; the fixture leaves them out when empty.
    expect(normalize(await wire.getTrace(trace))).toEqual(normalize({ relatedTraces: [], relatedTracesTruncated: false, ...(await fixtures.getTrace(trace)) }));

    const logs = { ...scope, window, query: "", services: [], severities: [], traceId: TRACE, limit: 100 };
    const fromWire = await wire.searchLogs(logs);
    const fromFixtures = await fixtures.searchLogs(logs);
    // api.ts keys a record by its page position, the fixture by its own id; the rest must match.
    const strip = (records: { id: string }[]) => records.map(({ id: _id, ...rest }) => rest);
    expect(normalize(strip(fromWire.records))).toEqual(normalize(strip(fromFixtures.records)));
    expect(fromWire.records.map((r) => r.service)).toContain("inventory");
  });

  it("answers the overview, the flamegraph and the service signals as the fixtures do", async () => {
    // The client always reports unavailableSections (empty from a whole answer); the fixture leaves it out.
    expect(normalize(await wire.getServiceOverview({ ...scope, window }))).toEqual(
      normalize({ unavailableSections: [], ...(await fixtures.getServiceOverview({ ...scope, window })) }),
    );
    const flame = { ...scope, window, service: "api", profileType: "cpu", traceId: TRACE };
    expect(normalize(await wire.getFlamegraph(flame))).toEqual(normalize(await fixtures.getFlamegraph(flame)));
    expect(await wire.getServiceSignals({ ...scope, window })).toEqual(FIXTURE_SIGNALS);
  });

  it("splits SearchLogs' composed query back into the reader's text and the trace scope", () => {
    expect(splitLogQuery(`(a OR b) trace_id:${TRACE} span_id:e1`)).toEqual({ query: "a OR b", traceId: TRACE, spanId: "e1" });
    expect(splitLogQuery("timeout")).toEqual({ query: "timeout" });
  });
});

describe("the demo transport", () => {
  it("refuses every write before the scenario sees it, and reads Lists/Gets/Searches/Queries and the like", async () => {
    const client = createClient(IssuesService, createDemoTransport({ source: "fixtures", url: "" }));
    await expect(client.updateIssueStatus({ issueId: "x" } as never)).rejects.toSatisfy(
      (err: unknown) => err instanceof ConnectError && err.code === Code.PermissionDenied,
    );
    // A read the story says nothing about gets forge's generated default (demoServiceDefaults.ts).
    await expect(client.listIssues({} as never)).resolves.toBeDefined();
    expect(["ListTraces", "GetTrace", "SearchLogs", "QueryPanel", "CompareReleases", "WatchDeploy", "StreamLogs", "PlanDeploy"].every((name) => isDemoRead({ name }))).toBe(true);
    expect(["UpdateIssueStatus", "CreateSilence", "SaveDashboard", "MarkNotificationsRead"].some((name) => isDemoRead({ name }))).toBe(false);
  });

  it("serves the console's trace reads by the scenario's RPC keys", () => {
    expect(Object.values(DEMO_SCENARIO_RPCS)).toEqual(
      expect.arrayContaining(["controlplane.v1.ObservabilityService/ListTraces", "controlplane.v1.ObservabilityService/GetTrace"]),
    );
  });

  it("asks the gateway first when the source is remote, and uses its answer", async () => {
    const gateway = {
      unary: vi.fn(async (method: { name: string }) => ({
        message: create(ListTracesResponseSchema, { traces: [{ traceId: "live".padEnd(32, "0"), rootService: "api" }] }),
        method,
      })),
      stream: vi.fn(),
    } as unknown as Transport;
    const client = createClient(ObservabilityService, createDemoTransport({ source: "remote", url: "https://gw.test" }, gateway));
    const res = await client.listTraces({});
    expect(res.traces[0]?.traceId).toMatch(/^live/);
    expect(getDemoDataStatus()).toBe("live");
  });

  it("falls back to the sample data when the gateway fails, and says so", async () => {
    const gateway = {
      unary: vi.fn(async () => {
        throw new ConnectError("gateway down", Code.Unavailable);
      }),
      stream: vi.fn(),
    } as unknown as Transport;
    const client = createClient(ObservabilityService, createDemoTransport({ source: "remote", url: "https://gw.test" }, gateway));
    const res = await client.getTrace({ traceId: TRACE });
    expect(res.spans.some((span) => span.service === "inventory")).toBe(true);
    expect(getDemoDataStatus()).toBe("fallback");
    // A gateway never sees a write either.
    await expect(createClient(IssuesService, createDemoTransport({ source: "remote", url: "https://gw.test" }, gateway)).assignIssue({} as never)).rejects.toSatisfy(
      (err: unknown) => err instanceof ConnectError && err.code === Code.PermissionDenied,
    );
    expect(gateway.unary).toHaveBeenCalledTimes(1);
  });

  it("reads DEMO_DATA_SOURCE and DEMO_DATA_URL; remote without a URL is the sample data", () => {
    setRuntimeConfig({});
    expect(demoDataConfig()).toEqual({ source: "fixtures", url: "" });
    setRuntimeConfig({ DEMO_DATA_SOURCE: "remote" });
    expect(demoDataConfig()).toEqual({ source: "fixtures", url: "" });
    setRuntimeConfig({ DEMO_DATA_SOURCE: "remote", DEMO_DATA_URL: "https://demo-data.example.test/" });
    expect(demoDataConfig()).toEqual({ source: "remote", url: "https://demo-data.example.test" });
  });
});

describe("the demo console's API", () => {
  it("reads the scenario's RPCs over the wire on the demo host, the fixtures elsewhere, and refuses writes in both", async () => {
    resetShellForTests("demo");
    const onDemo = createDemoApi();
    expect(onDemo.listTraces).toBe(controlPlaneObservabilityApi.listTraces);
    await expect(onDemo.updateIssueStatus({} as never)).rejects.toThrow(/demo/i);
    resetShellForTests("forge");
    const elsewhere = createDemoApi();
    expect(elsewhere.listTraces).not.toBe(controlPlaneObservabilityApi.listTraces);
    await expect(elsewhere.assignIssue({} as never)).rejects.toThrow(/demo/i);
  });
});
