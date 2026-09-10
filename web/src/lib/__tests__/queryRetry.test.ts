/**
 * The retry predicate every React Query query in the app shares.
 *
 * THE BUG THIS PINS: the guard used to ask whether the error MESSAGE contained
 * the substring "401" or "403". A Connect RPC failure reads
 *
 *   [unauthenticated] missing authorization token
 *
 * which contains neither. So the guard never fired, and every RPC issued while
 * signed out was retried twice — the background "polling" a user saw against
 * GetProviderStatuses while they were still typing their email code.
 *
 * The real signal is the ConnectError CODE, not the prose it renders to.
 */
import { describe, expect, it } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";

import { shouldRetryQuery } from "../queryRetry";

describe("shouldRetryQuery — Connect error codes", () => {
  it("does NOT retry an unauthenticated Connect error (the prod bug)", () => {
    // Byte-for-byte what the backend's auth interceptor produces, and what was
    // confirmed in the dev browser logs.
    const error = new ConnectError(
      "missing authorization token",
      Code.Unauthenticated,
    );
    expect(error.message).toBe("[unauthenticated] missing authorization token");
    // The old guard's exact test, shown failing to match:
    expect(error.message.includes("401")).toBe(false);
    expect(error.message.includes("403")).toBe(false);

    expect(shouldRetryQuery(0, error)).toBe(false);
  });

  it("does NOT retry permission-denied", () => {
    const error = new ConnectError("forbidden", Code.PermissionDenied);
    expect(shouldRetryQuery(0, error)).toBe(false);
  });

  it("does NOT retry not-found — retrying a 404 twice is pure latency", () => {
    const error = new ConnectError("no such chat", Code.NotFound);
    expect(shouldRetryQuery(0, error)).toBe(false);
  });

  it("retries codes that are genuinely transient", () => {
    for (const code of [Code.Unavailable, Code.DeadlineExceeded, Code.Internal]) {
      expect(shouldRetryQuery(0, new ConnectError("boom", code))).toBe(true);
    }
  });

  it("still stops after two retries for a retryable code", () => {
    const error = new ConnectError("boom", Code.Unavailable);
    expect(shouldRetryQuery(1, error)).toBe(true);
    expect(shouldRetryQuery(2, error)).toBe(false);
  });
});

describe("shouldRetryQuery — plain HTTP errors", () => {
  it("does not retry an error carrying a 401/403/404 numeric status", () => {
    for (const status of [401, 403, 404]) {
      const error = Object.assign(new Error("Request failed"), { status });
      expect(shouldRetryQuery(0, error)).toBe(false);
    }
  });

  it("reads `statusCode` too — node-shaped HTTP errors use that name", () => {
    const error = Object.assign(new Error("Request failed"), { statusCode: 401 });
    expect(shouldRetryQuery(0, error)).toBe(false);
  });

  it("still honours a status in the message, as the original guard did", () => {
    expect(shouldRetryQuery(0, new Error("HTTP 401 Unauthorized"))).toBe(false);
    expect(shouldRetryQuery(0, new Error("Request failed with status 403"))).toBe(
      false,
    );
  });

  it("does not mistake an unrelated number for a status", () => {
    // The substring guard matched this; a bounded check must not. 4013 is not
    // 401, and a chat id that happens to contain 403 is not a permission error.
    expect(shouldRetryQuery(0, new Error("timed out after 4013ms"))).toBe(true);
    expect(shouldRetryQuery(0, new Error("chat 9403 not synced"))).toBe(true);
  });

  it("retries an ordinary error and a non-Error throw", () => {
    expect(shouldRetryQuery(0, new Error("network down"))).toBe(true);
    expect(shouldRetryQuery(0, "something threw a string" as unknown as Error)).toBe(
      true,
    );
  });
});
