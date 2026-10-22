/**
 * The client must never be what ends the live update stream.
 *
 * Prod (read-path-latency, 2026-10-09): Envoy recorded the BROWSER resetting
 * 157 of 162 StreamUserUpdates streams, 25 of them at exactly 10.0s — the
 * length of the default unary budget (DEFAULT_GRPC_TIMEOUT_MS). Every one of
 * those was followed by a new stream and a ListChats/ListArchivedChats/
 * GetChat/ListDaemons refetch burst. A server-streaming call is open for as
 * long as the page is; its lifetime is its owner's to manage (heartbeat
 * watchdog, reconnect), never a request deadline.
 *
 * Pinned here against the real buildInterceptors() chain, with only the wire
 * faked, and against every server-streaming method the generated services
 * declare — so a stream added later cannot inherit the 10s default.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Interceptor } from "@connectrpc/connect";
import type { DescService } from "@bufbuild/protobuf";

vi.mock("@sentry/react", () => ({ captureException: vi.fn(), captureMessage: vi.fn() }));
vi.mock("@/lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() },
}));

type Next = Parameters<Interceptor>[0];
type Req = Parameters<Next>[0];

/** A wire whose stream answers with headers at once, then stays open. */
function makeStreamWire() {
  const requests: Req[] = [];
  const wire = (async (req: Req) => {
    requests.push(req);
    return {
      stream: true,
      service: req.service,
      method: req.method,
      header: new Headers(),
      trailer: new Headers(),
      // A response body that never yields and never ends: an open stream.
      message: { [Symbol.asyncIterator]: () => ({ next: () => new Promise(() => undefined) }) },
    };
  }) as unknown as Next;
  return { requests, wire };
}

async function openStream(service: string, method: string) {
  const { buildInterceptors } = await import("../transport");
  const { requests, wire } = makeStreamWire();
  const run = buildInterceptors({ withAuth: false }).reduceRight<Next>(
    (next, interceptor) => interceptor(next),
    wire,
  );
  await run({
    stream: true,
    service: { typeName: service },
    method: { name: method, methodKind: "server_streaming" },
    header: new Headers(),
    signal: new AbortController().signal,
    message: (async function* () {
      yield {};
    })(),
    url: `https://api.test/${service}/${method}`,
    requestMethod: "POST",
    contextValues: undefined,
  } as unknown as Req);
  return requests[0];
}

let visibility: DocumentVisibilityState = "visible";

beforeEach(() => {
  vi.resetModules();
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-10-09T01:00:00Z"));
  visibility = "visible";
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => visibility,
  });
});

afterEach(() => {
  vi.useRealTimers();
  delete (document as unknown as { visibilityState?: unknown }).visibilityState;
});

describe("the update stream through the real interceptor chain", () => {
  it("is not aborted at 10s, or at any later time", async () => {
    const wireReq = await openStream("reliant.v1.StreamingService", "StreamUserUpdates");

    await vi.advanceTimersByTimeAsync(10_000);
    expect(wireReq.signal.aborted).toBe(false);

    await vi.advanceTimersByTimeAsync(30 * 60_000);
    expect(wireReq.signal.aborted).toBe(false);
  });

  it("is not replaced by the transport when the page resumes or a connection is reported lost", async () => {
    // Stranded-read recovery is for unary reads. The stream's owner already
    // reacts to these (and owns its resume cursor); the transport must not.
    const wireReq = await openStream("reliant.v1.StreamingService", "StreamUserUpdates");

    visibility = "hidden";
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(60_000);
    visibility = "visible";
    document.dispatchEvent(new Event("visibilitychange"));
    const { pageActivity } = await import("../../lib/pageActivity");
    pageActivity().noteConnectionLost();
    await vi.advanceTimersByTimeAsync(10_000);

    expect(wireReq.signal.aborted).toBe(false);
  });
});

describe("every server-streaming RPC the web can call", () => {
  const modules = import.meta.glob<Record<string, unknown>>("../../gen/reliant/v1/*_pb.ts", {
    eager: true,
  });
  const streams: Array<{ service: string; method: string }> = [];
  for (const mod of Object.values(modules)) {
    for (const value of Object.values(mod)) {
      const service = value as Partial<DescService>;
      if (service?.kind !== "service" || !service.methods) continue;
      for (const method of service.methods) {
        if (method.methodKind === "server_streaming") {
          streams.push({ service: service.typeName!, method: method.name });
        }
      }
    }
  }

  it("includes the update stream (the scan works)", () => {
    expect(streams).toContainEqual({
      service: "reliant.v1.StreamingService",
      method: "StreamUserUpdates",
    });
  });

  it("has no client deadline", async () => {
    const { timeoutForProcedure } = await import("../transport");
    const withDeadline = streams.filter(
      ({ service, method }) => timeoutForProcedure(service, method) !== 0,
    );
    expect(withDeadline).toEqual([]);
  });
});
