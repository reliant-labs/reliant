import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { ConnectClientError, normalizeError } from "@reliantlabs/forge-web-runtime";
import { setRuntimeConfig } from "@/test/runtimeConfig";

// A failure the app catches reaches the error pipeline only through these
// calls, so what they hand the runtime is pinned here.

const runtime = vi.hoisted(() => ({ reportException: vi.fn(() => true) }));
vi.mock("@reliantlabs/forge-web-runtime", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@reliantlabs/forge-web-runtime")>()),
  reportException: runtime.reportException,
}));

import { createDeadlineInterceptor } from "../../api/rpcDeadline";
import { pageActivity } from "../pageActivity";
import { reportError, reportFailure, reportTypedFailure } from "../errorReporting";

/**
 * True when an error carries no stack frame the browser SDK's stack parser
 * (Sentry's, inside @hyperdx/instrumentation-exception) could read: no
 * `exception.parsed_stacktrace` then leaves the browser, and the control plane
 * groups the issue by type and message alone (fingerprint.go, fpNoStack).
 */
function hasNoFrames(error: Error): boolean {
  const stack = error.stack ?? "";
  return !stack.includes("\n") && !/\bat\s|@|:\d+:\d+/.test(stack);
}

const ENDPOINT = "https://ingest.example.dev/k/rbk_x";

beforeEach(() => {
  vi.clearAllMocks();
  setRuntimeConfig({ OTEL_ENDPOINT: ENDPOINT });
});

describe("reportError", () => {
  it("hands the error and its context to the runtime, dropping unset values", () => {
    const err = new Error("boom");
    reportError(err, { component: "chat", operation: "load_messages", chat_id: undefined, attempt: 2 });
    expect(runtime.reportException).toHaveBeenCalledWith(err, {
      component: "chat",
      operation: "load_messages",
      attempt: 2,
    });
  });

  it("reports with no context", () => {
    const err = new Error("boom");
    reportError(err);
    expect(runtime.reportException).toHaveBeenCalledWith(err, undefined);
  });

  it("does nothing when the deployment ships no telemetry", () => {
    setRuntimeConfig({ OTEL_ENDPOINT: "" });
    reportError(new Error("boom"));
    expect(runtime.reportException).not.toHaveBeenCalled();
  });

  it("never throws when the runtime does", () => {
    runtime.reportException.mockImplementationOnce(() => {
      throw new Error("runtime exploded");
    });
    expect(() => reportError(new Error("boom"))).not.toThrow();
  });
});

describe("reportFailure", () => {
  it("reports an Error carrying the message, so it groups by it", () => {
    reportFailure("Codex OAuth state mismatch", { component: "oauth", provider: "codex" });
    const [error, attributes] = runtime.reportException.mock.calls[0] as unknown as [Error, unknown];
    expect(error).toBeInstanceOf(Error);
    expect(error.message).toBe("Codex OAuth state mismatch");
    expect(attributes).toEqual({ component: "oauth", provider: "codex" });
  });
});

describe("reportError of a client-side timeout", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  /** The ConnectError the transport's deadline aborts `method` with. */
  async function clientTimeout(method: string): Promise<ConnectError> {
    vi.useFakeTimers();
    const deadline = createDeadlineInterceptor({
      timeoutFor: () => 10_000,
      activity: pageActivity,
      onTimeout: () => {},
      stallGrouping: false,
    });
    // Like fetch: an aborted request rejects with the signal's reason.
    const wire = ((req: { signal: AbortSignal }) =>
      new Promise((_, reject) => {
        req.signal.addEventListener("abort", () => reject(req.signal.reason));
      })) as never;
    const run = deadline.interceptor(wire);
    let caught: unknown;
    void run({
      stream: false,
      service: { typeName: "reliant.v1.ChatService" },
      method: { name: method },
      header: new Headers(),
      signal: new AbortController().signal,
      message: {},
    } as never).catch((e: unknown) => (caught = e));
    await vi.advanceTimersByTimeAsync(10_100);
    expect(caught).toBeInstanceOf(ConnectError);
    return caught as ConnectError;
  }

  it("reports it frameless, under the caller's type and the deadline's message", async () => {
    const abort = await clientTimeout("ListMessages");
    // What a caller catches: the runtime's ConnectClientError wrapping it.
    const caught = normalizeError(abort, "0af7651916cd43dd8448eb211c80319c");
    expect(caught).toBeInstanceOf(ConnectClientError);

    reportError(caught, { component: "chat", operation: "load_messages" });

    const [error, attributes] = runtime.reportException.mock.calls[0] as unknown as [Error, unknown];
    expect(error).not.toBe(caught);
    expect(error.name).toBe("ConnectError");
    expect(error.message).toBe("ListMessages timed out after 10000ms");
    expect(hasNoFrames(error)).toBe(true);
    // No cause: the SDK would report the wrapped abort, frames and all.
    expect((error as Error & { cause?: unknown }).cause).toBeUndefined();
    expect(attributes).toEqual({
      "server.trace_id": "0af7651916cd43dd8448eb211c80319c",
      component: "chat",
      operation: "load_messages",
    });
  });

  it("reports the deadline's own ConnectError the same way, without its code framing", async () => {
    const abort = await clientTimeout("GetChat");
    reportError(abort);
    const [error, attributes] = runtime.reportException.mock.calls[0] as unknown as [Error, unknown];
    expect(error.name).toBe("ConnectError");
    expect(error.message).toBe("GetChat timed out after 10000ms");
    expect(hasNoFrames(error)).toBe(true);
    expect(attributes).toBeUndefined();
  });

  it("leaves a server's DeadlineExceeded alone: it is not the transport's own abort", () => {
    const serverError = new ConnectError("context deadline exceeded", Code.DeadlineExceeded);
    reportError(serverError);
    expect(runtime.reportException).toHaveBeenCalledWith(serverError, undefined);
  });
});

describe("reportTypedFailure", () => {
  it("reports an Error with no stack frames, so it groups by type and message alone", () => {
    reportTypedFailure("RpcTimeout", "rpc-timeout: ListInbox after 10000ms", {
      "rpc.method": "ListInbox",
      "rpc.timeout_ms": 10_000,
    });
    const [error, attributes] = runtime.reportException.mock.calls[0] as unknown as [Error, unknown];
    expect(error.name).toBe("RpcTimeout");
    // The method stays in the message, so each RPC keeps an issue of its own.
    expect(error.message).toBe("rpc-timeout: ListInbox after 10000ms");
    expect(error.stack).toBe("RpcTimeout: rpc-timeout: ListInbox after 10000ms");
    expect(hasNoFrames(error)).toBe(true);
    expect(attributes).toEqual({ "rpc.method": "ListInbox", "rpc.timeout_ms": 10_000 });
  });

  it("reports an Error named by the type, which becomes the issue's exception type", () => {
    reportTypedFailure("RpcStall", "connection stalled", { "rpc.stalled_count": 4 });
    const [error, attributes] = runtime.reportException.mock.calls[0] as unknown as [Error, unknown];
    expect(error).toBeInstanceOf(Error);
    expect(error.name).toBe("RpcStall");
    expect(error.message).toBe("connection stalled");
    // A `code` would take the exception type's place in the OTel span event.
    expect((error as Error & { code?: unknown }).code).toBeUndefined();
    expect(attributes).toEqual({ "rpc.stalled_count": 4 });
  });
});
