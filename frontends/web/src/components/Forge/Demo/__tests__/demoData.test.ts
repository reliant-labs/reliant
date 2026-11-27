// Copyright (c) 2025 Reliant Labs

/**
 * The demo's data layer on forge's mock transport:
 *
 *  - the `demo` scenario, read through the console's REAL client (api.ts and
 *    its mappers, over connect.ts's routed transport), answers exactly what
 *    the story's model (model/shopApi.ts) answers — the wire encoding loses nothing;
 *  - the transport refuses every write before a handler or the gateway sees it;
 *  - DEMO_DATA_SOURCE=remote asks the gateway first and falls back to the
 *    sample data, saying so, when it fails, for every observability read
 *    (fixtures/shop/__tests__/realClient.test.ts holds each read's encoding);
 *  - the Agents pages' reads (projects, chats, …) are the shop app layer's
 *    (fixtures/shop/app.ts), with either source, and never reach the gateway.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError, createClient, type Transport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { IssuesService } from "@/gen/controlplane/services/issues/v1/issues_pb";
import { ChatService } from "@/gen/reliant/v1/chat_pb";
import { ProjectService } from "@/gen/reliant/v1/project_pb";
import { SHOP_READ_ONLY_MESSAGE } from "@/fixtures/shop";
import { CHAT_IDS, PROJECT_IDS } from "@/fixtures/shop/workspace";
import { ListTracesResponseSchema, ObservabilityService } from "@/gen/controlplane/services/observability/v1/observability_pb";
import { installShellTransport } from "@/lib/connect";
import { resetShellForTests } from "@/lib/shell";
import { controlPlaneObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { setRuntimeConfig } from "@/test/runtimeConfig";

import { DEMO_SIGNALS } from "@/fixtures/shop/model/busyApp";
import { createShopApi } from "@/fixtures/shop/model/shopApi";
import { getDemoDataStatus, resetDemoDataStatusForTests } from "../demoDataStatus";
import { splitLogQuery } from "@/fixtures/shop/handlers/observability";
import { createDemoTransport, demoDataConfig } from "../demoTransport";

const END_MS = Date.UTC(2026, 0, 15, 12, 0, 0);
const window = { startMs: END_MS - 3_600_000, endMs: END_MS };
const scope = { orgId: "org-acme", projectId: "shop", envIds: [] as string[], filters: [] };
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
  const fixtures = createShopApi();
  const wire = controlPlaneObservabilityApi;

  it("answers ListTraces, GetTrace and SearchLogs exactly as the model does", async () => {
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

  it("answers the overview, the flamegraph and the service signals as the model does", async () => {
    // The client always reports unavailableSections (empty from a whole answer); the fixture leaves it out.
    expect(normalize(await wire.getServiceOverview({ ...scope, window }))).toEqual(
      normalize({ unavailableSections: [], ...(await fixtures.getServiceOverview({ ...scope, window })) }),
    );
    const flame = { ...scope, window, service: "api", profileType: "cpu", traceId: TRACE };
    expect(normalize(await wire.getFlamegraph(flame))).toEqual(normalize(await fixtures.getFlamegraph(flame)));
    expect(await wire.getServiceSignals({ ...scope, window })).toEqual(DEMO_SIGNALS);
  });

  it("splits SearchLogs' composed query back into the reader's text and the trace scope", () => {
    expect(splitLogQuery(`(a OR b) trace_id:${TRACE} span_id:e1`)).toEqual({ query: "a OR b", traceId: TRACE, spanId: "e1" });
    expect(splitLogQuery("timeout")).toEqual({ query: "timeout" });
  });
});

describe("the demo transport", () => {
  it("refuses every write before the scenario sees it, and answers the reads its proto declares", async () => {
    const client = createClient(IssuesService, createDemoTransport({ source: "fixtures", url: "" }));
    await expect(client.updateIssueStatus({ issueId: "x" } as never)).rejects.toSatisfy(
      (err: unknown) => err instanceof ConnectError && err.code === Code.PermissionDenied,
    );
    // A read is answered: the story's (handlers/issues.ts), else forge's generated default (handlers/defaults.ts).
    await expect(client.listIssues({} as never)).resolves.toBeDefined();
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

  it("never masks caller cancellation as fixture success or a fallback banner", async () => {
    const controller = new AbortController();
    const canceled = new ConnectError("caller canceled", Code.Canceled);
    const gateway = {
      unary: vi.fn(async () => { controller.abort(); throw canceled; }),
      stream: vi.fn(),
    } as unknown as Transport;
    const client = createClient(ObservabilityService, createDemoTransport({ source: "remote", url: "https://gw.test" }, gateway));

    await expect(client.listTraces({}, { signal: controller.signal })).rejects.toBe(canceled);
    expect(getDemoDataStatus()).toBe("live");
    expect(gateway.unary).toHaveBeenCalledTimes(1);
  });

  it("falls back on the gateway deadline and keeps fixture mode network-free", async () => {
    const gateway = {
      unary: vi.fn(async (_method, _signal, timeoutMs) => {
        expect(timeoutMs).toBe(4_000);
        throw new ConnectError("gateway timed out", Code.DeadlineExceeded);
      }),
      stream: vi.fn(),
    } as unknown as Transport;
    const remote = createClient(ObservabilityService, createDemoTransport({ source: "remote", url: "https://gw.test" }, gateway));
    expect((await remote.getTrace({ traceId: TRACE })).spans.length).toBeGreaterThan(0);
    expect(getDemoDataStatus()).toBe("fallback");

    const fixtures = createClient(ObservabilityService, createDemoTransport({ source: "fixtures", url: "" }, gateway));
    await fixtures.getTrace({ traceId: TRACE });
    expect(gateway.unary).toHaveBeenCalledTimes(1);
    expect(getDemoDataStatus()).toBe("fixtures");
  });

  it("bounds a gateway that ignores deadlines, falls back and updates the failure banner", async () => {
    vi.useFakeTimers();
    try {
      const gateway = {
        unary: vi.fn(() => new Promise(() => {})),
        stream: vi.fn(),
      } as unknown as Transport;
      const client = createClient(ObservabilityService, createDemoTransport({ source: "remote", url: "https://gw.test" }, gateway));
      const response = client.getTrace({ traceId: TRACE });
      await vi.advanceTimersByTimeAsync(4_001);
      expect((await response).spans.length).toBeGreaterThan(0);
      expect(getDemoDataStatus()).toBe("fallback");
      expect(gateway.unary).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps unknown fixture reads unavailable rather than reaching a remote backend", async () => {
    // A descriptor not in the console defaults must not silently become live.
    const gateway = { unary: vi.fn(), stream: vi.fn() } as unknown as Transport;
    const transport = createDemoTransport({ source: "fixtures", url: "" }, gateway);
    const method = { ...IssuesService.method.listIssues, parent: { ...IssuesService, typeName: "unknown.Service" } };
    const client = createClient({ ...IssuesService, methods: [method], method: { listIssues: method } }, transport);
    await expect(client.listIssues({})).rejects.toSatisfy(
      (err: unknown) => err instanceof ConnectError && err.code === Code.Unimplemented,
    );
    expect(gateway.unary).not.toHaveBeenCalled();
    expect(globalThis.window.__demoRpcs?.unknown).toContain("unknown.Service/ListIssues");
  });

  it("falls back to the sample data for every observability read, not only the telemetry ones", async () => {
    const gateway = {
      unary: vi.fn(async () => {
        throw new ConnectError("gateway down", Code.Unavailable);
      }),
      stream: vi.fn(),
    } as unknown as Transport;
    installShellTransport(createDemoTransport({ source: "remote", url: "https://gw.test" }, gateway));
    const issues = await controlPlaneObservabilityApi.listIssues({ ...scope, window: { startMs: END_MS - 86_400_000, endMs: END_MS }, statuses: [], sort: "last" });
    expect(issues.issues.length).toBeGreaterThan(0);
    expect(gateway.unary).toHaveBeenCalled();
    expect(getDemoDataStatus()).toBe("fallback");
  });

  it("answers the Agents pages' reads from the shop app layer, and refuses their writes with the demo's message", async () => {
    const transport = createDemoTransport({ source: "fixtures", url: "" });
    const projects = createClient(ProjectService, transport);
    expect((await projects.listProjects({})).projects.map((p) => p.id)).toContain(PROJECT_IDS.shop);
    // One of the app layer's named exceptions (APP_LAYER_ANSWERS): a write on read on the server, a read here.
    expect((await projects.getProject({ projectId: PROJECT_IDS.shop })).project?.id).toBe(PROJECT_IDS.shop);
    expect((await createClient(ChatService, transport).listChats({ projectId: PROJECT_IDS.shop })).chats.map((c) => c.id)).toContain(CHAT_IDS.cartTotals);
    await expect(projects.deleteProject({ projectId: PROJECT_IDS.shop } as never)).rejects.toSatisfy(
      (err: unknown) => err instanceof ConnectError && err.code === Code.PermissionDenied && err.rawMessage === SHOP_READ_ONLY_MESSAGE,
    );
    // The page's probe names the refusal, for the smoke check and for whoever adds sample data.
    expect(globalThis.window.__demoRpcs?.refused).toContain("reliant.v1.ProjectService/DeleteProject");
  });

  it("keeps the Agents pages' reads off the gateway: the app layer answers them, and only console reads go out", async () => {
    const gateway = {
      unary: vi.fn(async () => {
        throw new ConnectError("gateway down", Code.Unavailable);
      }),
      stream: vi.fn(),
    } as unknown as Transport;
    const transport = createDemoTransport({ source: "remote", url: "https://gw.test" }, gateway);
    const projects = createClient(ProjectService, transport);
    expect((await projects.listProjects({})).projects.map((p) => p.id)).toContain(PROJECT_IDS.shop);
    expect((await projects.getProject({ projectId: PROJECT_IDS.shop })).project?.id).toBe(PROJECT_IDS.shop);
    expect(gateway.unary).not.toHaveBeenCalled();
    await createClient(ObservabilityService, transport).getTrace({ traceId: TRACE });
    expect(gateway.unary).toHaveBeenCalledTimes(1);
    expect(getDemoDataStatus()).toBe("fallback");
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
