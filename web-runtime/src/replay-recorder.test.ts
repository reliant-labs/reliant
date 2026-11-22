// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The runtime's replay recorder, piece by piece: the OTLP/JSON encoder (and
// the Worker that runs it), the batcher, the transport's background and
// leaving paths, the mutation rate limiter, and the recorder's lifecycle
// (start after the first interaction, a session change, the 4 h cap,
// chunking, the flush on hide, stop).
import { gunzipSync } from "node:zlib";

import { afterEach, describe, expect, it, vi } from "vitest";

import {
  encodeReplayRequest,
  KEEPALIVE_MAX_BYTES,
  limitMessageSize,
  MAX_CHUNK_CHARS,
  MAX_RECORDING_MS,
  MutationRateLimiter,
  REPLAY_FLUSH_INTERVAL_MS,
  REPLAY_MAX_PENDING_BYTES,
  REPLAY_SCOPE,
  REPLAY_SNAPSHOT_FLUSH_MS,
  REPLAY_TAIL_MAX_BYTES,
  SNAPSHOT_WAIT_MS,
  ReplayBatcher,
  type ReplayRecord,
  type RrwebRecord,
  ReplayTransport,
  replayWorkerSource,
  startReplayRecorder,
  type WorkerLike,
} from "./replay-recorder.js";

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const record = (
  body: string,
  time = 1_700_000_000_123,
  i = 1,
): ReplayRecord => ({
  time,
  body,
  attributes: {
    "rr-web.offset": i,
    "rr-web.event": i,
    "rr-web.chunk": 1,
    "rr-web.total-chunks": 1,
  },
});

interface OtlpRequest {
  resourceLogs: Array<{
    resource: { attributes: Array<{ key: string; value: unknown }> };
    scopeLogs: Array<{
      scope: { name: string; version: string };
      logRecords: Array<{
        timeUnixNano: string;
        body: { stringValue: string };
        attributes: Array<{ key: string; value: unknown }>;
      }>;
    }>;
  }>;
}

describe("encodeReplayRequest", () => {
  it("round-trips every record as an OTLP/JSON ExportLogsServiceRequest of the recorder's scope", () => {
    const bodies = [
      JSON.stringify({
        type: 2,
        data: { node: { text: 'quote " and \\ slash' } },
      }),
      JSON.stringify({ type: 3, data: { text: "ünïcödé 🎉\nnewline\u2028" } }),
    ];
    const json = encodeReplayRequest(
      {
        "service.name": "web",
        "rum.sessionId": "abc",
        "telemetry.sdk.version": 1.5,
        "browser.mobile": false,
        list: ["a", 2],
        nested: { k: "v" },
      },
      REPLAY_SCOPE,
      bodies.map((b, i) => record(b, 1_763_000_000_001 + i, i + 1)),
    );
    const request = JSON.parse(json) as OtlpRequest;
    const resource = request.resourceLogs[0]!.resource.attributes;
    expect(resource).toEqual([
      { key: "service.name", value: { stringValue: "web" } },
      { key: "rum.sessionId", value: { stringValue: "abc" } },
      { key: "telemetry.sdk.version", value: { doubleValue: 1.5 } },
      { key: "browser.mobile", value: { boolValue: false } },
      {
        key: "list",
        value: {
          arrayValue: { values: [{ stringValue: "a" }, { doubleValue: 2 }] },
        },
      },
      {
        key: "nested",
        value: {
          kvlistValue: { values: [{ key: "k", value: { stringValue: "v" } }] },
        },
      },
    ]);
    const scope = request.resourceLogs[0]!.scopeLogs[0]!;
    expect(scope.scope).toEqual({ name: "rum.rr-web", version: "4.0.0" });
    expect(scope.logRecords.map((r) => r.body.stringValue)).toEqual(bodies);
    // 64-bit nanoseconds as a decimal string: exact, where ms·1e6 as a JSON
    // number is past 2^53 and would round.
    expect(scope.logRecords.map((r) => r.timeUnixNano)).toEqual([
      "1763000000001000000",
      "1763000000002000000",
    ]);
    expect(scope.logRecords[1]!.attributes).toEqual([
      { key: "rr-web.offset", value: { doubleValue: 2 } },
      { key: "rr-web.event", value: { doubleValue: 2 } },
      { key: "rr-web.chunk", value: { doubleValue: 1 } },
      { key: "rr-web.total-chunks", value: { doubleValue: 1 } },
    ]);
  });

  it("is the Worker's encoder too: the Worker gzips exactly what the main thread would send", async () => {
    // Run the Worker's program as a Worker would: `self` with onmessage.
    const posted: Array<{ data: Record<string, unknown> }> = [];
    const self: Record<string, unknown> = {
      postMessage: (data: Record<string, unknown>) => posted.push({ data }),
    };
    new Function("self", replayWorkerSource())(self);
    const resource = { "rum.sessionId": "s" };
    const records = [
      record('{"type":4}'),
      record('{"type":2}', 1_763_000_000_999, 2),
    ];
    await (self.onmessage as (e: unknown) => Promise<void>)({
      data: { id: 7, resource, scope: REPLAY_SCOPE, records },
    });
    expect(posted).toHaveLength(1);
    const { id, body } = posted[0]!.data as { id: number; body: ArrayBuffer };
    expect(id).toBe(7);
    expect(gunzipSync(Buffer.from(body)).toString("utf8")).toBe(
      encodeReplayRequest(resource, REPLAY_SCOPE, records),
    );
  });
});

describe("ReplayBatcher", () => {
  it(`sends what it holds ${REPLAY_FLUSH_INTERVAL_MS} ms after the first record, in one batch`, () => {
    vi.useFakeTimers();
    const sent: Array<[number, string]> = [];
    const b = new ReplayBatcher({
      send: (records, mode) => sent.push([records.length, mode]),
    });
    b.add(record("a"));
    vi.advanceTimersByTime(REPLAY_FLUSH_INTERVAL_MS - 1);
    b.add(record("b"));
    expect(sent).toEqual([]);
    vi.advanceTimersByTime(1);
    expect(sent).toEqual([[2, "background"]]);
    // Nothing held, no timer: nothing more is sent.
    vi.advanceTimersByTime(REPLAY_FLUSH_INTERVAL_MS * 3);
    expect(sent).toHaveLength(1);
  });

  it("sends at once when REPLAY_MAX_PENDING_BYTES are held", () => {
    vi.useFakeTimers();
    const sent: number[] = [];
    const b = new ReplayBatcher({
      send: (records) => sent.push(records.length),
    });
    b.add(record("x".repeat(REPLAY_MAX_PENDING_BYTES / 2)));
    expect(sent).toEqual([]);
    b.add(record("x".repeat(REPLAY_MAX_PENDING_BYTES / 2)));
    expect(sent).toEqual([2]);
    expect(b.size).toBe(0);
  });

  it("sends within REPLAY_SNAPSHOT_FLUSH_MS once REPLAY_TAIL_MAX_BYTES are held, so a page left loses little to the uncompressed tail", () => {
    vi.useFakeTimers();
    const sent: number[] = [];
    const b = new ReplayBatcher({
      send: (records) => sent.push(records.length),
    });
    b.add(record("x".repeat(REPLAY_TAIL_MAX_BYTES / 2)));
    vi.advanceTimersByTime(REPLAY_SNAPSHOT_FLUSH_MS);
    expect(sent).toEqual([]);
    b.add(record("x".repeat(REPLAY_TAIL_MAX_BYTES / 2)));
    vi.advanceTimersByTime(REPLAY_SNAPSHOT_FLUSH_MS - 1);
    expect(sent).toEqual([]);
    vi.advanceTimersByTime(1);
    expect(sent).toEqual([2]);
  });

  it("sends sooner when asked to, never later than already due", () => {
    vi.useFakeTimers();
    const sent: number[] = [];
    const b = new ReplayBatcher({
      send: (records) => sent.push(records.length),
    });
    b.flushWithin(10);
    b.add(record("a"));
    b.flushWithin(1000);
    vi.advanceTimersByTime(999);
    expect(sent).toEqual([]);
    vi.advanceTimersByTime(1);
    expect(sent).toEqual([1]);
    b.add(record("b"));
    b.flushWithin(100);
    b.flushWithin(5000);
    vi.advanceTimersByTime(100);
    expect(sent).toEqual([1, 1]);
  });

  it("flushes on demand in the mode asked, and drops what it holds on drop()", () => {
    vi.useFakeTimers();
    const sent: string[] = [];
    const b = new ReplayBatcher({ send: (_r, mode) => sent.push(mode) });
    b.flush("leaving");
    expect(sent).toEqual([]);
    b.add(record("a"));
    b.flush("leaving");
    b.add(record("b"));
    b.drop();
    vi.advanceTimersByTime(REPLAY_FLUSH_INTERVAL_MS * 2);
    expect(sent).toEqual(["leaving"]);
  });
});

type Sent = { url: string; init: RequestInit };

function fakeFetch(fail?: (init: RequestInit) => boolean) {
  const sent: Sent[] = [];
  const fn = vi.fn(async (url: string, init: RequestInit) => {
    sent.push({ url, init });
    if (fail?.(init)) {
      throw new TypeError("keepalive quota");
    }
    return {};
  });
  return { sent, fn };
}

function textOf(init: RequestInit): string {
  const body = init.body;
  if (typeof body === "string") {
    return body;
  }
  return gunzipSync(Buffer.from(body as ArrayBuffer)).toString("utf8");
}

// A Worker that runs the real program on the message, as a browser's would.
function inlineWorker(): WorkerLike & { posts: number } {
  const listeners = new Map<string, Array<(e: unknown) => void>>();
  const self: Record<string, unknown> = {
    postMessage: (data: unknown) => {
      for (const fn of listeners.get("message") ?? []) fn({ data });
    },
  };
  new Function("self", replayWorkerSource())(self);
  const worker = {
    posts: 0,
    postMessage(message: unknown) {
      worker.posts++;
      void (self.onmessage as (e: unknown) => Promise<void>)({ data: message });
    },
    addEventListener(type: string, fn: (e: unknown) => void) {
      listeners.set(type, [...(listeners.get(type) ?? []), fn]);
    },
    terminate() {},
  };
  return worker;
}

describe("ReplayTransport", () => {
  const URL_ = "https://shop.test/_otel/v1/logs";
  const resource = () => ({ "rum.sessionId": "s1" });

  it("in the background, encodes and gzips in the Worker and posts gzipped OTLP/JSON", async () => {
    const { sent, fn } = fakeFetch();
    const worker = inlineWorker();
    const t = new ReplayTransport({
      url: URL_,
      apiKey: "public",
      resource,
      fetch: fn,
      createWorker: () => worker,
    });
    t.send([record("one"), record("two")], "background");
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    expect(worker.posts).toBe(1);
    const { url, init } = sent[0]!;
    expect(url).toBe(URL_);
    expect(init.headers).toEqual({
      "content-type": "application/json",
      "content-encoding": "gzip",
      authorization: "public",
    });
    expect(init.keepalive).toBe(true);
    const request = JSON.parse(textOf(init)) as OtlpRequest;
    expect(
      request.resourceLogs[0]!.scopeLogs[0]!.logRecords.map(
        (r) => r.body.stringValue,
      ),
    ).toEqual(["one", "two"]);
  });

  it("falls back to the main thread when the Worker fails, and keeps falling back", async () => {
    const { sent, fn } = fakeFetch();
    let errorListener: ((e: unknown) => void) | undefined;
    const broken: WorkerLike = {
      postMessage: () => errorListener?.({}),
      addEventListener: (type, l) => {
        if (type === "error") errorListener = l;
      },
      terminate: () => {},
    };
    const created = vi.fn(() => broken);
    const t = new ReplayTransport({
      url: URL_,
      resource,
      fetch: fn,
      createWorker: created,
      yieldToMain: async () => {},
    });
    t.send([record("first")], "background");
    t.send([record("second")], "background");
    await vi.waitFor(() => expect(sent).toHaveLength(2));
    expect(created).toHaveBeenCalledTimes(1);
    expect(
      sent.map(
        (s) => (s.init.headers as Record<string, string>)["content-encoding"],
      ),
    ).toEqual(["gzip", "gzip"]);
    expect(sent.map((s) => textOf(s.init)).join()).toContain("first");
    expect(sent.map((s) => textOf(s.init)).join()).toContain("second");
  });

  it("posts uncompressed JSON where there is neither a Worker nor CompressionStream", async () => {
    vi.stubGlobal("CompressionStream", undefined);
    const { sent, fn } = fakeFetch();
    const t = new ReplayTransport({
      url: URL_,
      resource,
      fetch: fn,
      createWorker: () => null,
    });
    t.send([record("plain")], "background");
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]!.init.headers).toEqual({
      "content-type": "application/json",
    });
    expect(textOf(sent[0]!.init)).toContain("plain");
  });

  it("when the page is leaving, sends now with keepalive, in requests within the keepalive budget", () => {
    const { sent, fn } = fakeFetch();
    const t = new ReplayTransport({
      url: URL_,
      resource,
      fetch: fn,
      createWorker: () => {
        throw new Error("never used when leaving");
      },
    });
    const big = "y".repeat(20 * 1024);
    // ~100 KB: more than one keepalive request, more than the whole budget.
    t.send(
      [1, 2, 3, 4, 5].map((i) => record(`${big}${i}`, 1, i)),
      "leaving",
    );
    // Synchronous: every request is issued before send() returns.
    expect(sent.length).toBeGreaterThan(1);
    let keepaliveBytes = 0;
    for (const { init } of sent) {
      const bytes = (init.body as string).length;
      if (init.keepalive) {
        keepaliveBytes += bytes;
        expect(bytes).toBeLessThanOrEqual(KEEPALIVE_MAX_BYTES);
      }
    }
    expect(keepaliveBytes).toBeLessThanOrEqual(KEEPALIVE_MAX_BYTES);
    expect(sent.some((s) => s.init.keepalive === false)).toBe(true);
    const all = sent.map((s) => textOf(s.init)).join();
    for (const i of [1, 2, 3, 4, 5]) expect(all).toContain(`${big}${i}`);
  });

  it("when the page is leaving, sizes each request by its encoded UTF-8 bytes, so escaped or multi-byte text never pushes a group over the keepalive cap", () => {
    const { sent, fn } = fakeFetch();
    const t = new ReplayTransport({ url: URL_, resource, fetch: fn });
    // 16 Ki characters each, 32 KiB each once encoded: a quote is escaped to
    // two bytes, an é is two bytes of UTF-8. Counted in characters the two
    // fit one request; encoded, together they are over the cap.
    const quoted = '"'.repeat(16 * 1024);
    const accented = "é".repeat(16 * 1024);
    t.send([record(quoted, 1, 1), record(accented, 1, 2)], "leaving");
    const utf8 = (s: string) => new TextEncoder().encode(s).length;
    expect(sent).toHaveLength(2);
    for (const { init } of sent) {
      expect(utf8(init.body as string)).toBeLessThanOrEqual(
        KEEPALIVE_MAX_BYTES,
      );
    }
    // The first fits the whole budget, so it outlives the page.
    expect(sent[0]!.init.keepalive).toBe(true);
    const bodies = sent.flatMap((s) =>
      (
        JSON.parse(textOf(s.init)) as OtlpRequest
      ).resourceLogs[0]!.scopeLogs[0]!.logRecords.map(
        (r) => r.body.stringValue,
      ),
    );
    expect(bodies).toEqual([quoted, accented]);
  });

  it("fits any one record of at most MAX_CHUNK_CHARS in a keepalive request, however its text encodes", () => {
    const fullResource = () => ({
      "rum.sessionId": "0123456789abcdef0123456789abcdef",
      "service.name": "a-rather-long-frontend-service-name",
      "service.version": "v1.2.3-0.20261118040000-0123456789ab",
      "deployment.environment": "production",
    });
    const utf8 = (s: string) => new TextEncoder().encode(s).length;
    // A character is at most 3 bytes of UTF-8 (€), a quote 2 once escaped,
    // and a surrogate pair cut at a chunk's edges 6 escaped bytes a half.
    const pair = "😀";
    for (const body of [
      "€".repeat(MAX_CHUNK_CHARS),
      '"'.repeat(MAX_CHUNK_CHARS),
      pair[1] + "€".repeat(MAX_CHUNK_CHARS - 2) + pair[0],
    ]) {
      const json = encodeReplayRequest(fullResource(), REPLAY_SCOPE, [
        {
          ...record(body, Number.MAX_SAFE_INTEGER, 1_000_000_000),
          attributes: {
            "rr-web.offset": 1_000_000_000,
            "rr-web.event": 1_000_000_000,
            "rr-web.chunk": 1_000_000,
            "rr-web.total-chunks": 1_000_000,
          },
        },
      ]);
      expect(utf8(json)).toBeLessThanOrEqual(KEEPALIVE_MAX_BYTES);
    }
  });

  it("when hidden, gzips in the background and still sends with keepalive", async () => {
    const { sent, fn } = fakeFetch();
    const t = new ReplayTransport({
      url: URL_,
      resource,
      fetch: fn,
      createWorker: inlineWorker,
    });
    t.send([record("hidden")], "hidden");
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]!.init.keepalive).toBe(true);
    expect(
      (sent[0]!.init.headers as Record<string, string>)["content-encoding"],
    ).toBe("gzip");
  });

  it("on unload, sends a batch still encoding now, uncompressed, and never its background result too", async () => {
    const { sent, fn } = fakeFetch();
    let reply: ((e: unknown) => void) | undefined;
    const slow: WorkerLike = {
      postMessage: () => {},
      addEventListener: (type, l) => {
        if (type === "message") reply = l;
      },
      terminate: () => {},
    };
    const t = new ReplayTransport({
      url: URL_,
      resource,
      fetch: fn,
      createWorker: () => slow,
    });
    t.send([record("encoding")], "hidden");
    await new Promise((r) => setTimeout(r, 5));
    expect(sent).toEqual([]);
    t.flushInFlight();
    expect(sent).toHaveLength(1);
    expect(sent[0]!.init.keepalive).toBe(true);
    expect(textOf(sent[0]!.init)).toContain("encoding");
    // The Worker finishes after all: nothing more is sent.
    reply?.({ data: { id: 1, json: "late" } });
    await new Promise((r) => setTimeout(r, 10));
    expect(sent).toHaveLength(1);
  });

  it("retries without keepalive a request the browser refuses for its keepalive budget while the page lives", async () => {
    const { sent, fn } = fakeFetch((init) => init.keepalive === true);
    const t = new ReplayTransport({
      url: URL_,
      resource,
      fetch: fn,
      createWorker: inlineWorker,
    });
    t.send([record("r")], "hidden");
    await vi.waitFor(() => expect(sent).toHaveLength(2));
    expect(sent.map((s) => s.init.keepalive)).toEqual([true, false]);
  });

  it("sends nothing closed while the main thread was still gzipping it", async () => {
    const { sent, fn } = fakeFetch();
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => (release = r));
    const t = new ReplayTransport({
      url: URL_,
      resource,
      fetch: fn,
      createWorker: () => null,
      // The fallback yields between gzip slices: hold it at the first.
      yieldToMain: () => gate,
    });
    t.send([record("mid-gzip")], "background");
    await new Promise((r) => setTimeout(r, 10));
    t.close();
    release();
    await new Promise((r) => setTimeout(r, 30));
    expect(sent).toEqual([]);
  });

  it("never resends a keepalive request the browser rejects while the page unloads: it was sent", async () => {
    const { sent, fn } = fakeFetch((init) => init.keepalive === true);
    const t = new ReplayTransport({ url: URL_, resource, fetch: fn });
    t.send([record("left")], "leaving");
    await new Promise((r) => setTimeout(r, 20));
    expect(sent.map((s) => s.init.keepalive)).toEqual([true]);
  });

  it("retries again once the page is restored from the back-forward cache", async () => {
    const { sent, fn } = fakeFetch((init) => init.keepalive === true);
    const t = new ReplayTransport({
      url: URL_,
      resource,
      fetch: fn,
      createWorker: inlineWorker,
    });
    t.send([record("left")], "leaving");
    t.restored();
    t.send([record("back")], "hidden");
    await vi.waitFor(() => expect(sent).toHaveLength(3));
    expect(sent.map((s) => s.init.keepalive)).toEqual([true, true, false]);
  });

  it("sends nothing once closed, not even a batch it was encoding", async () => {
    const { sent, fn } = fakeFetch();
    const worker = inlineWorker();
    const t = new ReplayTransport({
      url: URL_,
      resource,
      fetch: fn,
      createWorker: () => worker,
    });
    t.send([record("in-flight")], "background");
    t.close();
    t.send([record("after")], "leaving");
    await new Promise((r) => setTimeout(r, 30));
    expect(sent).toEqual([]);
  });
});

describe("MutationRateLimiter", () => {
  const mutation = (ids: number[]) => ({
    type: 3,
    timestamp: 1,
    data: { source: 0, attributes: ids.map((id) => ({ id })) },
  });

  it("cuts a node's attribute mutations past its bucket, tells once, and drops an event left empty", () => {
    vi.useFakeTimers();
    const blocked = vi.fn();
    const limiter = new MutationRateLimiter(
      { getNode: () => null, getId: () => 0 },
      { bucketSize: 3, refillRate: 1, onBlockedNode: blocked },
    );
    for (let i = 0; i < 3; i++) {
      expect(limiter.throttle(mutation([7]))).toBeDefined();
    }
    expect(blocked).toHaveBeenCalledTimes(1);
    expect(blocked).toHaveBeenCalledWith(7, null);
    // Bucket empty: node 7 alone is dropped whole, node 8 still records.
    expect(limiter.throttle(mutation([7]))).toBeUndefined();
    const mixed = limiter.throttle(mutation([7, 8]));
    expect(mixed?.data.attributes).toEqual([{ id: 8 }]);
    // Other events pass untouched.
    const snapshot = { type: 2, timestamp: 1, data: {} };
    expect(limiter.throttle(snapshot)).toBe(snapshot);
    // A second of rest refills one.
    vi.advanceTimersByTime(1000);
    expect(limiter.throttle(mutation([7]))).toBeDefined();
    expect(blocked).toHaveBeenCalledTimes(1);
    limiter.stop();
  });

  it("counts an svg's descendants against the svg", () => {
    vi.useFakeTimers();
    const svg = { nodeName: "svg" };
    const path = { nodeName: "path", closest: () => svg };
    const limiter = new MutationRateLimiter(
      {
        getNode: (id) => (id === 2 ? (path as unknown as Node) : null),
        getId: (n) => (n === (svg as unknown) ? 1 : 0),
      },
      { bucketSize: 2 },
    );
    expect(limiter.throttle(mutation([2]))).toBeDefined();
    expect(limiter.throttle(mutation([1]))).toBeDefined();
    // svg 1's bucket is spent by the path and the svg together.
    expect(limiter.throttle(mutation([2]))).toBeUndefined();
    limiter.stop();
  });
});

describe("limitMessageSize", () => {
  it("leaves an ordinary event alone, and strips data: URIs from an oversized one", () => {
    expect(limitMessageSize('{"a":"data:image/png;base64,AAAA"}')).toBe(
      '{"a":"data:image/png;base64,AAAA"}',
    );
    const big = `{"img":"data:image/png;base64,${"A".repeat(5_000_001)}","font":"data:font/woff2;base64,BBBB"}`;
    const out = limitMessageSize(big);
    expect(out.length).toBeLessThan(2000);
    expect(out).toContain('"img":"data:image/svg+xml;base64,');
    expect(out).toContain('"font":""');
  });
});

// rrweb's record(), driven by the test.
function fakeRrweb() {
  const state = {
    emit: undefined as ((event: unknown) => void) | undefined,
    options: undefined as Record<string, unknown> | undefined,
    snapshots: 0,
    stops: 0,
  };
  const record = Object.assign(
    (options: Record<string, unknown>) => {
      state.options = options;
      state.emit = options.emit as (event: unknown) => void;
      return () => {
        state.stops++;
      };
    },
    {
      takeFullSnapshot: () => {
        state.snapshots++;
      },
      mirror: { getNode: () => null, getId: () => 0 },
    },
  ) as unknown as RrwebRecord;
  return { state, record };
}

type Listener = (e: unknown) => void;

function stubPage() {
  const win = new Map<string, Listener[]>();
  const doc = new Map<string, Listener[]>();
  const add = (m: Map<string, Listener[]>) => (type: string, fn: Listener) =>
    m.set(type, [...(m.get(type) ?? []), fn]);
  const remove = (m: Map<string, Listener[]>) => (type: string, fn: Listener) =>
    m.set(
      type,
      (m.get(type) ?? []).filter((l) => l !== fn),
    );
  const document = {
    visibilityState: "visible",
    hidden: false,
    addEventListener: add(doc),
    removeEventListener: remove(doc),
  };
  vi.stubGlobal("document", document);
  vi.stubGlobal("window", {
    addEventListener: add(win),
    removeEventListener: remove(win),
  });
  return {
    document,
    fire: (type: string) => {
      for (const fn of win.get(type) ?? []) fn({});
    },
    hide: () => {
      document.visibilityState = "hidden";
      document.hidden = true;
      for (const fn of doc.get("visibilitychange") ?? []) fn({});
    },
    listening: () =>
      [...win.values(), ...doc.values()].reduce((n, l) => n + l.length, 0),
  };
}

function fakeTransport() {
  const batches: Array<{ records: ReplayRecord[]; mode: string }> = [];
  let closed = 0;
  return {
    batches,
    closed: () => closed,
    transport: {
      send: (records: ReplayRecord[], mode: "background" | "leaving") =>
        batches.push({ records, mode }),
      close: () => {
        closed++;
      },
    },
  };
}

const base = {
  url: "https://shop.test/_otel/v1/logs",
  rrweb: { slimDOMOptions: "all" },
  resource: () => ({}),
};

describe("startReplayRecorder", () => {
  it("records each event as numbered records, masked by default, and sends them when the page is hidden", async () => {
    const page = stubPage();
    const { state, record } = fakeRrweb();
    const { batches, transport } = fakeTransport();
    const rec = startReplayRecorder({
      ...base,
      sessionId: () => "s1",
      loadRecord: async () => record,
      transport,
    });
    expect(await rec.started).toBe(true);
    expect(state.options).toMatchObject({
      maskAllInputs: true,
      maskTextSelector: "*",
      slimDOMOptions: "all",
    });
    state.emit!({ type: 4, timestamp: Date.now(), data: { href: "x" } });
    state.emit!({ type: 2, timestamp: Date.now(), data: {} });
    expect(batches).toEqual([]);
    page.hide();
    expect(batches).toHaveLength(1);
    // Hidden, not gone: gzipped in the background, with keepalive.
    expect(batches[0]!.mode).toBe("hidden");
    expect(batches[0]!.records.map((r) => r.attributes)).toEqual([
      {
        "rr-web.offset": 1,
        "rr-web.event": 1,
        "rr-web.chunk": 1,
        "rr-web.total-chunks": 1,
      },
      {
        "rr-web.offset": 2,
        "rr-web.event": 2,
        "rr-web.chunk": 1,
        "rr-web.total-chunks": 1,
      },
    ]);
    expect(JSON.parse(batches[0]!.records[0]!.body)).toEqual({
      type: 4,
      timestamp: expect.any(Number),
      data: { href: "x" },
    });
    rec.stop();
  });

  it("on pagehide, also hands over what the transport is still encoding", async () => {
    const page = stubPage();
    const { record } = fakeRrweb();
    const { transport } = fakeTransport();
    const flushInFlight = vi.fn();
    const rec = startReplayRecorder({
      ...base,
      sessionId: () => "s1",
      loadRecord: async () => record,
      transport: { ...transport, flushInFlight },
    });
    await rec.started;
    page.hide();
    expect(flushInFlight).not.toHaveBeenCalled();
    page.fire("pagehide");
    expect(flushInFlight).toHaveBeenCalledTimes(1);
    rec.stop();
  });

  it("sends on pagehide too, and splits an event over MAX_CHUNK_CHARS into chunks", async () => {
    const page = stubPage();
    const { state, record } = fakeRrweb();
    const { batches, transport } = fakeTransport();
    const rec = startReplayRecorder({
      ...base,
      sessionId: () => "s1",
      loadRecord: async () => record,
      transport,
    });
    await rec.started;
    state.emit!({
      type: 2,
      timestamp: Date.now(),
      data: { big: "z".repeat(MAX_CHUNK_CHARS + 10) },
    });
    page.fire("pagehide");
    const records = batches.flatMap((b) => b.records);
    expect(records.map((r) => r.attributes)).toEqual([
      {
        "rr-web.offset": 1,
        "rr-web.event": 1,
        "rr-web.chunk": 1,
        "rr-web.total-chunks": 2,
      },
      {
        "rr-web.offset": 2,
        "rr-web.event": 1,
        "rr-web.chunk": 2,
        "rr-web.total-chunks": 2,
      },
    ]);
    expect(records[0]!.body.length).toBe(MAX_CHUNK_CHARS);
    expect(JSON.parse(records.map((r) => r.body).join(""))).toMatchObject({
      type: 2,
    });
    rec.stop();
  });

  it(`sends a full snapshot in the background within ${REPLAY_SNAPSHOT_FLUSH_MS} ms, not at the interval`, async () => {
    vi.useFakeTimers();
    stubPage();
    const { state, record } = fakeRrweb();
    const { batches, transport } = fakeTransport();
    const rec = startReplayRecorder({
      ...base,
      sessionId: () => "s1",
      loadRecord: async () => record,
      transport,
    });
    await rec.started;
    state.emit!({ type: 4, timestamp: Date.now(), data: {} });
    state.emit!({ type: 2, timestamp: Date.now(), data: {} });
    await vi.advanceTimersByTimeAsync(REPLAY_SNAPSHOT_FLUSH_MS);
    expect(batches.map((b) => [b.mode, b.records.length])).toEqual([
      ["background", 2],
    ]);
    // An incremental event waits for the interval.
    state.emit!({ type: 3, timestamp: Date.now(), data: {} });
    await vi.advanceTimersByTimeAsync(REPLAY_SNAPSHOT_FLUSH_MS * 2);
    expect(batches).toHaveLength(1);
    rec.stop();
  });

  it("takes a full snapshot and renumbers when the RUM session changes, and records nothing past the 4 h cap", async () => {
    vi.useFakeTimers();
    stubPage();
    const { state, record } = fakeRrweb();
    const { batches, transport } = fakeTransport();
    let session = "s1";
    let now = 1_000_000;
    const rec = startReplayRecorder({
      ...base,
      sessionId: () => session,
      loadRecord: async () => record,
      transport,
      now: () => now,
    });
    await rec.started;
    state.emit!({ type: 3, timestamp: now, data: {} });
    state.emit!({ type: 3, timestamp: now + MAX_RECORDING_MS + 1, data: {} });
    session = "s2";
    now += 10;
    state.emit!({ type: 3, timestamp: now, data: {} });
    expect(state.snapshots).toBe(1);
    // This fake takes no snapshot: the new session's event waits for one, then goes anyway.
    await vi.advanceTimersByTimeAsync(SNAPSHOT_WAIT_MS);
    rec.flush();
    expect(
      batches[0]!.records.map((r) => r.attributes["rr-web.event"]),
    ).toEqual([1, 1]);
    rec.stop();
  });

  it("batches a new session's Meta and full snapshot before the event that found the session", async () => {
    stubPage();
    const { state, record } = fakeRrweb();
    // As rrweb 2.x does: record.takeFullSnapshot() emits Meta, then the
    // FullSnapshot, synchronously, through the same emit callback.
    (record as unknown as { takeFullSnapshot: () => void }).takeFullSnapshot =
      () => {
        state.snapshots++;
        state.emit!({ type: 4, timestamp: 2, data: { href: "/" } });
        state.emit!({ type: 2, timestamp: 2, data: { node: {} } });
      };
    const { batches, transport } = fakeTransport();
    let session = "s1";
    const rec = startReplayRecorder({
      ...base,
      sessionId: () => session,
      loadRecord: async () => record,
      transport,
    });
    await rec.started;
    state.emit!({ type: 3, timestamp: 1, data: {} });
    rec.flush();
    batches.length = 0;
    session = "s2";
    state.emit!({ type: 3, timestamp: 3, data: { source: 2 } });
    rec.flush();
    const records = batches.flatMap((b) => b.records);
    expect(state.snapshots).toBe(1);
    expect(
      records.map((r) => (JSON.parse(r.body) as { type: number }).type),
    ).toEqual([4, 2, 3]);
    expect(records.map((r) => r.attributes["rr-web.event"])).toEqual([1, 2, 3]);
    expect(records.map((r) => r.attributes["rr-web.offset"])).toEqual([
      1, 2, 3,
    ]);
    rec.stop();
  });

  it("holds a new session's events until its full snapshot is recorded, even when the snapshot comes later", async () => {
    vi.useFakeTimers();
    stubPage();
    const { state, record } = fakeRrweb();
    // A recorder that emitted the snapshot asynchronously.
    (record as unknown as { takeFullSnapshot: () => void }).takeFullSnapshot =
      () => {
        state.snapshots++;
        setTimeout(() => {
          state.emit!({ type: 4, timestamp: 2, data: { href: "/" } });
          state.emit!({ type: 2, timestamp: 2, data: { node: {} } });
        }, 50);
      };
    const { batches, transport } = fakeTransport();
    let session = "s1";
    const rec = startReplayRecorder({
      ...base,
      sessionId: () => session,
      loadRecord: async () => record,
      transport,
    });
    await rec.started;
    session = "s2";
    state.emit!({ type: 3, timestamp: 3, data: { source: 2 } });
    state.emit!({ type: 3, timestamp: 4, data: { source: 2 } });
    rec.flush();
    expect(batches.flatMap((b) => b.records)).toEqual([]);
    await vi.advanceTimersByTimeAsync(50);
    rec.flush();
    const records = batches.flatMap((b) => b.records);
    expect(
      records.map((r) => (JSON.parse(r.body) as { type: number }).type),
    ).toEqual([4, 2, 3, 3]);
    expect(records.map((r) => r.attributes["rr-web.event"])).toEqual([
      1, 2, 3, 4,
    ]);
    rec.stop();
  });

  it(`records a new session's events anyway after ${SNAPSHOT_WAIT_MS} ms if no snapshot comes`, async () => {
    vi.useFakeTimers();
    stubPage();
    const { state, record } = fakeRrweb();
    const { batches, transport } = fakeTransport();
    let session = "s1";
    const rec = startReplayRecorder({
      ...base,
      sessionId: () => session,
      loadRecord: async () => record,
      transport,
    });
    await rec.started;
    session = "s2";
    state.emit!({ type: 3, timestamp: 3, data: { source: 2 } });
    await vi.advanceTimersByTimeAsync(SNAPSHOT_WAIT_MS);
    rec.flush();
    expect(state.snapshots).toBe(1);
    expect(
      batches
        .flatMap((b) => b.records)
        .map((r) => r.attributes["rr-web.event"]),
    ).toEqual([1]);
    rec.stop();
  });

  it("with start: interaction, loads and records nothing until the first interaction has painted", async () => {
    vi.useFakeTimers();
    const page = stubPage();
    const { state, record } = fakeRrweb();
    const loadRecord = vi.fn(async () => record);
    const { transport } = fakeTransport();
    const rec = startReplayRecorder({
      ...base,
      sessionId: () => "s1",
      start: "interaction",
      loadRecord,
      transport,
    });
    await vi.advanceTimersByTimeAsync(60_000);
    expect(loadRecord).not.toHaveBeenCalled();

    page.fire("pointerdown");
    // Not in the interaction's own task: after its frame.
    expect(loadRecord).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(300);
    expect(loadRecord).toHaveBeenCalledTimes(1);
    await expect(rec.started).resolves.toBe(true);
    expect(state.emit).toBeDefined();
    // One start, whatever follows.
    page.fire("keydown");
    await vi.advanceTimersByTimeAsync(300);
    expect(loadRecord).toHaveBeenCalledTimes(1);
    rec.stop();
  });

  it("with start: interaction, stopped before any interaction, never loads rrweb", async () => {
    vi.useFakeTimers();
    const page = stubPage();
    const loadRecord = vi.fn(async () => fakeRrweb().record);
    const { transport } = fakeTransport();
    const rec = startReplayRecorder({
      ...base,
      sessionId: () => "s1",
      start: "interaction",
      loadRecord,
      transport,
    });
    rec.stop();
    await expect(rec.started).resolves.toBe(false);
    page.fire("pointerdown");
    await vi.advanceTimersByTimeAsync(1000);
    expect(loadRecord).not.toHaveBeenCalled();
    expect(page.listening()).toBe(0);
  });

  it("stop() is final: rrweb stops once, what is held is dropped, the transport closes, the listeners go", async () => {
    vi.useFakeTimers();
    const page = stubPage();
    const { state, record } = fakeRrweb();
    const { batches, transport, closed } = fakeTransport();
    const rec = startReplayRecorder({
      ...base,
      sessionId: () => "s1",
      loadRecord: async () => record,
      transport,
    });
    await rec.started;
    state.emit!({ type: 3, timestamp: Date.now(), data: { held: true } });
    rec.stop();
    rec.stop();
    state.emit!({ type: 3, timestamp: Date.now(), data: {} });
    page.hide();
    page.fire("pagehide");
    // Past the batch timer the held record would have gone out on.
    await vi.advanceTimersByTimeAsync(REPLAY_FLUSH_INTERVAL_MS * 2);
    expect(batches).toEqual([]);
    expect(state.stops).toBe(1);
    expect(closed()).toBe(1);
    expect(page.listening()).toBe(0);
  });

  it("resolves started to false, and throws nothing, when rrweb cannot load", async () => {
    stubPage();
    const rec = startReplayRecorder({
      ...base,
      sessionId: () => "s1",
      loadRecord: async () => {
        throw new Error("chunk blocked");
      },
      transport: fakeTransport().transport,
    });
    await expect(rec.started).resolves.toBe(false);
    rec.stop();
  });
});
