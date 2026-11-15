// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The contract of initBrowserTelemetry, pinned against a fake of the HyperDX
// SDK: SSR is a no-op, init is idempotent, the ingest URL is same-origin,
// replay is off by default and masked when on, and no real secret is ever in
// the options. The SDK itself is mocked — its behaviour is HyperDX's to test;
// what is ours is exactly what we hand it.
import {
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vitest";

import type { ReplayRecordPlugin } from "./url-scrub.js";

const init = vi.fn();
const recordException = vi.fn();
const addAction = vi.fn();
const setGlobalAttributes = vi.fn();
const stopSessionRecorder = vi.fn();

// The SDK as a bundler hands it back: a module NAMESPACE, whose members are
// read-only getters at the top level AND the real client under `default`. The
// real client's methods use `this`, so calling one on the namespace throws.
const client = {
  init(this: unknown, ...args: unknown[]) {
    if (this !== client) {
      throw new TypeError(
        "init called on the module namespace, not the client",
      );
    }
    return init(...args);
  },
  recordException(this: unknown, ...args: unknown[]) {
    if (this !== client) {
      throw new TypeError("recordException called on the module namespace");
    }
    return recordException(...args);
  },
  addAction(this: unknown, ...args: unknown[]) {
    if (this !== client) {
      throw new TypeError("addAction called on the module namespace");
    }
    return addAction(...args);
  },
  setGlobalAttributes(this: unknown, ...args: unknown[]) {
    if (this !== client) {
      throw new TypeError("setGlobalAttributes called on the module namespace");
    }
    return setGlobalAttributes(...args);
  },
  stopSessionRecorder(this: unknown, ...args: unknown[]) {
    if (this !== client) {
      throw new TypeError("stopSessionRecorder called on the module namespace");
    }
    return stopSessionRecorder(...args);
  },
};

vi.mock("@hyperdx/browser", () => {
  const ns = { default: client } as Record<string, unknown>;
  for (const key of [
    "init",
    "recordException",
    "addAction",
    "setGlobalAttributes",
    "stopSessionRecorder",
  ] as const) {
    Object.defineProperty(ns, key, {
      get: () => client[key],
      enumerable: true,
    });
  }
  return ns;
});

const ORIGIN = "http://localhost:3000";

type Telemetry = typeof import("./telemetry.js");
type Reporter = typeof import("./error-reporter.js");
type Track = typeof import("./track.js");

async function load(): Promise<{ t: Telemetry; r: Reporter; k: Track }> {
  vi.resetModules();
  return {
    t: await import("./telemetry.js"),
    r: await import("./error-reporter.js"),
    k: await import("./track.js"),
  };
}

type Listener = (e: unknown) => void;
const listeners = new Map<string, Listener[]>();

function stubDom() {
  listeners.clear();
  vi.stubGlobal("window", {
    location: { origin: ORIGIN },
    addEventListener: (type: string, fn: Listener) =>
      listeners.set(type, [...(listeners.get(type) ?? []), fn]),
    removeEventListener: (type: string, fn: Listener) =>
      listeners.set(
        type,
        (listeners.get(type) ?? []).filter((l) => l !== fn),
      ),
  });
}

function dispatch(type: string, event: unknown) {
  for (const fn of listeners.get(type) ?? []) fn(event);
}

// Transform the module graph once, outside any test: a cold import of
// telemetry.js takes seconds on a loaded machine, and inside the first test
// it ran against that test's 5s timeout. load() then re-evaluates from the
// transform cache in milliseconds.
beforeAll(() => load(), 60_000);

beforeEach(() => {
  init.mockReset();
  recordException.mockReset();
  addAction.mockReset();
  setGlobalAttributes.mockReset();
  stopSessionRecorder.mockReset();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("initBrowserTelemetry", () => {
  it("is a no-op without a DOM and never loads the SDK", async () => {
    const { t } = await load();

    await expect(
      t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" }),
    ).resolves.toBe(false);
    expect(init).not.toHaveBeenCalled();
  });

  it("is a no-op without an endpoint, or with it set to off", async () => {
    stubDom();
    const { t } = await load();

    await expect(t.initBrowserTelemetry({ serviceName: "web" })).resolves.toBe(
      false,
    );
    await expect(
      t.initBrowserTelemetry({ serviceName: "web", endpoint: "off" }),
    ).resolves.toBe(false);
    expect(init).not.toHaveBeenCalled();
  });

  it("initialises the SDK exactly once however often it is called", async () => {
    stubDom();
    const { t } = await load();
    const cfg = { serviceName: "web", endpoint: "/_otel" };

    const results = await Promise.all([
      t.initBrowserTelemetry(cfg),
      t.initBrowserTelemetry(cfg),
    ]);
    await t.initBrowserTelemetry(cfg);

    expect(results).toEqual([true, true]);
    expect(init).toHaveBeenCalledTimes(1);
  });

  it("never throws when the SDK fails to load or init", async () => {
    stubDom();
    init.mockImplementation(() => {
      throw new Error("boom");
    });
    vi.spyOn(console, "debug").mockImplementation(() => undefined);
    const { t } = await load();

    await expect(
      t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" }),
    ).resolves.toBe(false);
  });

  it("sends to this origin's /_otel, with a placeholder key and no real secret", async () => {
    stubDom();
    const { t } = await load();

    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: t.DEFAULT_INGEST_PATH,
    });

    const opts = init.mock.calls[0]?.[0] as Record<string, unknown>;
    expect(opts.url).toBe(`${ORIGIN}/_otel`);
    expect(new URL(opts.url as string).origin).toBe(ORIGIN);
    expect(opts.apiKey).toBe(t.PLACEHOLDER_API_KEY);
    expect(opts.service).toBe("web");
  });

  it("records exceptions through the SDK once running, and not before", async () => {
    stubDom();
    const { t, r } = await load();
    const err = new Error("render crash");

    // before init: nothing to hand it to, and the caller is told so
    expect(r.reportException(err)).toBe(false);
    expect(recordException).not.toHaveBeenCalled();

    await t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" });
    expect(r.reportException(err, { "react.component_stack": "at X" })).toBe(
      true,
    );

    expect(recordException).toHaveBeenCalledWith(err, {
      "react.component_stack": "at X",
    });
  });
});

describe("reportException de-duplication", () => {
  it("skips an error the SDK already recorded through console.error, and records any other", async () => {
    stubDom();
    const original = console.error;
    const sink = vi.fn();
    console.error = sink;
    try {
      const { t, r } = await load();
      await t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" });

      // React logs a boundary-caught error to console.error first.
      const logged = new Error("caught by boundary");
      console.error(logged);
      r.reportException(logged);
      expect(sink).toHaveBeenCalledWith(logged); // still forwarded untouched
      expect(recordException).not.toHaveBeenCalled();

      const silent = new Error("never logged");
      r.reportException(silent);
      expect(recordException).toHaveBeenCalledTimes(1);
      expect(recordException).toHaveBeenCalledWith(silent, undefined);
    } finally {
      console.error = original;
    }
  });

  it("skips an error the SDK saw as an uncaught window error or unhandled rejection", async () => {
    stubDom();
    const { t, r } = await load();
    await t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" });

    const uncaught = new Error("uncaught render error");
    dispatch("error", { error: uncaught });
    const rejected = new Error("rejected");
    dispatch("unhandledrejection", { reason: rejected });

    // Still reports "the SDK has it" so a caller does not console.error it.
    expect(r.reportException(uncaught)).toBe(true);
    expect(r.reportException(rejected)).toBe(true);
    expect(recordException).not.toHaveBeenCalled();
  });
});

describe("benign browser errors", () => {
  // A fake of the SDK's error capture, shaped like the real one: init
  // registers its window listeners and wraps console.error, and each captured
  // failure is a span on the `errors` tracer of the registered provider,
  // exported only once it is ENDED.
  type FakeSpan = {
    name: string;
    attributes: Record<string, unknown>;
    setAttribute(k: string, v: unknown): FakeSpan;
    end(): void;
  };
  const exported: Array<{ name: string; message: unknown }> = [];
  const consoleUnderSdk = vi.fn();
  let originalConsoleError: typeof console.error;

  function fakeProvider() {
    const tracers = new Map<string, { startSpan(name: string): FakeSpan }>();
    return {
      getTracer(name: string, version?: string) {
        const key = `${name}@${version ?? ""}`;
        if (!tracers.has(key)) {
          tracers.set(key, {
            startSpan(spanName: string): FakeSpan {
              const span: FakeSpan = {
                name: spanName,
                attributes: {},
                setAttribute(k, v) {
                  span.attributes[k] = v;
                  return span;
                },
                end() {
                  exported.push({
                    name: spanName,
                    message: span.attributes["error.message"],
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

  function exportedMessages(name: string) {
    return exported.filter((e) => e.name === name).map((e) => e.message);
  }

  function dispatchEvent(type: string, fields: Record<string, unknown>) {
    for (const fn of listeners.get(type) ?? []) fn({ ...fields });
  }

  function nameOfValue(v: unknown): string {
    try {
      const n = (v as { name?: unknown }).name;
      return typeof n === "string" ? n : "String";
    } catch {
      return "String";
    }
  }

  async function start(
    config: Record<string, unknown> = {},
    opts: { initThrows?: boolean } = {},
  ) {
    stubDom();
    const { trace } = await import("@opentelemetry/api");
    trace.disable();
    trace.setGlobalTracerProvider(fakeProvider() as never);
    init.mockImplementation(() => {
      if (opts.initThrows) throw new Error("sdk init failed");
      const provider = trace.getTracerProvider() as unknown as {
        getDelegate(): ReturnType<typeof fakeProvider>;
      };
      // Held once, as the real instrumentation holds its tracer.
      const tracer = provider.getDelegate().getTracer("errors", "1");
      const record = (source: string, value: unknown) => {
        const span = tracer.startSpan(source);
        if (typeof value === "object" && value !== null) {
          span.setAttribute("error.object", nameOfValue(value));
          let message: unknown = "";
          try {
            message = (value as { message?: unknown }).message;
          } catch {
            message = "";
          }
          span.setAttribute("error.message", message);
        } else {
          span.setAttribute("error.object", "String");
          span.setAttribute("error.message", String(value));
        }
        span.end();
      };
      window.addEventListener("unhandledrejection", (e: Event) =>
        record("unhandledrejection", (e as PromiseRejectionEvent).reason),
      );
      window.addEventListener("error", (e: Event) =>
        record("onerror", (e as ErrorEvent).error),
      );
      const below = console.error;
      console.error = (...args: unknown[]) => {
        record("console.error", args[0]);
        below(...args);
      };
    });
    const loaded = await load();
    await loaded.t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      ...config,
    });
    return loaded;
  }

  beforeEach(() => {
    exported.length = 0;
    consoleUnderSdk.mockReset();
    originalConsoleError = console.error;
    console.error = consoleUnderSdk;
  });

  afterEach(async () => {
    console.error = originalConsoleError;
    const { trace } = await import("@opentelemetry/api");
    trace.disable();
  });

  it("keeps an unhandled AbortError out of the record, and records any other rejection", async () => {
    await start();
    dispatchEvent("unhandledrejection", {
      reason: new DOMException(
        "signal is aborted without reason",
        "AbortError",
      ),
    });
    expect(exported).toEqual([]);

    dispatchEvent("unhandledrejection", { reason: new Error("boom") });
    expect(exportedMessages("unhandledrejection")).toEqual(["boom"]);
  });

  it("keeps an unhandled TimeoutError out of the record, and records a real error that mentions a timeout", async () => {
    await start();
    dispatchEvent("unhandledrejection", {
      reason: new DOMException("signal timed out", "TimeoutError"),
    });
    expect(exported).toEqual([]);

    dispatchEvent("unhandledrejection", {
      reason: new Error("request timeout after 30s"),
    });
    expect(exportedMessages("unhandledrejection")).toEqual([
      "request timeout after 30s",
    ]);
  });

  it("never touches the app's handlers: before and after telemetry starts, they see every event", async () => {
    stubDom();
    const before = vi.fn();
    window.addEventListener("unhandledrejection", (e: Event) =>
      before((e as PromiseRejectionEvent).reason),
    );
    // start() re-stubs the DOM, so register "before" on the stub it will use.
    const keep = listeners.get("unhandledrejection") ?? [];
    await start();
    listeners.set("unhandledrejection", [
      ...keep,
      ...(listeners.get("unhandledrejection") ?? []),
    ]);
    const after = vi.fn();
    window.addEventListener("unhandledrejection", (e: Event) =>
      after((e as PromiseRejectionEvent).reason),
    );
    const abort = new DOMException("x", "AbortError");
    const offline = new TypeError("Failed to fetch");
    dispatchEvent("unhandledrejection", { reason: abort });
    dispatchEvent("unhandledrejection", { reason: offline });
    expect(before.mock.calls).toEqual([[abort], [offline]]);
    expect(after.mock.calls).toEqual([[abort], [offline]]);
    expect(exported).toEqual([]);
  });

  it("never filters a ConnectError, even one whose message is a fetch failure", async () => {
    await start();
    const rpc = new Error("[unavailable] Failed to fetch");
    rpc.name = "ConnectError";
    dispatchEvent("unhandledrejection", { reason: rpc });
    expect(exportedMessages("unhandledrejection")).toEqual([
      "[unavailable] Failed to fetch",
    ]);
  });

  it("logs a console.error of the browser's generic fetch failure as always, and does not record it", async () => {
    await start();
    const offline = new TypeError("Failed to fetch (dash.example.io)");
    console.error("auth refresh:", offline);
    expect(consoleUnderSdk).toHaveBeenCalledWith("auth refresh:", offline);
    expect(exported).toEqual([]);

    const bug = new TypeError("Cannot read properties of undefined");
    console.error(bug);
    expect(consoleUnderSdk).toHaveBeenCalledWith(bug);
    expect(exportedMessages("console.error")).toEqual([
      "Cannot read properties of undefined",
    ]);
  });

  it("records a console.error when any exception in it is real", async () => {
    await start();
    const offline = new TypeError("Failed to fetch");
    const rpc = new Error("[unavailable] upstream");
    rpc.name = "ConnectError";
    console.error(offline, rpc);
    expect(exported).toHaveLength(1);

    console.error(new DOMException("x", "AbortError"), new Error("real bug"));
    expect(exported).toHaveLength(2);
    dispatchEvent("pagehide", {});
    expect(addAction).not.toHaveBeenCalledWith(
      "forge.errors_filtered",
      expect.anything(),
    );
  });

  it("treats a DOMException that is not an Error as an exception, either way", async () => {
    await start();
    // Some browsers' DOMException is not instanceof Error.
    const notAnError = (name: string, message: string) => {
      const ex = {} as object;
      Object.defineProperty(ex, "name", { value: name });
      Object.defineProperty(ex, "message", { value: message });
      Object.setPrototypeOf(ex, { [Symbol.toStringTag]: "DOMException" });
      return ex;
    };
    console.error(notAnError("AbortError", "aborted"));
    expect(exported).toEqual([]);

    console.error(
      new TypeError("Failed to fetch"),
      notAnError("NotAllowedError", "denied"),
    );
    expect(exported).toHaveLength(1);
  });

  it("leaves everything as it was when the SDK fails to start", async () => {
    vi.spyOn(console, "debug").mockImplementation(() => undefined);
    await start({}, { initThrows: true });
    expect(console.error).toBe(consoleUnderSdk);
    const { trace } = await import("@opentelemetry/api");
    const tracer = (
      trace.getTracerProvider() as unknown as {
        getDelegate(): ReturnType<typeof fakeProvider>;
      }
    )
      .getDelegate()
      .getTracer("errors", "1");
    tracer.startSpan("unhandledrejection").end();
    expect(exportedMessages("unhandledrejection")).toHaveLength(1);
  });

  it("undoes the tracer wrap and the console tap when startup fails after the SDK init", async () => {
    vi.spyOn(console, "debug").mockImplementation(() => undefined);
    setGlobalAttributes.mockImplementationOnce(() => {
      throw new Error("late failure");
    });
    await start();
    dispatchEvent("unhandledrejection", {
      reason: new DOMException("x", "AbortError"),
    });
    expect(exportedMessages("unhandledrejection")).toEqual(["x"]);
    console.error(new TypeError("Failed to fetch"));
    expect(exportedMessages("console.error")).toEqual(["Failed to fetch"]);
  });

  it("counts every exception of a benign console.error, of each kind", async () => {
    await start();
    console.error(
      new TypeError("Failed to fetch"),
      new DOMException("aborted", "AbortError"),
      new TypeError("Load failed"),
    );
    expect(exported).toEqual([]);
    dispatchEvent("pagehide", {});
    expect(addAction).toHaveBeenCalledWith("forge.errors_filtered", {
      kind: "network",
      source: "console.error",
      count: 2,
    });
    expect(addAction).toHaveBeenCalledWith("forge.errors_filtered", {
      kind: "abort",
      source: "console.error",
      count: 1,
    });
  });

  it("records and forwards a console.error whose argument cannot be inspected, without throwing", async () => {
    await start();
    const hostile = {
      get [Symbol.toStringTag](): string {
        throw new Error("toStringTag getter");
      },
    };
    const { proxy, revoke } = Proxy.revocable({}, {});
    revoke();
    expect(() => console.error(hostile)).not.toThrow();
    // Compared by identity: a matcher's deep equality would read the getter.
    expect(consoleUnderSdk.mock.calls.length).toBe(1);
    expect(consoleUnderSdk.mock.calls[0]?.[0] === hostile).toBe(true);
    expect(exported).toHaveLength(1);

    expect(() =>
      console.error(new TypeError("Failed to fetch"), proxy),
    ).not.toThrow();
    expect(exported).toHaveLength(2);
  });

  it("counts what it filtered and emits the counts when the page is hidden", async () => {
    await start();
    dispatchEvent("unhandledrejection", {
      reason: new DOMException("x", "AbortError"),
    });
    dispatchEvent("unhandledrejection", {
      reason: new DOMException("y", "AbortError"),
    });
    dispatchEvent("error", { error: new TypeError("Failed to fetch") });
    expect(addAction).not.toHaveBeenCalledWith(
      "forge.errors_filtered",
      expect.anything(),
    );

    dispatchEvent("pagehide", {});
    expect(addAction).toHaveBeenCalledWith("forge.errors_filtered", {
      kind: "abort",
      source: "unhandledrejection",
      count: 2,
    });
    expect(addAction).toHaveBeenCalledWith("forge.errors_filtered", {
      kind: "network",
      source: "error",
      count: 1,
    });
  });

  it("never filters a String throw, whatever its text", async () => {
    await start();
    dispatchEvent("unhandledrejection", { reason: "Failed to fetch" });
    expect(exportedMessages("unhandledrejection")).toEqual(["Failed to fetch"]);
  });

  it("does not record a benign error handed to reportException, but says the SDK has it", async () => {
    const { r } = await start();
    expect(r.reportException(new TypeError("Load failed"))).toBe(true);
    expect(recordException).not.toHaveBeenCalled();
  });

  it("records everything with classifyBenignError: () => null", async () => {
    await start({ classifyBenignError: () => null });
    dispatchEvent("unhandledrejection", {
      reason: new DOMException("x", "AbortError"),
    });
    expect(exportedMessages("unhandledrejection")).toEqual(["x"]);
  });
});

describe("buildSdkOptions", () => {
  const base = { serviceName: "web" };

  it("carries the page's client attributes on the resource, under the config's own", async () => {
    const { t } = await load();
    const opts = t.buildSdkOptions(
      { ...base, serviceVersion: "1.8.16-dev+7d787d80" },
      `${ORIGIN}/_otel`,
      ORIGIN,
      {
        "browser.platform": "macOS",
        "app.desktop.version": "1.8.16",
        "service.version": "spoofed",
      },
    );
    expect(opts.otelResourceAttributes).toEqual({
      "browser.platform": "macOS",
      "app.desktop.version": "1.8.16",
      "service.version": "1.8.16-dev+7d787d80",
    });
  });

  it.each([
    { doNotTrack: "1" },
    { doNotTrack: "yes" },
    { msDoNotTrack: "1" },
    { globalPrivacyControl: true },
  ])(
    "browser privacy overrides opt-in and a held sample: %o",
    async (privacy) => {
      vi.stubGlobal("navigator", privacy);
      const { t } = await load();
      const store = { getItem: () => "1", setItem: vi.fn() };
      expect(
        t.decideReplay({ replay: true, replaySampleRate: 1 }, store, () => 0),
      ).toBe(false);
      expect(
        t.decideReplay({ replay: true, replaySampleRate: 0.5 }, store, () => 0),
      ).toBe(false);
      expect(
        t.buildSdkOptions({ ...base, replay: true }, `${ORIGIN}/_otel`, ORIGIN)
          .disableReplay,
      ).toBe(true);
      expect(store.setItem).not.toHaveBeenCalled();
    },
  );

  it("legacy window DNT and unreadable preferences fail closed", async () => {
    vi.stubGlobal("navigator", {});
    vi.stubGlobal("window", { doNotTrack: "1" });
    const { t } = await load();
    expect(t.decideReplay({ replay: true }, null, () => 0)).toBe(false);
    vi.stubGlobal("navigator", {
      get globalPrivacyControl() {
        throw new Error("blocked");
      },
    });
    expect(t.decideReplay({ replay: true }, null, () => 0)).toBe(false);
  });

  it("keeps replay OFF by default", async () => {
    const { t } = await load();
    const opts = t.buildSdkOptions(base, `${ORIGIN}/_otel`, ORIGIN);

    expect(opts.disableReplay).toBe(true);
  });

  it("disables automatic DOM spans without disabling masked replay or network tracing", async () => {
    const { t } = await load();
    const opts = t.buildSdkOptions(
      { ...base, replay: true },
      `${ORIGIN}/_otel`,
      ORIGIN,
    );
    expect(opts.instrumentations).toMatchObject({
      interactions: false,
      fetch: { propagateTraceHeaderCorsUrls: expect.any(Array) },
      xhr: { propagateTraceHeaderCorsUrls: expect.any(Array) },
    });
    expect(opts.disableReplay).toBe(false);
    expect(opts.maskAllInputs).toBe(true);
    expect(opts.maskAllText).toBe(true);
  });

  it("forces masking and media blocking whatever the config says", async () => {
    const { t } = await load();
    for (const replay of [undefined, false, true]) {
      const opts = t.buildSdkOptions(
        // A caller reaching past the types must not weaken any of it.
        {
          ...base,
          replay,
          maskAllInputs: false,
          maskAllText: false,
          recordCanvas: true,
          blockSelector: "",
        } as never,
        `${ORIGIN}/_otel`,
        ORIGIN,
      );

      expect(opts.maskAllInputs).toBe(true);
      expect(opts.maskAllText).toBe(true);
      expect(opts.recordCanvas).toBe(false);
      expect(opts.blockSelector).toBe(t.REPLAY_BLOCK_SELECTOR);
      expect(opts.replay).toMatchObject({
        inlineImages: false,
        collectFonts: false,
      });
    }
    for (const tag of ["img", "picture", "video", "audio", "iframe"]) {
      expect(t.REPLAY_BLOCK_SELECTOR.split(/,\s*/)).toContain(tag);
    }
  });

  it("hands the recorder the replay URL scrub, with the config's kept parameters", async () => {
    const { t } = await load();
    const meta = {
      type: 4,
      data: {
        href: `${ORIGIN}/welcome?utm_source=ad&email=j%40x.io#tok`,
        width: 1,
        height: 1,
      },
      timestamp: 1,
    };
    const cases: Array<[{ keepUrlQueryParams?: string[] }, string]> = [
      [{}, `${ORIGIN}/welcome`],
      [{ keepUrlQueryParams: ["utm_*"] }, `${ORIGIN}/welcome?utm_source=ad`],
    ];
    for (const [config, href] of cases) {
      const opts = t.buildSdkOptions(
        { ...base, ...config, replay: true },
        `${ORIGIN}/_otel`,
        ORIGIN,
      );
      const plugins = (opts.replay as { plugins: ReplayRecordPlugin[] })
        .plugins;
      expect(plugins.map((p) => p.name)).toEqual(["forge/replay-url-scrub"]);
      expect(plugins[0]!.eventProcessor(meta).data.href).toBe(href);
    }
  });

  it("passes consoleCapture through, and defaults it on", async () => {
    const { t } = await load();
    const off = t.buildSdkOptions(
      { ...base, consoleCapture: false },
      `${ORIGIN}/_otel`,
      ORIGIN,
    );
    const on = t.buildSdkOptions(base, `${ORIGIN}/_otel`, ORIGIN);

    expect(off.consoleCapture).toBe(false);
    expect(on.consoleCapture).toBe(true);
  });

  it("emits forge.frontend only when a frontend is named", async () => {
    const { t } = await load();
    const named = t.buildSdkOptions(
      { ...base, frontend: "reliant-web" },
      `${ORIGIN}/_otel`,
      ORIGIN,
    );
    const anon = t.buildSdkOptions(base, `${ORIGIN}/_otel`, ORIGIN);

    expect(
      (named.otelResourceAttributes as Record<string, string>)[
        "forge.frontend"
      ],
    ).toBe("reliant-web");
    expect(anon.otelResourceAttributes).not.toHaveProperty("forge.frontend");
  });

  it("forces all text and all inputs masked whenever replay is enabled", async () => {
    const { t } = await load();
    const opts = t.buildSdkOptions(
      { ...base, replay: true },
      `${ORIGIN}/_otel`,
      ORIGIN,
    );

    expect(opts.disableReplay).toBe(false);
    expect(opts.maskAllInputs).toBe(true);
    expect(opts.maskAllText).toBe(true);
    expect(opts.recordCanvas).toBe(false);
  });

  it("captures console, but not network bodies, by default", async () => {
    const { t } = await load();
    const opts = t.buildSdkOptions(base, `${ORIGIN}/_otel`, ORIGIN);

    expect(opts.consoleCapture).toBe(true);
    expect(opts.advancedNetworkCapture).toBe(false);
    expect(opts.disableIntercom).toBe(true);
  });

  it("propagates traceparent to this origin and to the API origin, and nowhere else", async () => {
    const { t } = await load();
    const opts = t.buildSdkOptions(
      { ...base, apiOrigins: ["http://localhost:8080/", undefined] },
      `${ORIGIN}/_otel`,
      ORIGIN,
    );
    const targets = opts.tracePropagationTargets as RegExp[];
    const matches = (url: string) => targets.some((re) => re.test(url));

    expect(matches(`${ORIGIN}/anything`)).toBe(true);
    expect(matches("http://localhost:8080/svc.v1.Svc/Method")).toBe(true);
    expect(matches("http://localhost:8080")).toBe(true);
    expect(matches("https://third-party.example.com/x")).toBe(false);
    // Prefix lookalikes must not match: localhost:80801 is another origin.
    expect(matches("http://localhost:80801/x")).toBe(false);
  });

  it("does not trace its own exports", async () => {
    const { t } = await load();
    const opts = t.buildSdkOptions(base, `${ORIGIN}/_otel`, ORIGIN);
    const ignore = opts.ignoreUrls as RegExp[];

    expect(ignore.some((re) => re.test(`${ORIGIN}/_otel/v1/traces`))).toBe(
      true,
    );
    expect(ignore.some((re) => re.test(`${ORIGIN}/api/things`))).toBe(false);
  });

  it("emits both the current and the legacy deployment-environment key", async () => {
    const { t } = await load();
    const opts = t.buildSdkOptions(
      {
        ...base,
        environment: "staging",
        serviceVersion: "1.2.3",
        serviceNamespace: "shop",
      },
      `${ORIGIN}/_otel`,
      ORIGIN,
    );

    expect(opts.otelResourceAttributes).toEqual({
      "service.version": "1.2.3",
      "service.namespace": "shop",
      "deployment.environment.name": "staging",
      "deployment.environment": "staging",
    });
  });
});

describe("fetch and XHR URL query stripping", () => {
  type Hooked = {
    propagateTraceHeaderCorsUrls: RegExp[];
    advancedNetworkCapture: () => boolean;
    applyCustomAttributesOnSpan: (span: unknown) => void;
  };

  function span(attributes: Record<string, unknown>) {
    const set = vi.fn((k: string, v: unknown) => {
      attributes[k] = v;
    });
    return { attributes, setAttribute: set };
  }

  it("rewrites http.url and url.full on both instrumentations, query and fragment gone", async () => {
    const { t } = await load();
    const opts = t.buildSdkOptions(
      { serviceName: "web" },
      `${ORIGIN}/_otel`,
      ORIGIN,
    );
    const inst = opts.instrumentations as Record<string, Hooked>;

    for (const kind of ["fetch", "xhr"]) {
      const s = span({
        "http.url": "https://api.example.com/v1/items?token=abc&page=2#top",
        "url.full": "https://api.example.com/v1/items?code=oauth",
        "http.method": "GET",
      });
      inst[kind]!.applyCustomAttributesOnSpan(s);

      expect(s.attributes["http.url"]).toBe("https://api.example.com/v1/items");
      expect(s.attributes["url.full"]).toBe("https://api.example.com/v1/items");
      expect(s.attributes["http.method"]).toBe("GET");
    }
  });

  it("leaves a clean URL alone and tolerates a span with no URL", async () => {
    const { t } = await load();
    const opts = t.buildSdkOptions(
      { serviceName: "web" },
      `${ORIGIN}/_otel`,
      ORIGIN,
    );
    const hook = (opts.instrumentations as Record<string, Hooked>).fetch!
      .applyCustomAttributesOnSpan;

    const clean = span({ "http.url": "https://api.example.com/v1/items" });
    hook(clean);
    expect(clean.setAttribute).not.toHaveBeenCalled();

    expect(() => hook(span({}))).not.toThrow();
    expect(() => hook({ setAttribute: vi.fn() })).not.toThrow();
  });

  it("stamps the RUM session on fetch and xhr spans while telemetry runs", async () => {
    const { t } = await load();
    const gate = await import("./telemetry-gate.js");
    const opts = t.buildSdkOptions(
      { serviceName: "web" },
      `${ORIGIN}/_otel`,
      ORIGIN,
    );
    const inst = opts.instrumentations as Record<string, Hooked>;

    gate.registerRumSession(() => "91b955b313a1a86644ea53730a92deab");
    for (const kind of ["fetch", "xhr"]) {
      const s = span({ "http.url": "https://api.example.com/v1/items" });
      inst[kind]!.applyCustomAttributesOnSpan(s);
      expect(s.attributes["session.id"]).toBe(
        "91b955b313a1a86644ea53730a92deab",
      );
    }

    // No session (telemetry off), or one that is not a session id: nothing.
    for (const source of [null, () => "not a session"]) {
      gate.registerRumSession(source);
      const s = span({});
      inst.fetch!.applyCustomAttributesOnSpan(s);
      expect(s.attributes).not.toHaveProperty("session.id");
    }
    gate.resetTelemetryGate();
  });

  it("restates what the SDK's own fetch/xhr config would have carried", async () => {
    const { t } = await load();
    const opts = t.buildSdkOptions(
      { serviceName: "web", apiOrigins: ["http://localhost:8080"] },
      `${ORIGIN}/_otel`,
      ORIGIN,
    );
    const inst = opts.instrumentations as Record<string, Hooked>;

    for (const kind of ["fetch", "xhr"]) {
      // The SDK spreads this object over its own, whole keys at a time.
      expect(inst[kind]!.propagateTraceHeaderCorsUrls).toEqual(
        opts.tracePropagationTargets,
      );
      expect(inst[kind]!.advancedNetworkCapture()).toBe(false);
    }
  });

  it("stripUrlQuery cuts at the first ? or #", async () => {
    const { t } = await load();

    expect(t.stripUrlQuery("/a/b?x=1")).toBe("/a/b");
    expect(t.stripUrlQuery("/a/b#frag?x")).toBe("/a/b");
    expect(t.stripUrlQuery("/a/b")).toBe("/a/b");
  });
});

describe("URLs in what the SDK exports", () => {
  // The SDK's own export pipeline, built the way @hyperdx/otel-web's init
  // builds it (index.js): its SplunkWebTracerProvider, its attributes
  // processor (which stamps `location.href` on every span as it starts) and its
  // batch processor, over an exporter that serializes each batch with the
  // OTel JSON serializer the SDK's OTLPTraceExporter posts. Only the spans are
  // synthesized here, each with the attributes its instrumentation records
  // (document-load, user-interaction, the SDK's routeChange, fetch, its error
  // instrumentation and addAction); scripts/browser-telemetry-smoke.mjs proves
  // the same against the real bundle and real instrumentations in Chromium.
  const PAGE = `${ORIGIN}/welcome?email=jane%40example.com&utm_source=news#access_token=SECRET`;
  const NEXT = `${ORIGIN}/orders/17?ref=mail&token=SECRET#top`;

  type KeyValue = {
    key: string;
    value: {
      stringValue?: string;
      intValue?: number | string;
      arrayValue?: { values: Array<{ stringValue?: string }> };
    };
  };
  type ExportedSpan = {
    name: string;
    attributes: KeyValue[];
    events?: Array<{ name: string; attributes?: KeyValue[] }>;
  };
  type Batch = {
    resourceSpans: Array<{
      resource: { attributes: KeyValue[] };
      scopeSpans: Array<{ scope: { name: string }; spans: ExportedSpan[] }>;
    }>;
  };

  const value = (kv: KeyValue) =>
    kv.value.stringValue ??
    kv.value.intValue ??
    kv.value.arrayValue?.values.map((v) => v.stringValue);
  const attrs = (list: KeyValue[] = []) =>
    Object.fromEntries(list.map((kv) => [kv.key, value(kv)]));

  // The SDK's classes, loaded once: a cold import of the OTel web stack
  // takes seconds on a loaded machine. The package's exports map hides
  // these files, so they are imported by path.
  let sdk: Record<string, any> = {};
  beforeAll(async () => {
    const root = "../node_modules/@hyperdx/otel-web";
    const otel = `${root}/node_modules/@opentelemetry`;
    sdk = {
      ...(await import(`${root}/dist/esm/SplunkWebTracerProvider.js`)),
      ...(await import(`${root}/dist/esm/SplunkSpanAttributesProcessor.js`)),
      ...(await import(`${root}/dist/esm/HyperDXBatchSpanProcessor.js`)),
      ...(await import(`${otel}/resources/build/esm/index.js`)),
      ...(await import(
        `${otel}/otlp-transformer/build/esm/trace/json/index.js`
      )),
    };
  }, 60_000);

  let batches: Batch[] = [];
  let recorded: Array<{ attributes: Record<string, unknown> }> = [];
  let provider: { forceFlush(): Promise<void> } | null = null;
  const location = { href: PAGE, origin: ORIGIN };

  async function boot(config: Record<string, unknown> = {}) {
    stubDom();
    vi.stubGlobal("location", location);
    location.href = PAGE;
    const { trace } = await import("@opentelemetry/api");
    const {
      SplunkWebTracerProvider,
      SplunkSpanAttributesProcessor,
      HyperDXBatchSpanProcessor,
      resourceFromAttributes,
      JsonTraceSerializer,
    } = sdk;
    trace.disable();
    init.mockImplementation((opts: Record<string, unknown>) => {
      const exporter = {
        export(spans: never[], done: (r: { code: number }) => void) {
          const bytes = JsonTraceSerializer.serializeRequest(spans);
          batches.push(JSON.parse(new TextDecoder().decode(bytes)));
          done({ code: 0 });
        },
        shutdown: () => Promise.resolve(),
      };
      const p = new SplunkWebTracerProvider({
        resource: resourceFromAttributes({
          "service.name": opts.service,
          ...(opts.otelResourceAttributes as Record<string, string>),
          // An app's own resource attribute that holds a URL.
          "app.start_url": PAGE,
        }),
        spanProcessors: [
          new SplunkSpanAttributesProcessor({}),
          new HyperDXBatchSpanProcessor(exporter, {
            scheduledDelayMillis: 60_000,
          }),
        ],
      });
      // As the SDK does: the session is a live getter on the resource.
      Object.defineProperty(p.resource.attributes, "rum.sessionId", {
        get: () => "91b955b313a1a86644ea53730a92deab",
        enumerable: true,
        configurable: true,
      });
      trace.setGlobalTracerProvider(p);
      provider = p;
      // A span that ends DURING init, before the runtime's hook goes in.
      trace
        .getTracer("@opentelemetry/instrumentation-document-load")
        .startSpan("documentFetch", { attributes: { "http.url": PAGE } })
        .end();
    });
    const { t } = await load();
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      ...config,
    });
    return trace;
  }

  async function record(trace: typeof import("@opentelemetry/api").trace) {
    const tracer = (name: string) => trace.getTracer(name);
    const keep = <T>(span: T): T => {
      recorded.push(span as never);
      return span;
    };
    const doc = keep(
      tracer("@opentelemetry/instrumentation-document-load").startSpan(
        "documentLoad",
        {
          attributes: {
            "http.url": PAGE,
            "http.user_agent": "Mozilla/5.0 (X11) HeadlessChrome",
            "document.referrer": "https://search.example/?q=jane%40example.com",
          },
        },
      ),
    );
    doc.addEvent("fetchStart");
    doc.end();
    tracer("@opentelemetry/instrumentation-document-load")
      .startSpan("resourceFetch", {
        attributes: { "http.url": `${ORIGIN}/assets/app.js?v=3#map` },
      })
      .end();
    tracer("@opentelemetry/instrumentation-user-interaction")
      .startSpan("click", {
        attributes: {
          event_type: "click",
          target_element: "BUTTON",
          target_xpath: '//*[@id="go"]',
          "http.url": PAGE,
        },
      })
      .end();
    // The SDK's routeChange: emitted after history.pushState moved the page.
    location.href = NEXT;
    tracer("route")
      .startSpan("routeChange", {
        attributes: { component: "user-interaction", "prev.href": PAGE },
      })
      .end();
    const fetchSpan = tracer("@opentelemetry/instrumentation-fetch").startSpan(
      "HTTP GET",
      {
        attributes: {
          "http.method": "GET",
          "http.url": `${ORIGIN}/api/search?q=jane&utm_campaign=x#r`,
          "url.full": `${ORIGIN}/api/search?q=jane&utm_campaign=x#r`,
          "url.query": "q=jane&utm_campaign=x",
          "url.fragment": "r",
          "url.path": "/api/search",
          "http.status_code": 200,
        },
      },
    );
    fetchSpan.addEvent("redirect", {
      "http.url": `${ORIGIN}/api/next?code=oauth`,
    });
    fetchSpan.end();
    tracer("errors")
      .startSpan("onerror", {
        attributes: {
          "error.message": "Failed to load script",
          target_src: `${ORIGIN}/assets/chunk.js?sig=SECRET`,
        },
      })
      .end();
    tracer("@hyperdx/browser")
      .startSpan("checkout_started", {
        attributes: {
          component: "custom-action",
          // Free text is not a URL attribute: exported as recorded.
          message: "opened /welcome?email=jane@example.com",
          links: ["https://x.example/a?b=c"],
          count: 3,
        },
      })
      .end();
    await provider!.forceFlush();
  }

  const spansOf = () =>
    batches.flatMap((b) =>
      b.resourceSpans.flatMap((r) => r.scopeSpans.flatMap((s) => s.spans)),
    );
  const span = (name: string) => {
    const found = spansOf().find((s) => s.name === name);
    expect(found, `${name} among ${spansOf().map((s) => s.name)}`).toBeTruthy();
    return attrs(found!.attributes);
  };

  beforeEach(() => {
    batches = [];
    recorded = [];
    provider = null;
  });

  afterEach(async () => {
    const { trace } = await import("@opentelemetry/api");
    trace.disable();
  });

  it("exports every instrumented span type with no query or fragment in any URL", async () => {
    const trace = await boot();
    await record(trace);

    expect(span("documentFetch")).toMatchObject({
      "location.href": `${ORIGIN}/welcome`,
      "http.url": `${ORIGIN}/welcome`,
    });
    expect(span("documentLoad")).toMatchObject({
      "location.href": `${ORIGIN}/welcome`,
      "http.url": `${ORIGIN}/welcome`,
      "document.referrer": "https://search.example/",
    });
    expect(span("resourceFetch")["http.url"]).toBe(`${ORIGIN}/assets/app.js`);
    expect(span("click")).toMatchObject({
      "location.href": `${ORIGIN}/welcome`,
      "http.url": `${ORIGIN}/welcome`,
    });
    expect(span("routeChange")).toMatchObject({
      "location.href": `${ORIGIN}/orders/17`,
      "prev.href": `${ORIGIN}/welcome`,
    });
    const fetched = span("HTTP GET");
    expect(fetched).toMatchObject({
      "http.url": `${ORIGIN}/api/search`,
      "url.full": `${ORIGIN}/api/search`,
      "url.path": "/api/search",
    });
    expect(fetched).not.toHaveProperty("url.query");
    expect(fetched).not.toHaveProperty("url.fragment");
    const event = spansOf().find((s) => s.name === "HTTP GET")!.events![0]!;
    expect(attrs(event.attributes)["http.url"]).toBe(`${ORIGIN}/api/next`);
    expect(span("onerror")["target_src"]).toBe(`${ORIGIN}/assets/chunk.js`);

    // Resource attributes too, and the batch still has ONE resource.
    const resources = batches.flatMap((b) => b.resourceSpans);
    expect(resources.length).toBe(batches.length);
    for (const r of resources) {
      expect(attrs(r.resource.attributes)).toMatchObject({
        "app.start_url": `${ORIGIN}/welcome`,
        "service.name": "web",
        "rum.sessionId": "91b955b313a1a86644ea53730a92deab",
      });
    }

    // Nothing that left carries a secret from a URL.
    const wire = JSON.stringify(batches);
    for (const secret of ["SECRET", "email=", "q=jane", "code=oauth", "v=3"]) {
      expect(
        wire.replace("opened /welcome?email=jane@example.com", ""),
      ).not.toContain(secret);
    }
  });

  it("leaves every non-URL attribute exactly as recorded, and the recorded spans untouched", async () => {
    const trace = await boot();
    await record(trace);

    expect(span("checkout_started")).toEqual({
      "location.href": `${ORIGIN}/orders/17`,
      component: "custom-action",
      message: "opened /welcome?email=jane@example.com",
      links: ["https://x.example/a?b=c"],
      count: 3,
    });
    expect(span("documentLoad")["http.user_agent"]).toBe(
      "Mozilla/5.0 (X11) HeadlessChrome",
    );
    expect(span("click")).toMatchObject({
      event_type: "click",
      target_element: "BUTTON",
      target_xpath: '//*[@id="go"]',
    });
    expect(span("HTTP GET")).toMatchObject({
      "http.method": "GET",
      "http.status_code": 200,
    });
    expect(span("onerror")["error.message"]).toBe("Failed to load script");
    // The export is a view: the span the SDK recorded keeps its own values.
    expect(recorded[0]!.attributes["http.url"]).toBe(PAGE);
  });

  it("keeps only the query parameters keepUrlQueryParams names, and never a fragment", async () => {
    const trace = await boot({ keepUrlQueryParams: ["utm_*", "ref"] });
    await record(trace);

    expect(span("documentLoad")).toMatchObject({
      "location.href": `${ORIGIN}/welcome?utm_source=news`,
      "http.url": `${ORIGIN}/welcome?utm_source=news`,
      "document.referrer": "https://search.example/",
    });
    expect(span("routeChange")).toMatchObject({
      "location.href": `${ORIGIN}/orders/17?ref=mail`,
      "prev.href": `${ORIGIN}/welcome?utm_source=news`,
    });
    expect(span("HTTP GET")).toMatchObject({
      "http.url": `${ORIGIN}/api/search?utm_campaign=x`,
      "url.full": `${ORIGIN}/api/search?utm_campaign=x`,
      "url.query": "utm_campaign=x",
    });
    expect(span("HTTP GET")).not.toHaveProperty("url.fragment");
    const wire = JSON.stringify(batches);
    expect(wire).not.toContain("SECRET");
    expect(wire).not.toContain("q=jane");
  });

  it("warns, and still runs, when the SDK's provider has no exporter to wrap", async () => {
    stubDom();
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const { trace } = await import("@opentelemetry/api");
    trace.disable();
    init.mockImplementation(() => {
      trace.setGlobalTracerProvider({
        getTracer: () => ({}) as never,
        forceFlush: () => Promise.resolve(),
      } as never);
    });
    const { t } = await load();

    await expect(
      t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" }),
    ).resolves.toBe(true);
    expect(warn).toHaveBeenCalledWith(
      expect.stringContaining("URLs are exported with their query"),
    );
    warn.mockRestore();
  });
});

describe("fetch and XHR span names", () => {
  type Hooked = { applyCustomAttributesOnSpan: (span: unknown) => void };

  function span(attributes: Record<string, unknown>) {
    const named = { name: "HTTP POST" };
    return {
      named,
      attributes,
      setAttribute: vi.fn((k: string, v: unknown) => {
        attributes[k] = v;
      }),
      updateName: vi.fn((n: string) => {
        named.name = n;
      }),
    };
  }

  it("renames HyperDX's HTTP <METHOD> to the called procedure on both instrumentations", async () => {
    const { t } = await load();
    const opts = t.buildSdkOptions(
      { serviceName: "web" },
      `${ORIGIN}/_otel`,
      ORIGIN,
    );
    const inst = opts.instrumentations as Record<string, Hooked>;

    for (const kind of ["fetch", "xhr"]) {
      const rpc = span({
        "http.method": "POST",
        "http.url":
          "https://api.example.com/reliant.v1.ChatService/SendMessage?x=1",
      });
      inst[kind]!.applyCustomAttributesOnSpan(rpc);
      expect(rpc.named.name).toBe("POST /reliant.v1.ChatService/SendMessage");

      const rest = span({
        "http.request.method": "get",
        "url.full": "https://api.example.com/v1/items/42/notes/abc123",
      });
      inst[kind]!.applyCustomAttributesOnSpan(rest);
      expect(rest.updateName).not.toHaveBeenCalled();
      expect(rest.named.name).toBe("HTTP POST");
    }
  });

  it("keeps the SDK's name when the span has no URL or cannot be renamed", async () => {
    const { t } = await load();
    const opts = t.buildSdkOptions(
      { serviceName: "web" },
      `${ORIGIN}/_otel`,
      ORIGIN,
    );
    const hook = (opts.instrumentations as Record<string, Hooked>).fetch!
      .applyCustomAttributesOnSpan;

    const bare = span({ "http.method": "POST" });
    hook(bare);
    expect(bare.updateName).not.toHaveBeenCalled();
    expect(() =>
      hook({
        attributes: { "http.url": "https://a.example/x" },
        setAttribute: vi.fn(),
      }),
    ).not.toThrow();
  });

  it("spanRouteName names Connect/gRPC procedures and nothing else", async () => {
    const { t } = await load();

    expect(t.spanRouteName("post", "/pkg.v1.Svc/Do")).toBe(
      "POST /pkg.v1.Svc/Do",
    );
    expect(
      t.spanRouteName("", "https://x.example/reliant.v1.ChatService/Send"),
    ).toBe("POST /reliant.v1.ChatService/Send");
    // A free-form path can hold values no pattern recognises (short ids,
    // opaque tokens, emails), so it never becomes a span name.
    for (const url of [
      "https://x.example/",
      "https://x.example/users/abc123",
      "https://x.example/invite/secret-token",
      "https://x.example/reset/qwertyuiopasdfghjklzxcvbnm",
      "https://x.example/users/a%40b.com/settings",
      "https://x.example/v1/items/42",
      "https://dash.example/auth/v1/token",
      "https://x.example/files.v1/secret",
      "https://x.example/pkg.v1.Svc/Do/extra",
    ]) {
      expect(t.spanRouteName("GET", url)).toBe("");
    }
    expect(t.spanRouteName("GET", "http://[bad")).toBe("");
  });
});

describe("decideReplay", () => {
  function store() {
    const m = new Map<string, string>();
    return {
      m,
      getItem: (k: string) => m.get(k) ?? null,
      setItem: (k: string, v: string) => void m.set(k, v),
    };
  }

  it("never records unless replay is on", async () => {
    const { t } = await load();

    expect(t.decideReplay({}, null, () => 0)).toBe(false);
    expect(t.decideReplay({ replaySampleRate: 1 }, null, () => 0)).toBe(false);
    expect(t.decideReplay({ replay: false }, null, () => 0)).toBe(false);
  });

  it("records every session by default and at rate 1; none at rate 0", async () => {
    const { t } = await load();
    const s = store();

    expect(t.decideReplay({ replay: true }, s, () => 0.99)).toBe(true);
    expect(
      t.decideReplay({ replay: true, replaySampleRate: 1 }, s, () => 0.99),
    ).toBe(true);
    expect(
      t.decideReplay({ replay: true, replaySampleRate: 0 }, s, () => 0),
    ).toBe(false);
    expect(s.m.size).toBe(0);
  });

  it("samples by the rate and holds the decision for the session", async () => {
    const { t } = await load();
    const s = store();
    const cfg = { replay: true, replaySampleRate: 0.1 };

    expect(t.decideReplay(cfg, s, () => 0.05)).toBe(true);
    // A later load re-rolling high must not flip a session that is recording.
    expect(t.decideReplay(cfg, s, () => 0.99)).toBe(true);

    const other = store();
    expect(t.decideReplay(cfg, other, () => 0.5)).toBe(false);
    expect(t.decideReplay(cfg, other, () => 0.01)).toBe(false);
  });

  it("clamps the rate, and treats a non-number as no recording", async () => {
    const { t } = await load();

    expect(
      t.decideReplay({ replay: true, replaySampleRate: 7 }, null, () => 0.99),
    ).toBe(true);
    expect(
      t.decideReplay({ replay: true, replaySampleRate: -1 }, null, () => 0),
    ).toBe(false);
    expect(
      t.decideReplay(
        { replay: true, replaySampleRate: Number.NaN },
        null,
        () => 0,
      ),
    ).toBe(false);
    expect(
      t.decideReplay(
        { replay: true, replaySampleRate: "1" as unknown as number },
        null,
        () => 0,
      ),
    ).toBe(false);
  });

  it("falls back to a fresh draw when storage throws", async () => {
    const { t } = await load();
    const broken = {
      getItem: () => {
        throw new Error("blocked");
      },
      setItem: () => {
        throw new Error("blocked");
      },
    };

    expect(
      t.decideReplay(
        { replay: true, replaySampleRate: 0.5 },
        broken,
        () => 0.1,
      ),
    ).toBe(true);
  });
});

describe("no session before the page is first visible", () => {
  function stubHiddenDocument() {
    const docListeners: Listener[] = [];
    const doc = {
      visibilityState: "hidden" as DocumentVisibilityState,
      addEventListener: (type: string, fn: Listener) => {
        if (type === "visibilitychange") docListeners.push(fn);
      },
      removeEventListener: (type: string, fn: Listener) => {
        const i = docListeners.indexOf(fn);
        if (type === "visibilitychange" && i >= 0) docListeners.splice(i, 1);
      },
    };
    vi.stubGlobal("document", doc);
    return {
      waiting: () => docListeners.length,
      show() {
        doc.visibilityState = "visible";
        for (const fn of [...docListeners]) fn({});
      },
    };
  }

  it("does not load the SDK while the page has never been visible, and starts it when it is", async () => {
    stubDom();
    const doc = stubHiddenDocument();
    const { t, r } = await load();
    let running: boolean | undefined;
    const started = t
      .initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" })
      .then((ok) => (running = ok));

    // Startup is parked on the visibility listener: once it is registered,
    // nothing but a visibilitychange can move it on. (The second listener
    // only notes when the page was hidden, for the paint vitals.)
    await vi.waitFor(() => expect(doc.waiting()).toBe(2));
    expect(init).not.toHaveBeenCalled();
    expect(running).toBeUndefined();
    // Nothing to record yet: the caller falls back to the console.
    expect(r.reportException(new Error("early"))).toBe(false);

    doc.show();
    await started;
    expect(running).toBe(true);
    expect(init).toHaveBeenCalledTimes(1);
  });

  it("starts at once on a visible page", async () => {
    stubDom();
    vi.stubGlobal("document", {
      visibilityState: "visible",
      addEventListener: () => {},
      removeEventListener: () => {},
    });
    const { t } = await load();
    expect(
      await t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" }),
    ).toBe(true);
    expect(init).toHaveBeenCalledTimes(1);
  });

  it("stays off when telemetry is disabled before the page is first shown", async () => {
    stubDom();
    const doc = stubHiddenDocument();
    const { t, k } = await load();
    const started = t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
    });
    k.disableTelemetry();
    doc.show();
    expect(await started).toBe(false);
    expect(init).not.toHaveBeenCalled();
  });
});

describe("paint vitals of a page first shown late", () => {
  // The SDK's own web-vitals instrumentation (@hyperdx/otel-web webvitals.js,
  // the version @hyperdx/browser pins), run over a fake tracer provider and
  // driven through its injectable web-vitals callbacks: it holds the
  // `webvitals` tracer from init and reports each vital as a span carrying
  // the metric value and navigation type, exported only once it is ENDED.
  type Vital = { name: string; value: number };
  type NavigationType = "navigate" | "prerender" | "back-forward-cache";
  const exported: Vital[] = [];
  let report: (
    name: string,
    value: number,
    navigationType?: NavigationType,
  ) => void = () => {};

  function fakeProvider() {
    const tracers = new Map<string, { startSpan(name: string): unknown }>();
    return {
      getTracer(name: string, version?: string) {
        const key = `${name}@${version ?? ""}`;
        if (!tracers.has(key)) {
          tracers.set(key, {
            startSpan() {
              const span = {
                attributes: {} as Record<string, unknown>,
                setAttribute(k: string, v: unknown) {
                  span.attributes[k] = v;
                  return span;
                },
                end() {
                  for (const [k, v] of Object.entries(span.attributes)) {
                    if (typeof v === "number") {
                      exported.push({ name: k, value: v });
                    }
                  }
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

  function stubDocument(state: DocumentVisibilityState) {
    const docListeners: Listener[] = [];
    const doc = {
      visibilityState: state,
      addEventListener: (type: string, fn: Listener) => {
        if (type === "visibilitychange") docListeners.push(fn);
      },
      removeEventListener: (type: string, fn: Listener) => {
        const i = docListeners.indexOf(fn);
        if (type === "visibilitychange" && i >= 0) docListeners.splice(i, 1);
      },
    };
    vi.stubGlobal("document", doc);
    return {
      waiting: () => docListeners.length,
      set(next: DocumentVisibilityState) {
        doc.visibilityState = next;
        for (const fn of [...docListeners]) fn({});
      },
    };
  }

  function stubPerformance(
    now: () => number,
    visibility: Array<{ name: string; startTime: number }> = [],
    activationStart = 0,
  ) {
    vi.stubGlobal("performance", {
      now,
      getEntriesByType: (type: string) =>
        type === "visibility-state"
          ? visibility
          : type === "navigation"
            ? [{ activationStart }]
            : [],
    });
  }

  async function load2() {
    stubDom();
    const { trace } = await import("@opentelemetry/api");
    // The real instrumentation; the package's exports map hides the file.
    const { initWebVitals } = await import(
      "../node_modules/@hyperdx/otel-web/dist/esm/webvitals.js"
    );
    trace.disable();
    trace.setGlobalTracerProvider(fakeProvider() as never);
    init.mockImplementation(() => {
      const provider = trace.getTracerProvider() as unknown as {
        getDelegate(): ReturnType<typeof fakeProvider>;
      };
      type Callback = (metric: {
        value: number;
        id: string;
        navigationType: NavigationType;
      }) => void;
      const callbacks = new Map<string, Callback>();
      const on = (name: string) => (fn: Callback) => {
        callbacks.set(name, fn);
      };
      initWebVitals(
        provider.getDelegate() as never,
        {
          onFID: on("fid"),
          onCLS: on("cls"),
          onLCP: on("lcp"),
          onINP: on("inp"),
          onFCP: on("fcp"),
          onTTFB: on("ttfb"),
        } as never,
      );
      let id = 0;
      report = (name, value, navigationType = "navigate") =>
        callbacks.get(name)?.({ value, id: `v${++id}`, navigationType });
    });
    return load();
  }

  function reportAll() {
    report("ttfb", 300);
    report("fcp", 61_000);
    report("lcp", 61_200);
    report("cls", 0.01);
  }

  beforeEach(() => {
    exported.length = 0;
    report = () => {};
  });

  afterEach(async () => {
    const { trace } = await import("@opentelemetry/api");
    trace.disable();
  });

  it("drops the FCP and LCP of a page loaded in the background, and keeps the rest", async () => {
    let clock = 250;
    stubPerformance(() => clock);
    const doc = stubDocument("hidden");
    const { t } = await load2();
    const started = t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
    });
    await vi.waitFor(() => expect(doc.waiting()).toBeGreaterThan(0));
    clock = 61_000;
    doc.set("visible");
    expect(await started).toBe(true);

    reportAll();
    expect(exported.map((v) => v.name).sort()).toEqual(["cls", "ttfb"]);
  });

  it("keeps every vital of a page that was visible from the start", async () => {
    stubPerformance(() => 250, [{ name: "visible", startTime: 0 }]);
    stubDocument("visible");
    const { t } = await load2();
    expect(
      await t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" }),
    ).toBe(true);

    reportAll();
    expect(exported.map((v) => v.name).sort()).toEqual([
      "cls",
      "fcp",
      "lcp",
      "ttfb",
    ]);
  });

  it("keeps a paint from before the browser's first hidden entry, and drops one after it", async () => {
    stubPerformance(
      () => 9_000,
      [
        { name: "visible", startTime: 0 },
        { name: "hidden", startTime: 5_000 },
        { name: "visible", startTime: 8_000 },
      ],
    );
    stubDocument("visible");
    const { t } = await load2();
    await t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" });

    report("fcp", 1_200);
    report("lcp", 6_000);
    expect(exported).toEqual([{ name: "fcp", value: 1_200 }]);
  });

  it("drops a paint after the page was hidden while the SDK loaded", async () => {
    let clock = 100;
    stubPerformance(() => clock);
    const doc = stubDocument("visible");
    const { t } = await load2();
    const started = t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
    });
    // Startup is parked on the SDK import: hide the page and show it again
    // before init runs, so web-vitals registers on a visible page.
    expect(init).not.toHaveBeenCalled();
    clock = 400;
    doc.set("hidden");
    clock = 3_000;
    doc.set("visible");
    expect(await started).toBe(true);

    report("lcp", 2_500);
    report("ttfb", 90);
    expect(exported).toEqual([{ name: "ttfb", value: 90 }]);
  });

  it("keeps the paints of a back/forward-cache restore, timed from the restore", async () => {
    // Loaded in the background, so the first load's paints are dropped; a
    // restore's value is the time since the restore, never since navigation.
    let clock = 250;
    stubPerformance(() => clock);
    const doc = stubDocument("hidden");
    const { t } = await load2();
    const started = t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
    });
    await vi.waitFor(() => expect(doc.waiting()).toBeGreaterThan(0));
    clock = 61_000;
    doc.set("visible");
    expect(await started).toBe(true);

    report("fcp", 61_000);
    report("fcp", 40, "back-forward-cache");
    report("lcp", 55, "back-forward-cache");
    expect(exported).toEqual([
      { name: "fcp", value: 40 },
      { name: "lcp", value: 55 },
    ]);
  });

  it("times a prerendered page's paints from its activation", async () => {
    // Hidden while prerendering (ignored, as web-vitals ignores it), shown at
    // activation (2 s), hidden again at 5 s. web-vitals reports a paint at
    // 3 s as 1 s after activation: kept; one at 6 s as 4 s: dropped.
    stubPerformance(
      () => 9_000,
      [
        { name: "hidden", startTime: 0 },
        { name: "visible", startTime: 2_000 },
        { name: "hidden", startTime: 5_000 },
        { name: "visible", startTime: 8_000 },
      ],
      2_000,
    );
    stubDocument("visible");
    const { t } = await load2();
    await t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" });

    report("fcp", 1_000, "prerender");
    report("lcp", 4_000, "prerender");
    expect(exported).toEqual([{ name: "fcp", value: 1_000 }]);
  });
});

describe("initBrowserTelemetry replay wiring", () => {
  function stubDomWithStorage(sampled?: "1" | "0") {
    const store = new Map<string, string>();
    if (sampled) store.set("forge.replay.sampled", sampled);
    listeners.clear();
    vi.stubGlobal("window", {
      location: { origin: ORIGIN },
      addEventListener: (type: string, fn: Listener) =>
        listeners.set(type, [...(listeners.get(type) ?? []), fn]),
      sessionStorage: {
        getItem: (k: string) => store.get(k) ?? null,
        setItem: (k: string, v: string) => void store.set(k, v),
      },
    });
  }

  it("starts the recorder only for a sampled session", async () => {
    stubDomWithStorage("0");
    const { t, k } = await load();
    k.grantAnalyticsConsent();
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      analytics: "full",
      replay: true,
      replaySampleRate: 0.5,
    });
    expect(
      (init.mock.calls[0]?.[0] as Record<string, unknown>).disableReplay,
    ).toBe(true);

    init.mockReset();
    stubDomWithStorage("1");
    const again = await load();
    again.k.grantAnalyticsConsent();
    await again.t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      analytics: "full",
      replay: true,
      replaySampleRate: 0.5,
    });
    expect(
      (init.mock.calls[0]?.[0] as Record<string, unknown>).disableReplay,
    ).toBe(false);
  });

  it("records no replay below the full tier, and keeps no sampling decision", async () => {
    // Replay records a person: the cookieless default never runs it, and
    // full without consent is still aggregate.
    for (const config of [
      { replay: true, replaySampleRate: 0.5 },
      { replay: true, analytics: "full" as const },
    ]) {
      init.mockReset();
      stubDomWithStorage();
      const store = (
        window as unknown as { sessionStorage: { getItem(k: string): unknown } }
      ).sessionStorage;
      const { t } = await load();
      await t.initBrowserTelemetry({
        serviceName: "web",
        endpoint: "/_otel",
        ...config,
      });
      expect(
        (init.mock.calls[0]?.[0] as Record<string, unknown>).disableReplay,
      ).toBe(true);
      expect(store.getItem("forge.replay.sampled")).toBeNull();
    }
  });
});

describe("track, setTelemetryUser and disableTelemetry through the SDK", () => {
  it("holds calls made before the SDK is up, then replays them in order", async () => {
    stubDom();
    const { t, k } = await load();

    k.setTelemetryUser("user-1");
    k.track("signup_started", { plan: "pro" });
    k.setTelemetryUser("user-2");
    expect(addAction).not.toHaveBeenCalled();

    await t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" });

    // Below the full tier there is no visitor to identify: no
    // user_identified, only the event.
    expect(addAction).toHaveBeenCalledTimes(1);
    expect(addAction).toHaveBeenNthCalledWith(1, "signup_started", {
      plan: "pro",
    });
    // The event replays under the user it was recorded with, and the last
    // user is what later events carry.
    const stamps = setGlobalAttributes.mock.calls
      .map(([attrs]) => (attrs as { userId?: string }).userId)
      .filter((id) => id !== undefined);
    expect(stamps.slice(-2)).toEqual(["user-1", "user-2"]);
    expect(
      setGlobalAttributes.mock.invocationCallOrder[
        setGlobalAttributes.mock.calls.findIndex(
          ([attrs]) => (attrs as { userId?: string }).userId === "user-1",
        )
      ],
    ).toBeLessThan(addAction.mock.invocationCallOrder[0]!);

    k.track("signup_completed");
    expect(addAction).toHaveBeenLastCalledWith("signup_completed", {});
  });

  it("clears the user with an empty id on sign-out", async () => {
    stubDom();
    const { t, k } = await load();
    await t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" });

    k.setTelemetryUser("u1");
    k.setTelemetryUser(null);

    expect(setGlobalAttributes).toHaveBeenLastCalledWith({ userId: "" });
  });

  it("drops held calls when telemetry never starts", async () => {
    stubDom();
    const { t, k } = await load();

    k.track("early");
    await t.initBrowserTelemetry({ serviceName: "web" }); // no endpoint: off
    expect(addAction).not.toHaveBeenCalled();

    // And nothing resurfaces if telemetry is later started.
    await t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" });
    expect(addAction).not.toHaveBeenCalled();
  });

  it("disableTelemetry stops the recorder and silences track, setTelemetryUser and reportException", async () => {
    stubDom();
    const { t, r, k } = await load();
    // Replay runs only in the full tier.
    k.grantAnalyticsConsent();
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      analytics: "full",
      replay: true,
    });
    expect(init.mock.calls[0]?.[0]).toMatchObject({ disableReplay: false });
    addAction.mockReset();

    k.disableTelemetry();
    k.track("after_consent_withdrawn", { a: 1 });
    k.setTelemetryUser("u1");
    const reported = r.reportException(new Error("late"));

    expect(stopSessionRecorder).toHaveBeenCalledTimes(1);
    expect(addAction).not.toHaveBeenCalled();
    expect(setGlobalAttributes).not.toHaveBeenCalledWith(
      expect.objectContaining({ userId: expect.anything() }),
    );
    // The caller is told no SDK has it, so it can fall back to console.error.
    expect(reported).toBe(false);
    expect(recordException).not.toHaveBeenCalled();
  });

  it("disableTelemetry before init means the SDK is never loaded", async () => {
    stubDom();
    const { t, k } = await load();

    k.disableTelemetry();
    k.track("queued_never");

    await expect(
      t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" }),
    ).resolves.toBe(false);
    expect(init).not.toHaveBeenCalled();
    expect(addAction).not.toHaveBeenCalled();
  });
});

describe("page views and the signed-in user", () => {
  function stubDomWithHistory(pathname: string) {
    stubDom();
    const location = (window as unknown as { location: Record<string, string> })
      .location;
    location.pathname = pathname;
    const go = (_s: unknown, _u: string, url?: string | URL | null) => {
      if (url) location.pathname = new URL(String(url), ORIGIN).pathname;
    };
    // The real methods, kept apart from window.history, which gets wrapped.
    const history = { pushState: vi.fn(go), replaceState: vi.fn(go) };
    (window as unknown as Record<string, unknown>).history = { ...history };
    return history;
  }

  // No fixed waits: a route change is reported synchronously inside
  // pushState, and the session watch calls fetch() synchronously when it
  // asks, so a call that did not happen by the next line never will. Only
  // the applied answer is awaited, by polling for it (vi.waitFor).
  it("records neither by default, and asks no session endpoint", async () => {
    const history = stubDomWithHistory("/");
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const { t } = await load();
    await t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" });
    window.history.pushState({}, "", "/orders/1");

    expect(window.history.pushState).toBe(history.pushState);
    expect(addAction).not.toHaveBeenCalled();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("records page_viewed per route and stamps the session's subject", async () => {
    stubDomWithHistory("/admin/auth/sign-in");
    const fetchMock = vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            authenticated: true,
            subject: "u-42",
            email: "e@x",
          }),
        ),
    );
    vi.stubGlobal("fetch", fetchMock);
    const { t } = await load();
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      pageViews: { basePath: "/admin" },
      identify: { sessionUrl: "http://api.test/auth/session" },
    });
    window.history.pushState({}, "", "/admin/orders/17?tab=items");
    await vi.waitFor(() =>
      expect(setGlobalAttributes).toHaveBeenCalledWith({ userId: "u-42" }),
    );

    expect(
      addAction.mock.calls.filter(([name]) => name === "page_viewed"),
    ).toEqual([
      ["page_viewed", { route: "/auth/sign-in", entry: "true" }],
      ["page_viewed", { route: "/orders/:id" }],
    ]);
    // Once at start, once on leaving the sign-in screen.
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(setGlobalAttributes).toHaveBeenCalledWith({ userId: "u-42" });
    expect(setGlobalAttributes).not.toHaveBeenCalledWith(
      expect.objectContaining({ email: expect.anything() }),
    );
  });

  it("disableTelemetry takes the history hooks and the session watch out", async () => {
    const history = stubDomWithHistory("/");
    const fetchMock = vi.fn(
      async () => new Response(JSON.stringify({ authenticated: false })),
    );
    vi.stubGlobal("fetch", fetchMock);
    const { t, k } = await load();
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      pageViews: true,
      identify: { sessionUrl: "/auth/session" },
    });
    expect(window.history.pushState).not.toBe(history.pushState);
    expect(fetchMock).toHaveBeenCalledTimes(1);

    k.disableTelemetry();
    addAction.mockReset();
    window.history.pushState({}, "", "/auth/sign-in");

    expect(window.history.pushState).toBe(history.pushState);
    expect(addAction).not.toHaveBeenCalled();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});

describe("resolveIngestUrl", () => {
  it("resolves a path against the page origin", async () => {
    const { t } = await load();

    expect(t.resolveIngestUrl("/_otel", ORIGIN)).toBe(`${ORIGIN}/_otel`);
    expect(t.resolveIngestUrl("/_otel/", ORIGIN)).toBe(`${ORIGIN}/_otel`);
  });

  it("returns null when telemetry is off", async () => {
    const { t } = await load();

    expect(t.resolveIngestUrl(undefined, ORIGIN)).toBeNull();
    expect(t.resolveIngestUrl("  ", ORIGIN)).toBeNull();
    expect(t.resolveIngestUrl("OFF", ORIGIN)).toBeNull();
  });
});

describe("app.client, visitor.id and user_identified", () => {
  it("stamps app.client and visitor.id as global attributes at init in the full tier", async () => {
    stubDom();
    const { t, k } = await load();
    k.grantAnalyticsConsent();
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      appClient: "landing",
      analytics: "full",
    });

    expect(setGlobalAttributes).toHaveBeenCalledWith({
      "app.client": "landing",
      "visitor.id": expect.stringMatching(/^[A-Za-z0-9_-]{8,64}$/),
    });
  });

  it("defaults app.client to web", async () => {
    stubDom();
    const { t } = await load();
    await t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" });

    expect(setGlobalAttributes).toHaveBeenCalledWith(
      expect.objectContaining({ "app.client": "web" }),
    );
  });

  it("adopts rid and utm from the URL and reports them once per user (legacy attribution: true)", async () => {
    // attribution: true, from before the tiers, still means "full, with
    // consent": an app that chose it keeps what it chose.
    const rid = "0b8f3c2e-6a1d-4f7e-9c55-2d1e8a7b4c90";
    stubDom();
    (
      window as unknown as { location: Record<string, string> }
    ).location.search = `?rid=${rid}&utm_source=news`;
    const { t, k } = await load();
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      attribution: true,
    });

    expect(setGlobalAttributes).toHaveBeenCalledWith(
      expect.objectContaining({ "visitor.id": rid }),
    );

    k.setTelemetryUser("user-1");
    k.setTelemetryUser("user-1");
    const identified = addAction.mock.calls.filter(
      ([name]) => name === "user_identified",
    );
    expect(identified).toEqual([
      ["user_identified", { visitor_id: rid, utm_source: "news" }],
    ]);
  });

  // A two-tier consent site: error telemetry for everyone, the visitor id only
  // with analytics consent.
  function stubDomWithLocalStorage(search = "") {
    const store = new Map<string, string>();
    stubDom();
    const w = window as unknown as Record<string, unknown>;
    (w.location as Record<string, string>).search = search;
    w.localStorage = {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
    };
    return store;
  }

  it("with attribution: false creates, stores and stamps no visitor id", async () => {
    const store = stubDomWithLocalStorage("?utm_source=news");
    const { t, k } = await load();
    const index = await import("./index.js");
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      appClient: "landing",
      attribution: false,
    });

    expect(setGlobalAttributes).toHaveBeenCalledWith({
      "app.client": "landing",
    });
    expect(store.size).toBe(0);
    // This page load's campaign is passed on to the site's own app; no
    // visitor id is, and an unrelated site gets nothing.
    expect(
      index.withAttribution("https://app.example.com/", {
        sites: ["app.example.com"],
      }),
    ).toBe("https://app.example.com/?utm_source=news");
    expect(index.withAttribution("https://elsewhere.example.org/")).toBe(
      "https://elsewhere.example.org/",
    );
    k.setTelemetryUser("user-1");
    expect(
      addAction.mock.calls.filter(([name]) => name === "user_identified"),
    ).toEqual([]);
  });

  it("enableAttribution stamps the visitor id on a running SDK", async () => {
    const store = stubDomWithLocalStorage();
    const { t } = await load();
    const index = await import("./index.js");
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      attribution: false,
      attributionTouch: "first",
      visitorIdStorageKey: "site.visitor_id",
    });
    setGlobalAttributes.mockReset();

    // The landing query string, kept from page load past a client-side navigation.
    t.enableAttribution({ search: "?utm_source=news" });
    t.enableAttribution();

    expect(setGlobalAttributes).toHaveBeenCalledTimes(1);
    const id = (
      setGlobalAttributes.mock.calls[0]?.[0] as Record<string, string>
    )["visitor.id"];
    expect(id).toMatch(/^[A-Za-z0-9_-]{8,64}$/);
    expect(store.get("site.visitor_id")).toBe(id);
    expect(index.getAttribution()).toEqual({ utm_source: "news" });
    expect(index.withAttribution("https://app.example.com/")).toBe(
      `https://app.example.com/?rid=${id}&utm_source=news`,
    );
  });

  it("enableAttribution before the SDK starts stamps it at start", async () => {
    stubDomWithLocalStorage();
    const { t } = await load();
    t.enableAttribution();
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      attribution: false,
    });

    expect(setGlobalAttributes).toHaveBeenCalledWith({
      "app.client": "web",
      "visitor.id": expect.stringMatching(/^[A-Za-z0-9_-]{8,64}$/),
    });
  });

  // The default config (no `analytics`, no `attribution`) is "identified";
  // init must not lower the "full" enableAttribution raised before it, or the
  // landing handoff loses its visitor id and user_identified never fires.
  it("enableAttribution before init with the default config keeps the full tier", async () => {
    stubDomWithLocalStorage();
    const { t, k } = await load();
    t.enableAttribution({ search: "?rid=v_landing_123456&utm_source=reddit" });
    await t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" });

    expect(setGlobalAttributes).toHaveBeenCalledWith(
      expect.objectContaining({ "visitor.id": "v_landing_123456" }),
    );
    k.setTelemetryUser("user-1");
    const identified = addAction.mock.calls.filter(
      ([name]) => name === "user_identified",
    );
    expect(identified).toHaveLength(1);
    expect(identified[0]?.[1]).toMatchObject({
      visitor_id: "v_landing_123456",
      utm_source: "reddit",
    });
  });

  it("an explicit aggregate mode still wins over enableAttribution before init", async () => {
    const store = stubDomWithLocalStorage();
    const { t, k } = await load();
    t.enableAttribution({ search: "?rid=v_landing_123456" });
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      analytics: "aggregate",
    });

    k.setTelemetryUser("user-1");
    expect(
      addAction.mock.calls.filter(([name]) => name === "user_identified"),
    ).toEqual([]);
    expect(store.size).toBe(0);
  });

  it("enableAttribution is a no-op after disableTelemetry", async () => {
    const store = stubDomWithLocalStorage();
    const { t, k } = await load();
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      attribution: false,
    });
    k.disableTelemetry();
    setGlobalAttributes.mockReset();
    t.enableAttribution();

    expect(setGlobalAttributes).not.toHaveBeenCalled();
    expect(store.size).toBe(0);
  });

  it("does not identify on sign-out", async () => {
    stubDom();
    const { t, k } = await load();
    await t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" });

    k.setTelemetryUser(null);
    expect(addAction).not.toHaveBeenCalled();
  });
});

describe("initBrowserTelemetry on an origin that drops cookies", () => {
  function stubDomDroppingCookies() {
    const store = new Map<string, string>();
    stubDom();
    vi.stubGlobal("window", {
      ...(globalThis.window as unknown as Record<string, unknown>),
      localStorage: {
        getItem: (k: string) => store.get(k) ?? null,
        setItem: (k: string, v: string) => void store.set(k, v),
        removeItem: (k: string) => void store.delete(k),
      },
    });
    const doc = {
      get cookie() {
        return "";
      },
      set cookie(_v: string) {},
    };
    vi.stubGlobal("document", doc);
    return { doc, store };
  }

  it("keeps the SDK's session cookie in localStorage before the SDK starts, in the full tier", async () => {
    const { doc, store } = stubDomDroppingCookies();
    let seenAtInit = "";
    init.mockImplementation(() => {
      // What the SDK's session module would read and write as it starts.
      doc.cookie = "__rum_sid=abc; path=/;max-age=900;SameSite=Strict";
      seenAtInit = doc.cookie;
    });
    const { t, k } = await load();
    k.grantAnalyticsConsent();
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      analytics: "full",
    });
    expect(init).toHaveBeenCalledTimes(1);
    expect(seenAtInit).toBe("__rum_sid=abc");
    expect(store.get(t.RUM_SESSION_STORAGE_KEY)).toContain('"v":"abc"');
  });
});

describe("the fetch context shim", () => {
  it("wraps the fetch the SDK patched, and delegates to it", async () => {
    stubDom();
    const sdkPatched = vi.fn(async () => new Response("ok"));
    vi.stubGlobal("fetch", async () => new Response("native"));
    init.mockImplementation(() => {
      // HyperDX's fetch instrumentation patches the global during init.
      globalThis.fetch = sdkPatched as unknown as typeof fetch;
    });
    const { t } = await load();
    await expect(
      t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" }),
    ).resolves.toBe(true);

    // The shim sits OVER the SDK's fetch, so a call it re-enters a context
    // for still opens the SDK's span inside that context.
    expect(globalThis.fetch).not.toBe(sdkPatched);
    const res = await globalThis.fetch("http://api.test/x", { method: "POST" });
    expect(await res.text()).toBe("ok");
    expect(sdkPatched).toHaveBeenCalledWith("http://api.test/x", {
      method: "POST",
    });
  });
});
