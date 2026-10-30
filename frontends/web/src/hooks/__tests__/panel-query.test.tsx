/**
 * The panel hook's half of the read path: a refusal for the server's
 * concurrent-read cap is retried (honouring RetryInfo) for as long as the panel
 * is mounted and never becomes an error; the hourly quota and real errors do
 * surface; unmounting or changing the range cancels the read. The queue every
 * read waits in is obs-read-queue.test.tsx.
 */

import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { BinaryWriter } from "@bufbuild/protobuf/wire";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ObservabilityApiProvider, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { ObsQuotaProvider } from "@/services/controlPlane/observability/ObsQuotaContext";
import type { ObsScope, Panel, PanelResult } from "@/services/controlPlane/observability/types";

import { inFlightRetryDelayMs, obsRetry } from "@/services/controlPlane/observability/errors";
import { QUOTA_REASON } from "@/services/controlPlane/observability/quota";

import { usePanelQuery } from "../observability-queries";

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

describe("inFlightRetryDelayMs", () => {
  it("never waits less than the server's Retry-After, and adds a jittered share of a growing window", () => {
    expect(inFlightRetryDelayMs(0, busy(2), () => 0)).toBe(2000);
    expect(inFlightRetryDelayMs(0, busy(2), () => 0.5)).toBe(2500);
    expect(inFlightRetryDelayMs(2, busy(2), () => 0.5)).toBe(2000 + 2000);
    expect(inFlightRetryDelayMs(0, busy(), () => 0.5)).toBe(500);
  });

  it("spreads refused reads out, and caps the window so a long wait stays bounded", () => {
    const waits = [0.1, 0.4, 0.8].map((dice) => inFlightRetryDelayMs(1, busy(1), () => dice));
    expect(new Set(waits).size).toBe(3);
    expect(inFlightRetryDelayMs(20, busy(0), () => 1)).toBeLessThanOrEqual(16_000);
  });
});

describe("obsRetry", () => {
  it("retries the in-flight refusal without a count limit, and nothing else the server meant", () => {
    expect(obsRetry(0, busy())).toBe(true);
    expect(obsRetry(1_000, busy())).toBe(true);
    expect(obsRetry(0, new ConnectError("down", Code.Unavailable))).toBe(true);
    expect(obsRetry(1, new ConnectError("down", Code.Unavailable))).toBe(false);
    expect(obsRetry(0, new ConnectError("no", Code.PermissionDenied))).toBe(false);
    expect(obsRetry(0, quota())).toBe(false);
  });
});

function quota() {
  return new ConnectError("organization observability query quota exhausted", Code.ResourceExhausted, { "x-forge-error-reason": QUOTA_REASON });
}

describe("usePanelQuery", () => {
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

  it("aborts its read when the panel goes away", async () => {
    const { api, calls } = controlledApi();
    const { wrapper } = setup(api);
    const { unmount } = renderHook(() => usePanelQuery(SCOPE, "1h", panel("a"), []), { wrapper });
    await flush();
    unmount();
    await flush();
    expect(calls[0]!.signal?.aborted).toBe(true);
  });

  it("cancels the read of the old range when the range changes, then reads the new one", async () => {
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

  it("keeps asking for as long as the panel is mounted: dozens of refusals never become an error", async () => {
    const { api, calls } = controlledApi();
    const { wrapper } = setup(api);
    const { result } = renderHook(() => usePanelQuery(SCOPE, "1h", panel("p"), []), { wrapper });
    await flush();

    const REFUSALS = 40;
    for (let attempt = 1; attempt <= REFUSALS; attempt++) {
      expect(calls).toHaveLength(attempt);
      calls[attempt - 1]!.reject(busy(1));
      await flush();
      expect(result.current.isError).toBe(false);
      expect(result.current.isLoading).toBe(true);
      await advance(1000);
    }
    expect(calls).toHaveLength(REFUSALS + 1);
    calls[REFUSALS]!.resolve();
    await flush();
    expect(result.current.data).toEqual(EMPTY);
    expect(result.current.isError).toBe(false);
  });

  it("stops asking once the panel is unmounted", async () => {
    const { api, calls } = controlledApi();
    const { wrapper } = setup(api);
    const { unmount } = renderHook(() => usePanelQuery(SCOPE, "1h", panel("p"), []), { wrapper });
    await flush();
    calls[0]!.reject(busy(1));
    await flush();
    unmount();
    await advance(120_000);
    expect(calls).toHaveLength(1);
  });

  it("surfaces the org's hourly quota at once: it is not retried, and it is not the cap", async () => {
    const { api, calls } = controlledApi();
    const { wrapper } = setup(api);
    const { result } = renderHook(() => usePanelQuery(SCOPE, "1h", panel("p"), []), { wrapper });
    await flush();
    calls[0]!.reject(quota());
    await flush();
    expect(result.current.isError).toBe(true);
    expect(result.current.isLoading).toBe(false);
    await advance(120_000);
    expect(calls).toHaveLength(1);
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
