// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The headline guarantee, self-tested: every Connect call carries a valid
// W3C traceparent, WITHOUT any OTLP collector configured, so the backend
// (otelconnect + otelhttp) joins the same distributed trace. If this test
// fails, full-stack tracing is broken.
import { context, createContextKey, ROOT_CONTEXT } from "@opentelemetry/api";
import { afterEach, describe, expect, it } from "vitest";

import { buildRuntimeInterceptors, traceInterceptor } from "./interceptors.js";
import {
  registerTelemetryStartup,
  resetTelemetryGate,
  telemetryReady,
} from "./telemetry-gate.js";
import {
  freshTraceparent,
  injectTraceContext,
  TRACEPARENT_RE,
} from "./trace.js";

const ZERO_TRACE_ID = "0".repeat(32);

describe("traceparent propagation", () => {
  it("injectTraceContext writes a valid, non-zero traceparent", () => {
    const header = new Headers();
    injectTraceContext(header);

    const tp = header.get("traceparent");
    expect(tp).not.toBeNull();
    expect(tp).toMatch(TRACEPARENT_RE);
    // trace id (2nd field) must not be all zeros per the W3C spec.
    expect(tp?.split("-")[1]).not.toBe(ZERO_TRACE_ID);
  });

  it("freshTraceparent is well-formed and unique per call", () => {
    const a = freshTraceparent();
    const b = freshTraceparent();
    expect(a).toMatch(TRACEPARENT_RE);
    expect(a).not.toBe(b);
  });

  it("the trace interceptor attaches traceparent to the outgoing request", async () => {
    // The runtime trace interceptor only reads/writes req.header, so a
    // minimal request shape exercises it end to end.
    const req = { header: new Headers(), stream: false };
    let propagated: string | null = null;

    const next = async (r: { header: Headers }) => {
      propagated = r.header.get("traceparent");
      return { message: {} };
    };

    const run = traceInterceptor() as unknown as (
      n: typeof next,
    ) => (r: typeof req) => Promise<unknown>;
    await run(next)(req);

    expect(propagated).not.toBeNull();
    expect(propagated).toMatch(TRACEPARENT_RE);
  });

  it("preserves each concurrent caller's context across auth and telemetry awaits", async () => {
    // Match the real SDK's stack-only context manager: native await restores
    // root context. Context restoration belongs to our async boundaries.
    let active = ROOT_CONTEXT;
    context.setGlobalContextManager({
      active: () => active,
      with: (parent, fn, thisArg, ...args) => {
        const previous = active;
        active = parent;
        try {
          return fn.apply(thisArg, args);
        } finally {
          active = previous;
        }
      },
      bind: (_parent, target) => target,
      enable() {
        return this;
      },
      disable() {
        return this;
      },
    });
    try {
      const key = createContextKey("caller");
      const seen: unknown[] = [];
      const interceptors = buildRuntimeInterceptors({
        getToken: async () => null,
      });
      const next: Parameters<(typeof interceptors)[number]>[0] = async () => {
        seen.push(context.active().getValue(key));
        return {} as never;
      };
      const run = interceptors.reduceRight(
        (call, interceptor) => interceptor(call),
        next,
      );
      await Promise.all(
        ["one", "two"].map((name) =>
          context.with(ROOT_CONTEXT.setValue(key, name), () =>
            run({ header: new Headers(), stream: false } as never),
          ),
        ),
      );
      expect(seen).toEqual(["one", "two"]);
      expect(context.active()).toBe(ROOT_CONTEXT);
    } finally {
      context.disable();
    }
  });

  it("buildRuntimeInterceptors composes the full stack", () => {
    // error-normalize → auth → brand headers → traceparent.
    // retryInterceptor is intentionally NOT in the default chain — React
    // Query is the single retry owner (see interceptors.ts).
    expect(buildRuntimeInterceptors()).toHaveLength(4);
  });
});

describe("traceInterceptor and browser telemetry startup", () => {
  type Req = { header: Headers };
  const run = async (): Promise<{
    calledAfterMs: number;
    traceparent: string | null;
  }> => {
    const req: Req = { header: new Headers() };
    const t0 = Date.now();
    let calledAfterMs = -1;
    let traceparent: string | null = null;
    const next = async (r: Req) => {
      calledAfterMs = Date.now() - t0;
      traceparent = r.header.get("traceparent");
      return { message: {} };
    };
    const go = traceInterceptor() as unknown as (
      n: typeof next,
    ) => (r: Req) => Promise<unknown>;
    await go(next)(req);
    return { calledAfterMs, traceparent };
  };

  afterEach(() => {
    resetTelemetryGate();
  });

  it("does not wait when telemetry is not starting", async () => {
    const { calledAfterMs, traceparent } = await run();
    expect(calledAfterMs).toBeLessThan(50);
    expect(traceparent).toMatch(TRACEPARENT_RE);
  });

  it("holds the call until the SDK has patched fetch, so the traceparent names a real browser span", async () => {
    let finish!: () => void;
    registerTelemetryStartup(new Promise<void>((r) => (finish = r)));
    setTimeout(finish, 120);
    const { calledAfterMs } = await run();
    expect(calledAfterMs).toBeGreaterThanOrEqual(100);
  });

  it("gives up after the bound when the SDK never starts (blocked chunk)", async () => {
    registerTelemetryStartup(new Promise<void>(() => undefined));
    const started = Date.now();
    await telemetryReady(60);
    expect(Date.now() - started).toBeGreaterThanOrEqual(50);
    expect(Date.now() - started).toBeLessThan(500);
  });

  it("stops waiting if startup fails", async () => {
    registerTelemetryStartup(Promise.reject(new Error("boom")));
    const { calledAfterMs, traceparent } = await run();
    expect(calledAfterMs).toBeLessThan(200);
    expect(traceparent).toMatch(TRACEPARENT_RE);
  });
});
