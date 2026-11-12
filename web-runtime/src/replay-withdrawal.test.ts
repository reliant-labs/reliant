// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Withdrawal means stop now: once consent is withdrawn, no session-replay
// record and no replay span leaves the page, including what the recorder had
// buffered before the withdrawal and whatever stopping it emits.
//
// Pinned against a fake of the HyperDX SDK whose recorder behaves as the one
// bundled in @hyperdx/browser 0.26.0 (@hyperdx/otel-web-session-recorder
// 4.0.0) does: it buffers rrweb records and posts them to `${url}/v1/logs`
// with the global fetch on its own timer, its spans are on the `rum.rr-web`
// tracer (version 4.0.0), and a stop() on an already stopped recorder starts
// and ends a `record stop` span. scripts/browser-telemetry-smoke.mjs proves
// the same against the real SDK in Chromium.
import { gunzipSync, gzipSync } from "node:zlib";

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const ORIGIN = "https://shop.example.com";
const LOGS_URL = `${ORIGIN}/_otel/v1/logs`;

// One clock for every export, so "after the withdrawal" is an order, not a
// wall time a slow machine can blur.
let step = 0;
const tick = () => ++step;

interface Exported {
  kind: "span" | "fetch";
  scope?: string;
  name?: string;
  url?: string;
  body?: string;
  at: number;
}
const exported: Exported[] = [];

interface FakeSpan {
  attributes: Record<string, unknown>;
  setAttribute(k: string, v: unknown): FakeSpan;
  end(): void;
}

function fakeProvider() {
  const tracers = new Map<string, { startSpan(name: string): FakeSpan }>();
  return {
    getTracer(name: string, version?: string) {
      const key = `${name}@${version ?? ""}`;
      if (!tracers.has(key)) {
        tracers.set(key, {
          startSpan(spanName: string): FakeSpan {
            const span: FakeSpan = {
              attributes: {},
              setAttribute(k, v) {
                span.attributes[k] = v;
                return span;
              },
              end() {
                exported.push({
                  kind: "span",
                  scope: name,
                  name: spanName,
                  at: tick(),
                });
              },
            };
            return span;
          },
        });
      }
      return tracers.get(key)!;
    },
  };
}

// The recorder, as the SDK bundles it: emit() buffers while running, flush()
// is its batch timer (or its unload handler) posting the buffer with the
// global fetch, and stop() has the SDK's inverted guard.
const recorder = {
  inited: false,
  stopped: false,
  url: "",
  buffer: [] as string[],
  tracer: null as { startSpan(name: string): FakeSpan } | null,
  init(url: string, tracer: { startSpan(name: string): FakeSpan }) {
    this.inited = true;
    this.stopped = false;
    this.url = url;
    this.buffer = [];
    this.tracer = tracer;
    tracer.startSpan("record init").end();
  },
  emit(event: string) {
    if (this.inited && !this.stopped) {
      this.buffer.push(event);
    }
  },
  stop() {
    if (!this.inited) return;
    if (this.stopped) {
      this.tracer?.startSpan("record stop").end();
    }
    this.stopped = true;
  },
  flush() {
    const batch = this.buffer.splice(0, this.buffer.length);
    if (batch.length === 0) return;
    // A bare global lookup at export time, as the SDK's exporter does, with
    // its request shape: gzipped OTLP protobuf under the rum.rr-web scope.
    void fetch(
      this.url,
      otlpLogsPost("rum.rr-web", JSON.stringify(batch)),
    ).catch(() => {});
  },
};

/** A length-delimited protobuf field: tag, varint length, payload. */
function ld(field: number, payload: Buffer | string): Buffer {
  const bytes = Buffer.from(payload);
  const length: number[] = [];
  let n = bytes.length;
  do {
    length.push((n & 0x7f) | (n >= 0x80 ? 0x80 : 0));
    n >>>= 7;
  } while (n > 0);
  return Buffer.concat([Buffer.from([(field << 3) | 2, ...length]), bytes]);
}

/**
 * An OTLP logs post as the SDK's recorder makes it: a gzipped protobuf
 * ExportLogsServiceRequest, one ResourceLogs with one ScopeLogs whose
 * InstrumentationScope.name is `scope`, holding one LogRecord whose body is
 * `records` and which carries `attribute` as a string attribute value.
 */
function otlpLogsPost(
  scope: string,
  records: string,
  attribute = "",
): RequestInit {
  // LogRecord: body (5) AnyValue{string_value (1)}, attributes (6)
  // KeyValue{key (1), value (2) AnyValue{string_value (1)}}.
  const record = Buffer.concat([
    ld(5, ld(1, records)),
    ld(6, Buffer.concat([ld(1, "note"), ld(2, ld(1, attribute))])),
  ]);
  // ScopeLogs: scope (1) InstrumentationScope{name (1)}, log_records (2).
  const scopeLogs = Buffer.concat([ld(1, ld(1, scope)), ld(2, record)]);
  // ExportLogsServiceRequest: resource_logs (1) ResourceLogs{scope_logs (2)}.
  const body = gzipSync(ld(1, ld(2, scopeLogs)));
  return {
    method: "POST",
    body: new Uint8Array(body),
    headers: {
      "Content-Type": "application/x-protobuf",
      "Content-Encoding": "gzip",
    },
  };
}

const client = {
  init(options: Record<string, unknown>) {
    // The recorder holds its tracer from init on, as the real one does.
    if (options.disableReplay !== true) {
      recorder.init(
        `${String(options.url)}/v1/logs`,
        providerForInit.getTracer("rum.rr-web", "4.0.0"),
      );
    }
  },
  recordException: vi.fn(),
  addAction: vi.fn(),
  setGlobalAttributes: vi.fn(),
  stopSessionRecorder() {
    recorder.stop();
  },
  getSessionId: () => "s-1",
};

let providerForInit = fakeProvider();

vi.mock("@hyperdx/browser", () => ({ default: client }));

class FakeDocument {
  jar = new Map<string, string>();
  referrer = "";
  visibilityState = "visible";
  addEventListener(): void {}
  removeEventListener(): void {}
  get cookie(): string {
    return [...this.jar].map(([k, v]) => `${k}=${v}`).join("; ");
  }
  set cookie(text: string) {
    const [pair = "", ...attrs] = text.split(";");
    const eq = pair.indexOf("=");
    const name = pair.slice(0, eq).trim();
    const value = pair.slice(eq + 1).trim();
    const expired = attrs.some((a) => /^\s*max-age\s*=\s*0\s*$/i.test(a));
    if (expired || value === "") {
      this.jar.delete(name);
    } else {
      this.jar.set(name, value);
    }
  }
}

class FakeStorage {
  map = new Map<string, string>();
  get length(): number {
    return this.map.size;
  }
  key(i: number): string | null {
    return [...this.map.keys()][i] ?? null;
  }
  getItem(k: string): string | null {
    return this.map.get(k) ?? null;
  }
  setItem(k: string, v: string): void {
    this.map.set(k, String(v));
  }
  removeItem(k: string): void {
    this.map.delete(k);
  }
}

// The network below the runtime: every request that reaches it was sent.
const network = vi.fn(async (input: unknown, init?: { body?: unknown }) => {
  exported.push({
    kind: "fetch",
    url: String(input),
    body:
      typeof init?.body === "string"
        ? init.body
        : init?.body instanceof Uint8Array
          ? gunzipSync(init.body).toString("latin1")
          : undefined,
    at: tick(),
  });
  return new Response(null, { status: 200 });
});

function stubBrowser() {
  const local = new FakeStorage();
  const session = new FakeStorage();
  vi.stubGlobal("document", new FakeDocument());
  vi.stubGlobal("navigator", {});
  vi.stubGlobal("localStorage", local);
  vi.stubGlobal("sessionStorage", session);
  vi.stubGlobal("window", {
    location: { origin: ORIGIN, host: "shop.example.com", pathname: "/" },
    localStorage: local,
    sessionStorage: session,
    history: { pushState: () => {}, replaceState: () => {} },
    addEventListener: () => {},
    removeEventListener: () => {},
  });
  vi.stubGlobal("fetch", network);
}

async function startRecording() {
  stubBrowser();
  vi.resetModules();
  const { trace } = await import("@opentelemetry/api");
  trace.disable();
  providerForInit = fakeProvider();
  trace.setGlobalTracerProvider(providerForInit as never);
  const t = await import("./telemetry.js");
  const k = await import("./track.js");
  k.grantAnalyticsConsent();
  await expect(
    t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      analytics: "full",
      replay: true,
    }),
  ).resolves.toBe(true);
  return k;
}

/** Replay that left the page after `since`: record batches and rr-web spans. */
function replayAfter(since: number): Exported[] {
  return exported.filter(
    (e) =>
      e.at > since &&
      ((e.kind === "fetch" && e.url === LOGS_URL) ||
        (e.kind === "span" && e.scope === "rum.rr-web")),
  );
}

beforeEach(() => {
  exported.length = 0;
  network.mockClear();
  recorder.inited = false;
});

afterEach(async () => {
  vi.unstubAllGlobals();
  const { trace } = await import("@opentelemetry/api");
  trace.disable();
});

type Track = typeof import("./track.js");

const withdrawals: Array<[string, (k: Track) => void]> = [
  ["disableTelemetry()", (k) => k.disableTelemetry()],
  ["revokeAnalyticsConsent()", (k) => k.revokeAnalyticsConsent()],
  ["optOutOfAnalytics()", (k) => k.optOutOfAnalytics()],
  [
    "revokeAnalyticsConsent() then disableTelemetry()",
    (k) => {
      k.revokeAnalyticsConsent();
      k.disableTelemetry();
    },
  ],
];

describe("session replay after consent is withdrawn", () => {
  it.each(withdrawals)(
    "%s: nothing recorded before it and still buffered is sent, and stopping exports no span",
    async (_name, withdraw) => {
      const k = await startRecording();
      // Replay runs while consented: a flushed batch reaches the network.
      recorder.emit("before-1");
      recorder.flush();
      expect(
        exported.filter((e) => e.kind === "fetch" && e.url === LOGS_URL),
      ).toHaveLength(1);
      // Recorded before the withdrawal, still in the recorder's buffer.
      recorder.emit("before-2");

      const withdrawnAt = tick();
      withdraw(k);
      recorder.emit("after");
      // The recorder's batch timer (or its unload handler) fires.
      recorder.flush();
      await Promise.resolve();

      expect(replayAfter(withdrawnAt)).toEqual([]);
      expect(JSON.stringify(exported)).not.toContain("before-2");
    },
  );

  it("a later stop on the SDK itself exports no `record stop` span", async () => {
    const k = await startRecording();
    const withdrawnAt = tick();
    k.disableTelemetry();
    // Another caller (the app, an SDK path) stopping the recorder again.
    client.stopSessionRecorder();
    client.stopSessionRecorder();

    expect(replayAfter(withdrawnAt)).toEqual([]);
  });

  it("only replay is held back: traces and the app's own requests still go out", async () => {
    const k = await startRecording();
    k.disableTelemetry();
    const since = tick();

    await fetch(`${ORIGIN}/_otel/v1/traces`, { method: "POST", body: "{}" });
    await fetch("https://api.example.com/v1/orders", { method: "POST" });

    expect(
      exported
        .filter((e) => e.kind === "fetch" && e.at > since)
        .map((e) => e.url),
    ).toEqual([
      `${ORIGIN}/_otel/v1/traces`,
      "https://api.example.com/v1/orders",
    ]);
  });

  it("other OTLP logs to the same logs URL still go out after it", async () => {
    const k = await startRecording();
    k.disableTelemetry();
    const since = tick();

    // The app's own OTLP logs: protobuf under another scope, and JSON.
    await fetch(LOGS_URL, otlpLogsPost("app.logs", "app-log-protobuf"));
    await fetch(LOGS_URL, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ resourceLogs: [], note: "app-log-json" }),
    });

    const sent = exported.filter((e) => e.kind === "fetch" && e.at > since);
    expect(sent.map((e) => e.url)).toEqual([LOGS_URL, LOGS_URL]);
    expect(sent[0]?.body).toContain("app-log-protobuf");
    expect(sent[1]?.body).toContain("app-log-json");
  });

  it("an app log whose body or attributes hold the recorder's scope bytes still goes out", async () => {
    const k = await startRecording();
    k.disableTelemetry();
    const since = tick();

    // The exact bytes of InstrumentationScope.name = "rum.rr-web", but in a
    // log record's body and in an attribute value, not at the scope's path.
    const scopeBytes = ld(1, "rum.rr-web").toString("latin1");
    await fetch(
      LOGS_URL,
      otlpLogsPost("app.logs", `body ${scopeBytes}`, scopeBytes),
    );

    const sent = exported.filter((e) => e.kind === "fetch" && e.at > since);
    expect(sent.map((e) => e.url)).toEqual([LOGS_URL]);
    expect(sent[0]?.body).toContain("rum.rr-web");
  });

  it("a malformed protobuf log batch goes out as asked", async () => {
    const k = await startRecording();
    k.disableTelemetry();
    const since = tick();

    // Truncated: a length longer than what follows.
    await fetch(LOGS_URL, {
      method: "POST",
      body: new Uint8Array(gzipSync(Buffer.from([0x0a, 0x7f, 0x12]))),
      headers: {
        "Content-Type": "application/x-protobuf",
        "Content-Encoding": "gzip",
      },
    });

    expect(
      exported.filter((e) => e.kind === "fetch" && e.at > since),
    ).toHaveLength(1);
  });

  it.each([
    ["a trailing slash", `${LOGS_URL}/`],
    ["a relative URL", "/_otel/v1/logs"],
    ["a query string", `${LOGS_URL}?x=1`],
  ])(
    "a replay batch to the logs URL spelled with %s is dropped too",
    async (_name, url) => {
      const k = await startRecording();
      k.disableTelemetry();
      const since = tick();

      await fetch(url, otlpLogsPost("rum.rr-web", "spelled-differently"));

      expect(
        exported.filter((e) => e.kind === "fetch" && e.at > since),
      ).toEqual([]);
    },
  );

  it("the recorder is stopped exactly once however consent is withdrawn", async () => {
    const k = await startRecording();
    const stop = vi.spyOn(client, "stopSessionRecorder");
    k.revokeAnalyticsConsent();
    k.optOutOfAnalytics();
    k.disableTelemetry();

    expect(stop).toHaveBeenCalledTimes(1);
  });
});
