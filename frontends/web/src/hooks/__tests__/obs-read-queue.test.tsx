/**
 * The page's one observability read queue, end to end through obsClient and a
 * server that enforces control-plane's per-user cap of six concurrent reads:
 * a burst of panels all load with no error, views that are not the dashboard
 * share the budget, a refusal is re-queued rather than surfaced, and the org's
 * hourly quota still surfaces.
 */

import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ObservabilityApiProvider, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { ObsQuotaProvider } from "@/services/controlPlane/observability/ObsQuotaContext";
import { OBS_READ_CONCURRENCY, resetObsReadScheduler, SERVER_READ_CAP } from "@/services/controlPlane/observability/readScheduler";
import type { ObsScope, Panel } from "@/services/controlPlane/observability/types";

const server = vi.hoisted(() => ({
  cap: 6,
  live: 0,
  peak: 0,
  refused: 0,
  served: 0,
  /** Every call that reaches the server, in arrival order. */
  arrivals: [] as string[],
  /** What the server answers instead of serving, per call. */
  verdict: (_name: string): "serve" | "quota" => "serve",
  serveMs: 500,
}));

vi.mock("../../services/controlPlane/client", async () => {
  const { Code, ConnectError } = await import("@connectrpc/connect");
  const { BinaryWriter } = await import("@bufbuild/protobuf/wire");
  const inFlight = () => {
    const error = new ConnectError("too many observability queries in flight; retry shortly", Code.ResourceExhausted, { "x-forge-error-reason": "in_flight_limit" });
    const duration = new BinaryWriter().tag(1, 0).int64(1).finish();
    error.details.push({ type: "google.rpc.RetryInfo", value: new BinaryWriter().tag(1, 2).bytes(duration).finish() });
    return error;
  };
  const handle = (name: string, signal?: AbortSignal) => {
    server.arrivals.push(name);
    if (server.verdict(name) === "quota") {
      return Promise.reject(new ConnectError("organization observability query quota exhausted", Code.ResourceExhausted, { "x-forge-error-reason": "obs_query_quota" }));
    }
    if (server.live >= server.cap) {
      server.refused++;
      return Promise.reject(inFlight());
    }
    server.live++;
    server.peak = Math.max(server.peak, server.live);
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        server.live--;
        server.served++;
        resolve(RESPONSE[name] ?? {});
      }, server.serveMs);
      signal?.addEventListener("abort", () => {
        clearTimeout(timer);
        server.live--;
        reject(signal.reason);
      });
    });
  };
  const RESPONSE: Record<string, unknown> = {};
  const client = new Proxy(
    {},
    { get: (_t, name: string) => (req: { id?: string }, options?: { signal?: AbortSignal }) => handle(`${name}:${req.id ?? ""}`, options?.signal) }
  );
  return { getControlPlaneClient: () => client };
});

import { ObservabilityService } from "@/gen/controlplane/services/observability/v1/observability_pb";
import { useObsReadApi, usePanelQuery } from "../observability-queries";
import { obsClient } from "@/services/controlPlane/observability/obsClient";

const SCOPE: ObsScope = { orgId: "org", envIds: [], filters: [] };
const panel = (id: string): Panel => ({ id, title: id, kind: "number", signal: "traces", series: [{ label: id, agg: "count" }] });

/** An api whose queryPanel goes through the real obsClient, as production's does. */
function realisticApi(): ObservabilityApi {
  const client = () => obsClient(ObservabilityService) as unknown as Record<string, (req: unknown, options?: unknown) => Promise<unknown>>;
  return {
    currentOrgId: async () => "org",
    queryPanel: async (req: { panel: Panel }, options?: { signal?: AbortSignal }) => {
      const res = (await client().queryPanel({ id: req.panel.id }, options)) as Record<string, unknown>;
      return { kind: "number", unit: "", stepSeconds: 0, truncated: false, columns: [], rows: [], series: [], funnelSteps: [], issues: [], sessions: [], seriesMeta: [], seriesTotal: 0, warnings: [], compiledSql: [], ...res };
    },
    searchLogs: async () => (await client().searchLogs({ id: "logs" })) as never,
    getServiceGraph: async () => (await client().getServiceGraph({ id: "graph" })) as never,
    getObservabilityUsage: async () => (await client().getObservabilityUsage({ id: "usage" })) as never,
  } as unknown as ObservabilityApi;
}

function setup() {
  const client = new QueryClient({ defaultOptions: { queries: { gcTime: Infinity } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <ObsQuotaProvider>
        <ObservabilityApiProvider api={realisticApi()}>{children}</ObservabilityApiProvider>
      </ObsQuotaProvider>
    </QueryClientProvider>
  );
  return { client, wrapper };
}

const advance = (ms: number) => act(async () => void (await vi.advanceTimersByTimeAsync(ms)));

beforeEach(() => {
  vi.useFakeTimers();
  vi.spyOn(Math, "random").mockReturnValue(0.5);
  resetObsReadScheduler();
  Object.assign(server, { cap: SERVER_READ_CAP, live: 0, peak: 0, refused: 0, served: 0, arrivals: [], verdict: () => "serve", serveMs: 500 });
});
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("the read queue is sized to the server's cap", () => {
  it("never lets one page start more reads than the server allows", () => {
    expect(OBS_READ_CONCURRENCY).toBeLessThanOrEqual(SERVER_READ_CAP);
  });
});

describe("a burst of panels", () => {
  it.each([12, 30, 60])("all %i load, with no error state, and the server refuses none", async (count) => {
    const { wrapper } = setup();
    const ids = Array.from({ length: count }, (_, i) => `p${i}`);
    const { result } = renderHook(() => ids.map((id) => usePanelQuery(SCOPE, "1h", panel(id), [])), { wrapper });
    await advance(100 * 1000);

    expect(result.current.map((q) => q.isError)).not.toContain(true);
    expect(result.current.every((q) => q.data !== undefined)).toBe(true);
    expect(server.served).toBe(count);
    expect(server.peak).toBeLessThanOrEqual(OBS_READ_CONCURRENCY);
    expect(server.refused).toBe(0);
  });

  it("still loads every panel with no error when the server's cap is far below the queue's (another tab holds slots), by re-queuing the refusals", async () => {
    const { wrapper, client } = setup();
    server.cap = 1;
    let seed = 7;
    vi.spyOn(Math, "random").mockImplementation(() => ((seed = (seed * 48271) % 2147483647) / 2147483647));
    const ids = Array.from({ length: 20 }, (_, i) => `p${i}`);
    const { result } = renderHook(() => ids.map((id) => usePanelQuery(SCOPE, "1h", panel(id), [])), { wrapper });

    // Mid-way, no panel is in an error state: the refused ones are loading. (Reading `data` here
    // subscribes the observers to it: they only re-render for properties a test has read.)
    await advance(5_000);
    expect(server.refused).toBeGreaterThan(0);
    expect(result.current.map((q) => q.isError)).not.toContain(true);
    expect(result.current.some((q) => q.data === undefined && q.isLoading)).toBe(true);

    await advance(10 * 60_000);
    // The cache is the truth: hook results only re-render for the properties a test has read.
    const states = client.getQueryCache().getAll().map((q) => q.state.status);
    expect(states).toHaveLength(20);
    expect(states.every((status) => status === "success")).toBe(true);
    expect(server.served).toBe(20);
  });

  it("keeps waiting while the server stays full for minutes, then loads when a slot frees", async () => {
    const { wrapper } = setup();
    server.cap = 0;
    const { result } = renderHook(() => usePanelQuery(SCOPE, "1h", panel("p"), []), { wrapper });
    await advance(5 * 60_000);
    expect(result.current.isError).toBe(false);
    expect(result.current.isLoading).toBe(true);
    expect(server.refused).toBeGreaterThan(5);

    server.cap = SERVER_READ_CAP;
    await advance(60_000);
    expect(result.current.data).toBeDefined();
    expect(result.current.isError).toBe(false);
  });
});

describe("other views share the budget", () => {
  it("queues logs, the graph and panels in the same queue, and lets a read that takes no slot straight through", async () => {
    const { wrapper } = setup();
    const ids = Array.from({ length: 10 }, (_, i) => `p${i}`);
    const { result } = renderHook(
      () => {
        const api = useObsReadApi();
        return {
          panels: ids.map((id) => usePanelQuery(SCOPE, "1h", panel(id), [])),
          api,
        };
      },
      { wrapper }
    );
    const other = [result.current.api.searchLogs({} as never), result.current.api.getServiceGraph({} as never)];
    const usage = result.current.api.getObservabilityUsage({} as never);
    await advance(0);

    // The usage read is not behind the queue: it is already at the server.
    expect(server.arrivals).toContain("getObservabilityUsage:usage");
    // The queue holds everything else to its width, whichever view asked.
    expect(server.arrivals.filter((a) => !a.startsWith("getObservabilityUsage"))).toHaveLength(OBS_READ_CONCURRENCY);

    await advance(60_000);
    await Promise.all([...other, usage]);
    expect(server.refused).toBe(0);
    expect(server.peak).toBeLessThanOrEqual(OBS_READ_CONCURRENCY + 1);
    expect(result.current.panels.every((q) => q.data !== undefined)).toBe(true);
  });
});

describe("a refusal is re-queued, not surfaced", () => {
  it("shows the panel as loading with the cap as the reason, and asks again after the server's Retry-After", async () => {
    const { wrapper } = setup();
    server.cap = 0;
    const { result } = renderHook(() => usePanelQuery(SCOPE, "1h", panel("p"), []), { wrapper });
    await advance(10);

    expect(server.refused).toBe(1);
    expect(result.current.error).toBeNull();
    expect(result.current.isLoading).toBe(true);
    expect(result.current.failureCount).toBe(1);

    // Retry-After 1s + a jittered share of the first window (0.5 * 1s).
    await advance(1_400);
    expect(server.refused).toBe(1);
    await advance(200);
    expect(server.refused).toBe(2);
    expect(result.current.error).toBeNull();
  });
});

describe("the org's hourly quota still surfaces", () => {
  it("shows the quota error at once, never retries it, and does not clog the queue for the panels behind it", async () => {
    const { wrapper } = setup();
    server.verdict = (name) => (name === "queryPanel:quota" ? "quota" : "serve");
    const { result } = renderHook(() => ["quota", "a", "b"].map((id) => usePanelQuery(SCOPE, "1h", panel(id), [])), { wrapper });
    await advance(10);

    const [quota] = result.current;
    expect(quota!.isError).toBe(true);
    expect(quota!.error).toBeInstanceOf(ConnectError);
    expect((quota!.error as ConnectError).code).toBe(Code.ResourceExhausted);
    expect(server.arrivals.filter((x) => x === "queryPanel:quota")).toHaveLength(1);

    await advance(60_000);
    expect(server.arrivals.filter((x) => x === "queryPanel:quota")).toHaveLength(1);
    const [, a, b] = result.current;
    expect(a!.data).toBeDefined();
    expect(b!.data).toBeDefined();
    expect(a!.isError || b!.isError).toBe(false);
  });
});

describe("leaving the view", () => {
  it("drops queued and refused reads when the views unmount, so the queue is free for the next page", async () => {
    const { wrapper } = setup();
    server.cap = 0;
    const ids = Array.from({ length: 10 }, (_, i) => `p${i}`);
    const { unmount } = renderHook(() => ids.map((id) => usePanelQuery(SCOPE, "1h", panel(id), [])), { wrapper });
    await advance(5_000);
    const before = server.arrivals.length;
    unmount();
    await advance(5 * 60_000);
    expect(server.arrivals.length).toBe(before);
  });
});
