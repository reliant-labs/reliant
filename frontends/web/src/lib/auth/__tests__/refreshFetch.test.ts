// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  REFRESH_ATTEMPT_TIMEOUT_MS,
  TokenRefreshTimeoutError,
  isTokenRefreshRequest,
  withTokenRefreshTimeout,
} from "../refreshFetch";

// The token refresh every RPC waits behind: bounded, retried once, then the
// error surfaces. On an iPhone waking up it used to hang for 38-51s.

const REFRESH_URL = "https://dash.example.invalid/auth/v1/token?grant_type=refresh_token";
const refresh = { method: "POST", body: JSON.stringify({ refresh_token: "r" }) };

/** A fetch whose answers the test schedules, one per call. */
function scriptedFetch(script: Array<{ afterMs: number; status?: number } | "hang">) {
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = [];
  const fetchFn = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init });
    const step = script[calls.length - 1] ?? "hang";
    return new Promise<Response>((resolve, reject) => {
      init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
      if (step === "hang") return;
      setTimeout(() => resolve(new Response(JSON.stringify({ access_token: "a" }), { status: step.status ?? 200 })), step.afterMs);
    });
  });
  return { fetchFn: fetchFn as unknown as typeof fetch, calls };
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe("withTokenRefreshTimeout", () => {
  it("lets a slow refresh that succeeds within the deadline through on its first attempt", async () => {
    const { fetchFn, calls } = scriptedFetch([{ afterMs: REFRESH_ATTEMPT_TIMEOUT_MS - 1_000 }]);
    const pending = withTokenRefreshTimeout(fetchFn)(REFRESH_URL, refresh);
    await vi.advanceTimersByTimeAsync(REFRESH_ATTEMPT_TIMEOUT_MS);
    const response = await pending;
    expect(await response.json()).toEqual({ access_token: "a" });
    expect(calls).toHaveLength(1);
  });

  it("sends a stalled refresh once more, on a fresh request", async () => {
    const { fetchFn, calls } = scriptedFetch(["hang", { afterMs: 300 }]);
    const pending = withTokenRefreshTimeout(fetchFn)(REFRESH_URL, refresh);
    await vi.advanceTimersByTimeAsync(REFRESH_ATTEMPT_TIMEOUT_MS + 300);
    expect((await pending).status).toBe(200);
    expect(calls).toHaveLength(2);
    expect(calls[0]!.init?.signal?.aborted).toBe(true);
    expect(calls[1]!.init?.body).toBe(refresh.body);
  });

  it("surfaces the error after the retry stalls too", async () => {
    const { fetchFn, calls } = scriptedFetch(["hang", "hang", { afterMs: 0 }]);
    const pending = withTokenRefreshTimeout(fetchFn)(REFRESH_URL, refresh);
    const settled = pending.then(
      () => null,
      (error: unknown) => error,
    );
    await vi.advanceTimersByTimeAsync(2 * REFRESH_ATTEMPT_TIMEOUT_MS);
    expect(await settled).toBeInstanceOf(TokenRefreshTimeoutError);
    expect(calls).toHaveLength(2);
  });

  it("bounds a response whose body stalls after its headers", async () => {
    let call = 0;
    const fetchFn = ((_input: RequestInfo | URL, init?: RequestInit) => {
      call++;
      if (call === 2) return Promise.resolve(new Response("{}", { status: 200 }));
      const body = new ReadableStream({
        start(controller) {
          init?.signal?.addEventListener("abort", () => controller.error(new DOMException("aborted", "AbortError")));
        },
      });
      return Promise.resolve(new Response(body, { status: 200 }));
    }) as typeof fetch;
    const pending = withTokenRefreshTimeout(fetchFn)(REFRESH_URL, refresh);
    await vi.advanceTimersByTimeAsync(REFRESH_ATTEMPT_TIMEOUT_MS);
    expect((await pending).status).toBe(200);
    expect(call).toBe(2);
  });

  it("does not retry a refresh its caller cancelled", async () => {
    const { fetchFn, calls } = scriptedFetch(["hang"]);
    const caller = new AbortController();
    const pending = withTokenRefreshTimeout(fetchFn)(REFRESH_URL, { ...refresh, signal: caller.signal });
    const settled = pending.then(
      () => null,
      (error: unknown) => error,
    );
    caller.abort();
    await vi.advanceTimersByTimeAsync(3 * REFRESH_ATTEMPT_TIMEOUT_MS);
    expect(await settled).toBeInstanceOf(DOMException);
    expect(calls).toHaveLength(1);
  });

  it("passes every other request through untouched", async () => {
    const { fetchFn, calls } = scriptedFetch(["hang"]);
    const otp = withTokenRefreshTimeout(fetchFn)("https://dash.example.invalid/auth/v1/otp", { method: "POST" });
    await vi.advanceTimersByTimeAsync(5 * REFRESH_ATTEMPT_TIMEOUT_MS);
    expect(calls).toHaveLength(1);
    expect(calls[0]!.init?.signal).toBeUndefined();
    void otp;
  });
});

describe("isTokenRefreshRequest", () => {
  it("is the refresh grant only", () => {
    expect(isTokenRefreshRequest(REFRESH_URL, { method: "POST" })).toBe(true);
    expect(isTokenRefreshRequest(new URL(REFRESH_URL), { method: "post" })).toBe(true);
    expect(isTokenRefreshRequest("https://x/auth/v1/token?grant_type=password", { method: "POST" })).toBe(false);
    expect(isTokenRefreshRequest("https://x/auth/v1/token?grant_type=pkce", { method: "POST" })).toBe(false);
    expect(isTokenRefreshRequest(REFRESH_URL, { method: "GET" })).toBe(false);
    expect(isTokenRefreshRequest("https://x/auth/v1/user")).toBe(false);
  });
});
