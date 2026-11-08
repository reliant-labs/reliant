// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The browser→server trace join: a failed RPC's ConnectClientError names the
// trace the SERVER recorded it in (its traceresponse header, forge/pkg/observe
// TraceResponseInterceptor), falling back to the traceparent this runtime
// sent; reportException carries it as an attribute.
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, describe, expect, it, vi } from "vitest";

import { reportException, setExceptionReporter } from "./error-reporter.js";
import { ConnectClientError, normalizeError } from "./errors.js";
import { buildRuntimeInterceptors } from "./interceptors.js";
import { TRACERESPONSE_HEADER, traceIdFrom } from "./trace.js";
import { SERVER_TRACE_ID_ATTRIBUTE, serverTraceIdOf } from "./trace-id.js";

const SERVER_TRACE = "4bf92f3577b34da6a3ce929d0e0e4736";
const TRACERESPONSE = `00-${SERVER_TRACE}-00f067aa0ba902b7-01`;

describe("traceIdFrom", () => {
  it("reads the trace id from a traceparent or traceresponse", () => {
    expect(traceIdFrom(TRACERESPONSE)).toBe(SERVER_TRACE);
    expect(traceIdFrom(` ${TRACERESPONSE} `)).toBe(SERVER_TRACE);
  });
  it("rejects malformed and all-zero values", () => {
    expect(traceIdFrom(null)).toBeNull();
    expect(traceIdFrom("garbage")).toBeNull();
    expect(traceIdFrom(`00-${"0".repeat(32)}-00f067aa0ba902b7-01`)).toBeNull();
  });
});

describe("ConnectClientError.traceId", () => {
  it("prefers the server's traceresponse over the request's traceparent", () => {
    const err = new ConnectError("nope", Code.FailedPrecondition, {
      [TRACERESPONSE_HEADER]: TRACERESPONSE,
    });
    expect(normalizeError(err, "a".repeat(32)).traceId).toBe(SERVER_TRACE);
  });
  it("falls back to the request's trace when the server sent none", () => {
    const err = new ConnectError("bad gateway", Code.Unavailable);
    expect(normalizeError(err, "a".repeat(32)).traceId).toBe("a".repeat(32));
    expect(
      normalizeError(new TypeError("Failed to fetch"), "b".repeat(32)).traceId,
    ).toBe("b".repeat(32));
    expect(normalizeError(err).traceId).toBeNull();
  });

  // Through the runtime's own interceptors: with no traceresponse, the error a
  // caller catches still names the trace of the traceparent it sent.
  it("is set on errors the runtime interceptors raise", async () => {
    const [errorNormalize, , , traceIC] = buildRuntimeInterceptors();
    if (!errorNormalize || !traceIC)
      throw new Error("interceptor chain changed shape");
    let sent = "";
    const call = errorNormalize(
      traceIC(async (req) => {
        sent = req.header.get("traceparent") ?? "";
        throw new ConnectError("down", Code.Unavailable);
      }),
    );
    const caught = await call({
      header: new Headers(),
      stream: false,
    } as never).catch((e: unknown) => e);
    expect(caught).toBeInstanceOf(ConnectClientError);
    expect((caught as ConnectClientError).traceId).not.toBeNull();
    expect((caught as ConnectClientError).traceId).toBe(traceIdFrom(sent));
  });
});

describe("reportException", () => {
  afterEach(() => setExceptionReporter(null));

  it("adds the server trace id of a failed RPC", () => {
    const recorder = vi.fn();
    setExceptionReporter(recorder);
    const err = normalizeError(
      new ConnectError("nope", Code.Internal, {
        [TRACERESPONSE_HEADER]: TRACERESPONSE,
      }),
    );
    expect(reportException(err, { route: "/jobs" })).toBe(true);
    expect(recorder).toHaveBeenCalledWith(err, {
      [SERVER_TRACE_ID_ATTRIBUTE]: SERVER_TRACE,
      route: "/jobs",
    });
  });

  it("leaves other errors' attributes alone, and lets the caller's win", () => {
    const recorder = vi.fn();
    setExceptionReporter(recorder);
    const plain = new Error("render failed");
    reportException(plain, { route: "/" });
    expect(recorder).toHaveBeenLastCalledWith(plain, { route: "/" });

    const wrapped = new Error("outer", { cause: { traceId: SERVER_TRACE } });
    expect(serverTraceIdOf(wrapped)).toBe(SERVER_TRACE);
    reportException(wrapped, { [SERVER_TRACE_ID_ATTRIBUTE]: "mine" });
    expect(recorder).toHaveBeenLastCalledWith(wrapped, {
      [SERVER_TRACE_ID_ATTRIBUTE]: "mine",
    });
  });
});
