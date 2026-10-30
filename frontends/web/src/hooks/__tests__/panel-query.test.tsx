/**
 * The dashboard panel read path: a bounded, first-come-first-served queue
 * under control-plane's cap of six concurrent reads per user, retry with
 * jitter for the cap's refusal (honouring RetryInfo), and a Retry that asks
 * again for one panel only.
 */

import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { BinaryWriter } from "@bufbuild/protobuf/wire";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ObservabilityApiProvider, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { ObsQuotaProvider } from "@/services/controlPlane/observability/ObsQuotaContext";
import { PANEL_CONCURRENCY } from "@/services/controlPlane/observability/panelScheduler";
import type { ObsScope, Panel, PanelResult } from "@/services/controlPlane/observability/types";

import { PANEL_BUSY_RETRIES, panelBusyDelayMs, panelRetry, usePanelQuery } from "../observability-queries";

const SCOPE: ObsScope = { orgId: "org", envIds: [], filters: [] };
const EMPTY: PanelResult = { kind: "number", unit: "", stepSeconds: 0, truncated: false, columns: [], rows: [], series: [], funnelSteps: [], issues: [], sessions: [] } as unknown as PanelResult;

const panel = (id: string): Panel => ({ id, title: id, kind: "number", signal: "traces", series: [{ label: id, agg: "count" }] });

function busy(retryAfterSeconds?: number) {
  const error = new ConnectError("too many observability queries in flight; retry shortly", Code.ResourceExhausted, { "x-forge-error-reason": "in_flight_limit" });
  if (retryAfterSeconds !== undefined) {
    const duration = new BinaryWriter().tag(1, 0).int64(retryAfterSeconds).finish();
    error.details.push({ type: "google.rpc.RetryInfo", value: new BinaryWriter().tag(1, 2).bytes(duration).finish() });
  }
  return error;
}

interface Call {
  id: string;
  signal: AbortSignal | undefined;
  resolve: () => void;
  reject: (error: unknown) => void;
}

/** An api whose queryPanel answers when the test says so, recording every call in start order. */
function controlledApi() {
  const calls: Call[] = [];
  let live = 0;
  let peak = 0;
  const api = {
    currentOrgId: async () => "org",
    queryPanel: (req: { panel: Panel }, options?: { signal?: AbortSignal }) =>
      new Promise<PanelResult>((resolve, reject) => {
        live++;
        peak = Math.max(peak, live);
        const done = () => void live--;
        calls.push({
          id: req.panel.id,
          signal: options?.signal,
          resolve: () => {
            done();
            resolve(EMPTY);
          },
          reject: (error) => {
            done();
            reject(error);
          },
        });
      }),
  } as unknown as ObservabilityApi;
  return { api, calls, peak: () => peak };
}

function setup(api: ObservabilityApi) {
  const client = new QueryClient({ defaultOptions: { queries: { gcTime: Infinity } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <ObsQuotaProvider>
        <ObservabilityApiProvider api={api}>{children}</ObservabilityApiProvider>
      </ObsQuotaProvider>
    </QueryClientProvider>
  );
  return { client, wrapper };
}

const flush = () => act(async () => void (await vi.advanceTimersByTimeAsync(0)));
const advance = (ms: number) => act(async () => void (await vi.advanceTimersByTimeAsync(ms)));

beforeEach(() => {
  vi.useFakeTimers();
  vi.spyOn(Math, "random").mockReturnValue(0);
});
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("panelBusyDelayMs", () => {
  it("is never below the server's Retry-After, and adds a random share of a growing window", () => {
    expect(panelBusyDelayMs(0, busy(2), () => 0)).toBe(2000);
    expect(panelBusyDelayMs(0, busy(2), () => 0.5)).toBe(2500);
    expect(panelBusyDelayMs(2, busy(2), () => 0.5)).toBe(2000 + 2000);
    expect(panelBusyDelayMs(0, busy(), () => 0.5)).toBe(500);
  });

  it("jitters: different dice, different waits, so refused panels do not return together", () => {
    const waits = [0.1, 0.4, 0.8].map((dice) => panelBusyDelayMs(1, busy(1), () => dice));
    expect(new Set(waits).size).toBe(3);
  });

  it("caps the window", () => {
    expect(panelBusyDelayMs(20, busy(0), () => 1)).toBeLessThanOrEqual(16_000);
  });
});

describe("panelRetry", () => {
  it("retries the cap's refusal a bounded number of times and every other answer as before", () => {
    expect(panelRetry(0, busy())).toBe(true);
    expect(panelRetry(PANEL_BUSY_RETRIES - 1, busy())).toBe(true);
    expect(panelRetry(PANEL_BUSY_RETRIES, busy())).toBe(false);
    expect(panelRetry(0, new ConnectError("down", Code.Unavailable))).toBe(true);
    expect(panelRetry(1, new ConnectError("down", Code.Unavailable))).toBe(false);
    expect(panelRetry(0, new ConnectError("no", Code.PermissionDenied))).toBe(false);
  });
});

describe("usePanelQuery scheduling", () => {
  it("never has more reads in flight than the scheduler's cap, and starts panels in the order they asked", async () => {
    const { api, calls, peak } = controlledApi();
    const { wrapper } = setup(api);
    const ids = Array.from({ length: 15 }, (_, i) => `p${String(i).padStart(2, "0")}`);
    renderHook(() => ids.map((id) => usePanelQuery(SCOPE, "1h", panel(id), [])), { wrapper });
    await flush();

    expect(PANEL_CONCURRENCY).toBeLessThan(6);
    expect(calls.map((c) => c.id)).toEqual(ids.slice(0, PANEL_CONCURRENCY));

    for (let i = 0; i < ids.length; i++) {
      calls[i]!.resolve();
      await flush();
    }
    expect(calls.map((c) => c.id)).toEqual(ids);
    expect(peak()).toBe(PANEL_CONCURRENCY);
  });

  it("does not read a disabled panel (not yet near the viewport)", async () => {
    const { api, calls } = controlledApi();
    const { wrapper } = setup(api);
    const { rerender } = renderHook(({ enabled }) => usePanelQuery(SCOPE, "1h", panel("far"), [], [], { enabled }), { wrapper, initialProps: { enabled: false } });
    await flush();
    expect(calls).toHaveLength(0);
    rerender({ enabled: true });
    await flush();
    expect(calls.map((c) => c.id)).toEqual(["far"]);
  });

  it("drops a queued read, and aborts a running one, when its panel goes away", async () => {
    const { api, calls } = controlledApi();
    const { wrapper } = setup(api);
    const ids = ["a", "b", "c", "d", "e"];
    const { unmount } = renderHook(() => ids.map((id) => usePanelQuery(SCOPE, "1h", panel(id), [])), { wrapper });
    await flush();
    expect(calls.map((c) => c.id)).toEqual(["a", "b", "c"]);

    unmount();
    await flush();
    expect(calls.every((c) => c.signal?.aborted)).toBe(true);
    // The two that never got a slot never go out, even though slots were freed.
    expect(calls.map((c) => c.id)).toEqual(["a", "b", "c"]);
  });

  it("cancels the reads of the old range when the range changes, then reads the new one", async () => {
    const { api, calls } = controlledApi();
    const { wrapper } = setup(api);
    const { rerender } = renderHook(({ range }) => usePanelQuery(SCOPE, range, panel("p"), []), { wrapper, initialProps: { range: "1h" as "1h" | "24h" } });
    await flush();
    expect(calls).toHaveLength(1);
    rerender({ range: "24h" });
    await flush();
    expect(calls[0]!.signal?.aborted).toBe(true);
    expect(calls).toHaveLength(2);
    expect(calls[1]!.signal?.aborted).toBe(false);
  });
});

describe("usePanelQuery when the server is at its cap", () => {
  it("waits, honouring Retry-After, asks again, and recovers without ever showing an error", async () => {
    const { api, calls } = controlledApi();
    const { wrapper } = setup(api);
    const { result } = renderHook(() => usePanelQuery(SCOPE, "1h", panel("p"), []), { wrapper });
    await flush();
    calls[0]!.reject(busy(2));
    await flush();

    expect(result.current.isLoading).toBe(true);
    expect(result.current.error).toBeNull();
    expect(result.current.failureCount).toBe(1);
    expect(result.current.failureReason).toBeInstanceOf(ConnectError);

    await advance(1999);
    expect(calls).toHaveLength(1);
    await advance(1);
    expect(calls).toHaveLength(2);

    calls[1]!.resolve();
    await flush();
    expect(result.current.data).toEqual(EMPTY);
    expect(result.current.error).toBeNull();
  });

  it("backs off further on each refusal, and gives up with an error after the bounded attempts", async () => {
    const { api, calls } = controlledApi();
    const { wrapper } = setup(api);
    const { result } = renderHook(() => usePanelQuery(SCOPE, "1h", panel("p"), []), { wrapper });
    await flush();

    // Random pinned to 0: delay = Retry-After (1s) every time, so count the asks, not the waits.
    for (let attempt = 1; attempt <= PANEL_BUSY_RETRIES; attempt++) {
      expect(calls).toHaveLength(attempt);
      calls[attempt - 1]!.reject(busy(1));
      await flush();
      expect(result.current.error).toBeNull();
      await advance(1000);
    }
    expect(calls).toHaveLength(PANEL_BUSY_RETRIES + 1);
    calls[PANEL_BUSY_RETRIES]!.reject(busy(1));
    await flush();

    expect(result.current.isError).toBe(true);
    expect(result.current.isLoading).toBe(false);
    await advance(120_000);
    expect(calls).toHaveLength(PANEL_BUSY_RETRIES + 1);
  });

  it("Retry after exhaustion reads that panel again, and only that panel", async () => {
    const { api, calls } = controlledApi();
    const { wrapper } = setup(api);
    const { result } = renderHook(
      () => ({ a: usePanelQuery(SCOPE, "1h", panel("a"), []), b: usePanelQuery(SCOPE, "1h", panel("b"), []) }),
      { wrapper }
    );
    await flush();
    calls[1]!.resolve();
    await flush();
    // a is refused until it gives up.
    const asksOfA = () => calls.filter((c) => c.id === "a");
    for (let i = 0; i <= PANEL_BUSY_RETRIES; i++) {
      asksOfA().at(-1)!.reject(busy(1));
      await flush();
      await advance(1000);
    }
    expect(result.current.a.isError).toBe(true);
    // Observers re-render only for the properties a test has read: read the ones asserted on later.
    expect(result.current.a.data).toBeUndefined();
    expect(result.current.b.data).toEqual(EMPTY);
    const before = calls.length;

    act(() => void result.current.a.refetch());
    await flush();
    expect(calls).toHaveLength(before + 1);
    expect(calls.at(-1)!.id).toBe("a");
    calls.at(-1)!.resolve();
    await flush();
    expect(result.current.a.data).toEqual(EMPTY);
    expect(result.current.a.isError).toBe(false);
  });

  it("sends a retry to the back of the queue, so refused panels never jump ahead and the cap still holds", async () => {
    const { api, calls, peak } = controlledApi();
    const { wrapper } = setup(api);
    renderHook(() => ["a", "b", "c", "d"].map((id) => usePanelQuery(SCOPE, "1h", panel(id), [])), { wrapper });
    await flush();
    expect(calls.map((c) => c.id)).toEqual(["a", "b", "c"]);

    calls[0]!.reject(busy(1));
    await flush();
    // d was waiting first: it takes a's freed slot.
    expect(calls.map((c) => c.id)).toEqual(["a", "b", "c", "d"]);
    await advance(1000);
    // a's retry waits behind the three running reads.
    expect(calls.map((c) => c.id)).toEqual(["a", "b", "c", "d"]);
    calls[1]!.resolve();
    await flush();
    expect(calls.map((c) => c.id)).toEqual(["a", "b", "c", "d", "a"]);
    expect(peak()).toBe(PANEL_CONCURRENCY);
  });

  it("does not wait on an error the server meant, and a plain failure keeps its Retry", async () => {
    const { api, calls } = controlledApi();
    const { wrapper } = setup(api);
    const { result } = renderHook(() => usePanelQuery(SCOPE, "1h", panel("p"), []), { wrapper });
    await flush();
    calls[0]!.reject(new ConnectError("no", Code.PermissionDenied));
    await flush();
    expect(result.current.isError).toBe(true);
    expect(calls).toHaveLength(1);
    act(() => void result.current.refetch());
    await flush();
    expect(calls).toHaveLength(2);
  });
});
