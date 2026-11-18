// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Withdrawal means stop now: once consent is withdrawn, no session-replay
// record and no replay span leaves the page, including what the recorder had
// buffered before the withdrawal, a batch it was still encoding, and whatever
// stopping it emits.
//
// End to end through initBrowserTelemetry, the runtime's HyperDX client
// (sdk-client.ts) and its recorder (replay-recorder.ts), over a fake
// @hyperdx/otel-web and a fake rrweb whose emit() the test drives. Only the
// network is observed: every request that reaches it was sent.
// scripts/browser-telemetry-smoke.mjs proves the same against the real SDK
// and rrweb in Chromium.
import { gunzipSync } from "node:zlib";

import {
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vitest";

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

function fakeProvider() {
  return {
    getTracer(scope: string) {
      return {
        startSpan(name: string) {
          const span = {
            setAttribute: () => span,
            end() {
              exported.push({ kind: "span", scope, name, at: tick() });
            },
          };
          return span;
        },
      };
    },
  };
}

// @hyperdx/otel-web's Rum, as far as the client drives it.
const rum = {
  provider: undefined as
    | { resource: { attributes: Record<string, unknown> } }
    | undefined,
  init() {
    rum.provider = {
      resource: {
        attributes: { "service.name": "web", "rum.sessionId": "s1" },
      },
    };
  },
  recordException() {},
  addAction() {},
  setGlobalAttributes() {},
  getSessionId: () => "s1",
};
vi.mock("@hyperdx/otel-web", () => ({ default: rum, Rum: rum }));

// rrweb's record(), driven by the test.
const rrweb = {
  emit: undefined as ((event: unknown) => void) | undefined,
  stops: 0,
};
let timestamp = 1_700_000_000_000;
function emit(marker: string, size = 0) {
  rrweb.emit?.({
    type: 3,
    timestamp: ++timestamp,
    data: { source: 2, marker, pad: "x".repeat(size) },
  });
}
vi.mock("rrweb", () => {
  const record = Object.assign(
    (options: { emit(event: unknown): void }) => {
      rrweb.emit = options.emit;
      return () => {
        rrweb.stops++;
      };
    },
    {
      takeFullSnapshot: () => {},
      mirror: { getNode: () => null, getId: () => 0 },
    },
  );
  return { record };
});

class FakeDocument {
  jar = new Map<string, string>();
  referrer = "";
  visibilityState = "visible";
  hidden = false;
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

function bodyText(body: unknown): string | undefined {
  if (typeof body === "string") {
    return body;
  }
  if (body instanceof ArrayBuffer || body instanceof Uint8Array) {
    return gunzipSync(Buffer.from(body as ArrayBuffer)).toString("utf8");
  }
  return undefined;
}

// The network below the runtime: every request that reaches it was sent.
const network = vi.fn(async (input: unknown, init?: { body?: unknown }) => {
  exported.push({
    kind: "fetch",
    url: String(input),
    body: bodyText(init?.body),
    at: tick(),
  });
  return new Response(null, { status: 200 });
});

type Listener = (e: unknown) => void;
const windowListeners = new Map<string, Listener[]>();

function stubBrowser() {
  const local = new FakeStorage();
  const session = new FakeStorage();
  windowListeners.clear();
  vi.stubGlobal("document", new FakeDocument());
  vi.stubGlobal("navigator", {});
  vi.stubGlobal("localStorage", local);
  vi.stubGlobal("sessionStorage", session);
  vi.stubGlobal("window", {
    location: { origin: ORIGIN, host: "shop.example.com", pathname: "/" },
    localStorage: local,
    sessionStorage: session,
    history: { pushState: () => {}, replaceState: () => {} },
    addEventListener: (type: string, fn: Listener) =>
      windowListeners.set(type, [...(windowListeners.get(type) ?? []), fn]),
    removeEventListener: (type: string, fn: Listener) =>
      windowListeners.set(
        type,
        (windowListeners.get(type) ?? []).filter((l) => l !== fn),
      ),
  });
  vi.stubGlobal("fetch", network);
}

/** The page is being left: the recorder sends what it holds, now. */
function pagehide() {
  for (const fn of windowListeners.get("pagehide") ?? []) fn({});
}

async function startRecording() {
  stubBrowser();
  vi.resetModules();
  const { trace } = await import("@opentelemetry/api");
  trace.disable();
  trace.setGlobalTracerProvider(fakeProvider() as never);
  const t = await import("./telemetry.js");
  const k = await import("./track.js");
  k.grantAnalyticsConsent();
  await expect(
    t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      analytics: "full",
      replay: true,
      start: "eager",
    }),
  ).resolves.toBe(true);
  await vi.waitFor(() => expect(rrweb.emit).toBeDefined());
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

const settle = () => new Promise((resolve) => setTimeout(resolve, 50));

beforeAll(async () => {
  await import("@opentelemetry/api");
  await import("./telemetry.js");
  await import("./track.js");
}, 60_000);

beforeEach(() => {
  exported.length = 0;
  network.mockClear();
  rrweb.emit = undefined;
  rrweb.stops = 0;
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
    "%s: nothing recorded before it and still held is sent, and stopping exports no span",
    async (_name, withdraw) => {
      const k = await startRecording();
      // Replay runs while consented: a sent batch reaches the network.
      emit("before-1");
      pagehide();
      expect(
        exported.filter((e) => e.kind === "fetch" && e.url === LOGS_URL),
      ).toHaveLength(1);
      expect(JSON.stringify(exported)).toContain("before-1");
      // Recorded before the withdrawal, still held by the recorder.
      emit("before-2");

      const withdrawnAt = tick();
      withdraw(k);
      emit("after");
      // The page is left, and any timer the recorder had runs.
      pagehide();
      await settle();

      expect(replayAfter(withdrawnAt)).toEqual([]);
      expect(JSON.stringify(exported)).not.toContain("before-2");
      expect(JSON.stringify(exported)).not.toContain('"after"');
    },
  );

  it("a batch still being encoded when consent is withdrawn is never sent", async () => {
    const k = await startRecording();
    const since = tick();
    // Over REPLAY_MAX_PENDING_BYTES: sent in the background at once, which
    // encodes and gzips it before posting.
    emit("encoding", 600 * 1024);
    k.disableTelemetry();
    await settle();

    expect(replayAfter(since)).toEqual([]);
  });

  it("a batch sent in the background before the withdrawal does arrive, gzipped OTLP/JSON", async () => {
    await startRecording();
    const since = tick();
    emit("background", 600 * 1024);
    await vi.waitFor(() => expect(replayAfter(since)).toHaveLength(1));
    const sent = replayAfter(since)[0]!;
    const request = JSON.parse(sent.body!) as {
      resourceLogs: Array<{
        scopeLogs: Array<{ scope: { name: string }; logRecords: unknown[] }>;
      }>;
    };
    expect(request.resourceLogs[0]!.scopeLogs[0]!.scope.name).toBe(
      "rum.rr-web",
    );
    expect(sent.body).toContain("background");
  });

  it("only replay stops: traces, other OTLP logs and the app's own requests still go out", async () => {
    const k = await startRecording();
    k.disableTelemetry();
    const since = tick();

    await fetch(`${ORIGIN}/_otel/v1/traces`, { method: "POST", body: "{}" });
    await fetch(LOGS_URL, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ resourceLogs: [], note: "app-log-json" }),
    });
    await fetch("https://api.example.com/v1/orders", { method: "POST" });

    expect(
      exported
        .filter((e) => e.kind === "fetch" && e.at > since)
        .map((e) => e.url),
    ).toEqual([
      `${ORIGIN}/_otel/v1/traces`,
      LOGS_URL,
      "https://api.example.com/v1/orders",
    ]);
  });

  it("rrweb is stopped exactly once however consent is withdrawn, and nothing is exported for it", async () => {
    const k = await startRecording();
    const since = tick();
    k.revokeAnalyticsConsent();
    k.optOutOfAnalytics();
    k.disableTelemetry();
    await settle();

    expect(rrweb.stops).toBe(1);
    expect(replayAfter(since)).toEqual([]);
  });
});
