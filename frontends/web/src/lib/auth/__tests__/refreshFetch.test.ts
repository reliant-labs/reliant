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

  it.each(["init", "request"])("rejects an already cancelled %s without starting fetch", async (source) => {
    const caller = new AbortController();
    const reason = new DOMException("cancelled", "AbortError");
    caller.abort(reason);
    const fetchFn = vi.fn(() => new Promise<Response>(() => {}));
    const input = source === "request" ? new Request(REFRESH_URL, { ...refresh, signal: caller.signal }) : REFRESH_URL;
    const init = source === "init" ? { ...refresh, signal: caller.signal } : undefined;
    await expect(withTokenRefreshTimeout(fetchFn)(input, init)).rejects.toBe(reason);
    expect(fetchFn).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });

  it.each(["headers", "body"])("settles caller cancellation despite an unresponsive %s reader", async (stage) => {
    let started!: () => void;
    const reading = new Promise<void>((resolve) => {
      started = resolve;
    });
    const fetchFn = vi.fn(() => {
      if (stage === "headers") {
        started();
        return new Promise<Response>(() => {});
      }
      const response = new Response();
      response.arrayBuffer = () => {
        started();
        return new Promise<ArrayBuffer>(() => {});
      };
      return Promise.resolve(response);
    });
    const caller = new AbortController();
    const reason = new DOMException("cancelled", "AbortError");
    const pending = withTokenRefreshTimeout(fetchFn)(REFRESH_URL, { ...refresh, signal: caller.signal });
    const rejected = expect(pending).rejects.toBe(reason);
    await reading;
    caller.abort(reason);
    await rejected;
    expect(fetchFn).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("enforces deadlines even when fetch ignores abort", async () => {
    const fetchFn = vi.fn(() => new Promise<Response>(() => {}));
    const pending = withTokenRefreshTimeout(fetchFn)(REFRESH_URL, refresh);
    const rejected = expect(pending).rejects.toBeInstanceOf(TokenRefreshTimeoutError);
    await vi.advanceTimersByTimeAsync(2 * REFRESH_ATTEMPT_TIMEOUT_MS);
    await rejected;
    expect(fetchFn).toHaveBeenCalledTimes(2);
    expect(vi.getTimerCount()).toBe(0);
  });

  // What the resend's safety rests on (refreshFetch.ts, "Why resending is
  // safe"): it carries the token the caller still holds, so the server sees it
  // at most one rotation behind; it happens at most once per call; and never
  // after the server's answer reached the caller.
  it("resends only the identical request, at most once per call", async () => {
    const { fetchFn, calls } = scriptedFetch(["hang", "hang", "hang", "hang"]);
    const pending = withTokenRefreshTimeout(fetchFn)(REFRESH_URL, refresh).catch((error: unknown) => error);
    await vi.advanceTimersByTimeAsync(10 * REFRESH_ATTEMPT_TIMEOUT_MS);
    expect(await pending).toBeInstanceOf(TokenRefreshTimeoutError);
    expect(calls).toHaveLength(2);
    for (const call of calls) {
      expect(call.input).toBe(REFRESH_URL);
      expect(call.init?.method).toBe("POST");
      expect(call.init?.body).toBe(refresh.body);
    }
  });

  it.each([400, 401, 429, 500, 503])("hands a %i answer to the caller instead of resending", async (status) => {
    const { fetchFn, calls } = scriptedFetch([{ afterMs: 100, status }, { afterMs: 0 }]);
    const pending = withTokenRefreshTimeout(fetchFn)(REFRESH_URL, refresh);
    await vi.advanceTimersByTimeAsync(5 * REFRESH_ATTEMPT_TIMEOUT_MS);
    expect((await pending).status).toBe(status);
    expect(calls).toHaveLength(1);
  });

  it("leaves a connection failure to supabase-js's own retry instead of resending", async () => {
    const calls: unknown[] = [];
    const fetchFn = ((input: RequestInfo | URL) => {
      calls.push(input);
      return Promise.reject(new TypeError("Load failed"));
    }) as typeof fetch;
    const settled = withTokenRefreshTimeout(fetchFn)(REFRESH_URL, refresh).catch((error: unknown) => error);
    await vi.advanceTimersByTimeAsync(5 * REFRESH_ATTEMPT_TIMEOUT_MS);
    expect(await settled).toBeInstanceOf(TypeError);
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
