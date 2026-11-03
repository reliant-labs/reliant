// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The contract of initBrowserTelemetry, pinned against a fake of the HyperDX
// SDK: SSR is a no-op, init is idempotent, the ingest URL is same-origin,
// replay is off by default and masked when on, and no real secret is ever in
// the options. The SDK itself is mocked — its behaviour is HyperDX's to test;
// what is ours is exactly what we hand it.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

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
      expect(opts.replay).toEqual({ inlineImages: false, collectFonts: false });
    }
    for (const tag of ["img", "picture", "video", "audio", "iframe"]) {
      expect(t.REPLAY_BLOCK_SELECTOR.split(/,\s*/)).toContain(tag);
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
    const { t } = await load();
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      replay: true,
      replaySampleRate: 0.5,
    });
    expect(
      (init.mock.calls[0]?.[0] as Record<string, unknown>).disableReplay,
    ).toBe(true);

    init.mockReset();
    stubDomWithStorage("1");
    const again = await load();
    await again.t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      replay: true,
      replaySampleRate: 0.5,
    });
    expect(
      (init.mock.calls[0]?.[0] as Record<string, unknown>).disableReplay,
    ).toBe(false);
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

    expect(addAction).toHaveBeenCalledTimes(2);
    expect(addAction).toHaveBeenNthCalledWith(1, "signup_started", {
      plan: "pro",
    });
    // The last user wins; the stale one is not replayed or identified.
    expect(addAction).toHaveBeenNthCalledWith(
      2,
      "user_identified",
      expect.objectContaining({ visitor_id: expect.any(String) }),
    );
    expect(setGlobalAttributes).toHaveBeenCalledWith({ userId: "user-2" });
    expect(setGlobalAttributes).not.toHaveBeenCalledWith({ userId: "user-1" });

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
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      replay: true,
    });

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
  it("stamps app.client and visitor.id as global attributes at init", async () => {
    stubDom();
    const { t } = await load();
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      appClient: "landing",
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

  it("adopts rid and utm from the URL and reports them once per user", async () => {
    const rid = "0b8f3c2e-6a1d-4f7e-9c55-2d1e8a7b4c90";
    stubDom();
    (
      window as unknown as { location: Record<string, string> }
    ).location.search = `?rid=${rid}&utm_source=news`;
    const { t, k } = await load();
    await t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" });

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
    expect(index.withAttribution("https://app.example.com/")).toBe(
      "https://app.example.com/",
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

  it("keeps the SDK's session cookie in localStorage before the SDK starts", async () => {
    const { doc, store } = stubDomDroppingCookies();
    let seenAtInit = "";
    init.mockImplementation(() => {
      // What the SDK's session module would read and write as it starts.
      doc.cookie = "__rum_sid=abc; path=/;max-age=900;SameSite=Strict";
      seenAtInit = doc.cookie;
    });
    const { t } = await load();
    await t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" });
    expect(init).toHaveBeenCalledTimes(1);
    expect(seenAtInit).toBe("__rum_sid=abc");
    expect(store.get(t.RUM_SESSION_STORAGE_KEY)).toContain('"v":"abc"');
  });
});
