// This package carries no jsdom by design (see service-hooks.test.ts), so the
// DOM surfaces devlog touches — window/document.addEventListener, the event
// objects, navigator.sendBeacon — are stubbed here rather than pulling in a
// renderer. They are small and stable enough that a stub tests the same thing
// a DOM would.
//
// Every test that expects a POST must advance the fake clock past
// FLUSH_INTERVAL_MS (or trigger an error/threshold/unload flush): forwarding is
// batched, so a console call on its own produces no request yet.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  DEV_LOG_ENDPOINT,
  devLoggingInstalled,
  installDevLogging,
  uninstallDevLogging,
} from "../browser-log-forward";

interface Entry {
  level: string;
  msg: string;
}

type Listener = (event: unknown) => void;

const FLUSH_MS = 250;

/** Let queued microtasks (the error-flush path) run. */
async function microtasks(): Promise<void> {
  for (let i = 0; i < 5; i += 1) await Promise.resolve();
}

/** Minimal window/document stand-in that records listeners so tests can fire them. */
function stubDom(): { fire: (type: string, event?: unknown) => void } {
  const listeners = new Map<string, Set<Listener>>();
  const target = {
    addEventListener(type: string, fn: Listener) {
      const set = listeners.get(type) ?? new Set<Listener>();
      set.add(fn);
      listeners.set(type, set);
    },
    removeEventListener(type: string, fn: Listener) {
      listeners.get(type)?.delete(fn);
    },
  };
  vi.stubGlobal("window", target);
  vi.stubGlobal("document", { ...target, visibilityState: "visible" });
  return {
    fire(type, event) {
      for (const fn of listeners.get(type) ?? []) fn(event);
    },
  };
}

function bodyEntries(init?: RequestInit): Entry[] {
  const parsed = JSON.parse(String(init?.body)) as
    | { entries?: Entry[] }
    | Entry;
  const entries = (parsed as { entries?: Entry[] }).entries;
  return entries ?? [parsed as Entry];
}

interface FetchStub {
  /** Every request, as the list of entries it carried. */
  batches: () => Entry[][];
  /** All entries across all requests, in arrival order. */
  all: () => Entry[];
  calls: () => number;
  mock: ReturnType<typeof vi.fn>;
}

/** Capture what would have been POSTed, answering as a v2-aware endpoint. */
function stubFetch(
  respond?: (init?: RequestInit) => Promise<Response>,
): FetchStub {
  const batches: Entry[][] = [];
  const fetchMock = vi.fn((_url: string, init?: RequestInit) => {
    batches.push(bodyEntries(init));
    return respond
      ? respond(init)
      : Promise.resolve(
          new Response(null, {
            status: 204,
            headers: { "X-Forge-Devlog": "2" },
          }),
        );
  });
  vi.stubGlobal("fetch", fetchMock);
  return {
    batches: () => batches,
    all: () => batches.flat(),
    calls: () => fetchMock.mock.calls.length,
    mock: fetchMock as unknown as ReturnType<typeof vi.fn>,
  };
}

/** Record sendBeacon calls; `accept` decides the boolean it returns. */
function stubBeacon(accept = true): {
  sent: { url: string; body: unknown }[];
} {
  const sent: { url: string; body: unknown }[] = [];
  vi.stubGlobal("navigator", {
    sendBeacon: (url: string, body: unknown) => {
      sent.push({ url, body });
      return accept;
    },
  });
  return { sent };
}

describe("dev log forwarding", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.useFakeTimers();
    stubDom();
  });

  afterEach(() => {
    uninstallDevLogging();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("does nothing in production", () => {
    const f = stubFetch();
    installDevLogging({ dev: false });

    expect(devLoggingInstalled()).toBe(false);
    console.log("this must not ship anywhere");
    vi.advanceTimersByTime(FLUSH_MS * 4);
    expect(f.calls()).toBe(0);
  });

  // Regression: a bundler constant-folds `dev: import.meta.env.DEV` to false
  // and can then drop the property (or the whole argument) as dead weight,
  // leaving `installDevLogging({})` live in a production bundle. Observed in a
  // real `vite build` of the scaffold. `dev` must be fail-closed, not
  // defaulted, or that call silently installs the override in production.
  it("stays fail-closed when the bundler strips the dev flag", () => {
    const f = stubFetch();

    installDevLogging({} as never);
    expect(devLoggingInstalled()).toBe(false);

    installDevLogging({ dev: undefined } as never);
    expect(devLoggingInstalled()).toBe(false);

    console.log("must not be forwarded");
    vi.advanceTimersByTime(FLUSH_MS * 4);
    expect(f.calls()).toBe(0);
  });

  // THE point of protocol v2. Measured in a real app, one POST per console
  // call reached ~80 requests/s at light load and 200+/s in bursts, all of it
  // competing with the app's own RPCs for the browser's 6-per-origin
  // connection pool and the dev server's event loop.
  it("coalesces a burst of console calls into ONE request", () => {
    const f = stubFetch();
    installDevLogging({ dev: true, mirrorToConsole: false });

    console.log("one");
    console.info("two");
    console.warn("three", { userId: 42 });
    console.debug("four");
    expect(f.calls()).toBe(0); // nothing leaves synchronously

    vi.advanceTimersByTime(FLUSH_MS);

    expect(f.calls()).toBe(1);
    expect(f.batches()[0]).toEqual([
      { level: "log", msg: "one" },
      { level: "info", msg: "two" },
      { level: "warn", msg: 'three {"userId":42}' },
      { level: "debug", msg: "four" },
    ]);
  });

  it("posts batches to the forge endpoint convention as JSON", () => {
    const f = stubFetch();
    installDevLogging({ dev: true, mirrorToConsole: false });

    console.log("x");
    vi.advanceTimersByTime(FLUSH_MS);

    const [url, init] = f.mock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe(DEV_LOG_ENDPOINT);
    expect(init.method).toBe("POST");
    expect(init.keepalive).toBe(true);
    expect(init.credentials).toBe("omit");
    expect(
      (init.headers as Record<string, string>)["Content-Type"],
    ).toBe("application/json");
    expect(JSON.parse(String(init.body))).toEqual({
      entries: [{ level: "log", msg: "x" }],
    });
  });

  it("flushes early once buffered output crosses 32 KiB", () => {
    const f = stubFetch();
    installDevLogging({ dev: true, mirrorToConsole: false });

    // MAX_LINE is 8 000 chars, so five lines clear the 32 KiB threshold.
    for (let i = 0; i < 5; i += 1) console.log("y".repeat(7_000));

    expect(f.calls()).toBeGreaterThanOrEqual(1);
    // Every request stays inside the threshold so keepalive's 64 KiB in-flight
    // quota cannot reject it.
    for (const [, init] of f.mock.mock.calls as [string, RequestInit][]) {
      expect(String(init.body).length).toBeLessThanOrEqual(32 * 1024);
    }
  });

  it("flushes an error without waiting for the timer", async () => {
    const f = stubFetch();
    installDevLogging({ dev: true, mirrorToConsole: false });

    console.log("context before the failure");
    console.error("boom");
    await microtasks();

    // No timer advance at all: errors are the thing you are waiting for.
    expect(f.calls()).toBe(1);
    expect(f.all().map((e) => e.level)).toEqual(["log", "error"]);
  });

  it("coalesces a same-tick error burst into one request", async () => {
    const f = stubFetch();
    installDevLogging({ dev: true, mirrorToConsole: false });

    console.error("first");
    console.error("second");
    console.error("third");
    await microtasks();

    expect(f.calls()).toBe(1);
    expect(f.all()).toHaveLength(3);
  });

  it("keeps one request in flight so arrival order is the log order", async () => {
    let release: (() => void) | undefined;
    const f = stubFetch(
      () =>
        new Promise<Response>((resolve) => {
          release = () =>
            resolve(
              new Response(null, {
                status: 204,
                headers: { "X-Forge-Devlog": "2" },
              }),
            );
        }),
    );
    installDevLogging({ dev: true, mirrorToConsole: false });

    console.log("first batch");
    vi.advanceTimersByTime(FLUSH_MS);
    expect(f.calls()).toBe(1);

    console.log("second batch");
    vi.advanceTimersByTime(FLUSH_MS * 4);
    // Still one: the second batch waits rather than racing the first.
    expect(f.calls()).toBe(1);

    release?.();
    await microtasks();

    expect(f.calls()).toBe(2);
    expect(f.all().map((e) => e.msg)).toEqual(["first batch", "second batch"]);
  });

  it("beacons what is buffered when the page goes away", () => {
    stubFetch();
    const beacon = stubBeacon();
    const dom = stubDom();
    installDevLogging({ dev: true, mirrorToConsole: false });

    console.log("last words");
    dom.fire("pagehide");

    expect(beacon.sent).toHaveLength(1);
    expect(beacon.sent[0]?.url).toBe(DEV_LOG_ENDPOINT);
    const blob = beacon.sent[0]?.body as Blob;
    // text/plain keeps sendBeacon out of CORS preflight; the endpoint parses
    // the body regardless of Content-Type for exactly this reason. Blob
    // lowercases the type it was given, hence the case-insensitive compare.
    expect(blob.type.toLowerCase()).toBe("text/plain;charset=utf-8");
  });

  it("beacons on visibilitychange to hidden", () => {
    stubFetch();
    const beacon = stubBeacon();
    const dom = stubDom();
    installDevLogging({ dev: true, mirrorToConsole: false });

    console.log("backgrounded");
    (globalThis as { document: { visibilityState: string } }).document.visibilityState =
      "hidden";
    dom.fire("visibilitychange");

    expect(beacon.sent).toHaveLength(1);
  });

  // The endpoints are scaffold-once files the project owns, so a runtime
  // upgraded through npm WILL meet an endpoint that predates batching. Such an
  // endpoint answers 204 and prints the batch as one empty line — every
  // frontend log line would vanish with no error anywhere.
  it("falls back to one-request-per-line against a pre-batch endpoint", async () => {
    const f = stubFetch(() => Promise.resolve(new Response(null, { status: 204 })));
    installDevLogging({ dev: true, mirrorToConsole: false });

    console.log("alpha");
    console.log("beta");
    vi.advanceTimersByTime(FLUSH_MS);
    expect(f.calls()).toBe(1); // the batch that discovers the old endpoint
    await microtasks();

    // Re-sent one per POST, preceded by a notice that says what happened.
    const bodies = f.mock.mock.calls
      .slice(1)
      .map(([, init]) => bodyEntries(init as RequestInit));
    expect(bodies.every((b) => b.length === 1)).toBe(true);
    const msgs = bodies.flat().map((e) => e.msg);
    expect(msgs[0]).toContain("predates batched posts");
    expect(msgs.slice(1)).toEqual(["alpha", "beta"]);

    // And every later line is v1 too — one POST each, no `entries` wrapper.
    const before = f.calls();
    console.log("gamma");
    vi.advanceTimersByTime(FLUSH_MS);
    await microtasks();
    expect(f.calls()).toBe(before + 1);
    const last = f.mock.mock.calls.at(-1) as [string, RequestInit];
    expect(JSON.parse(String(last[1].body))).toEqual({
      level: "log",
      msg: "gamma",
    });
  });

  it("reports dropped lines rather than losing them silently", async () => {
    let fail = true;
    const f = stubFetch(() =>
      fail
        ? Promise.reject(new Error("dev server went away"))
        : Promise.resolve(
            new Response(null, {
              status: 204,
              headers: { "X-Forge-Devlog": "2" },
            }),
          ),
    );
    installDevLogging({ dev: true, mirrorToConsole: false });

    console.log("lost");
    vi.advanceTimersByTime(FLUSH_MS);
    await microtasks();

    fail = false;
    console.log("kept");
    vi.advanceTimersByTime(FLUSH_MS);
    await microtasks();

    const last = f.batches().at(-1) ?? [];
    expect(last[0]?.level).toBe("warn");
    expect(last[0]?.msg).toContain("[forge-devlog] dropped 1 lines");
    expect(last[1]?.msg).toBe("kept");

    // The counter resets: the next batch carries no notice.
    console.log("after");
    vi.advanceTimersByTime(FLUSH_MS);
    await microtasks();
    expect(f.batches().at(-1)).toEqual([{ level: "log", msg: "after" }]);
  });

  it("bounds the buffer and says how much it dropped", async () => {
    let release: (() => void) | undefined;
    const f = stubFetch(
      () =>
        new Promise<Response>((resolve) => {
          release = () =>
            resolve(
              new Response(null, {
                status: 204,
                headers: { "X-Forge-Devlog": "2" },
              }),
            );
        }),
    );
    installDevLogging({ dev: true, mirrorToConsole: false });

    // Park one request in flight so the buffer can only grow.
    console.log("in flight");
    vi.advanceTimersByTime(FLUSH_MS);

    for (let i = 0; i < 5_100; i += 1) console.log(`line ${i}`);

    release?.();
    await microtasks();

    const notice = f
      .all()
      .find((e) => e.msg.startsWith("[forge-devlog] dropped"));
    expect(notice?.level).toBe("warn");
    expect(notice?.msg).toContain("buffer full");
  });

  it("does not recurse when fetch itself logs", () => {
    const posted: Entry[][] = [];
    // A fetch implementation that logs — the recursion trap this guards.
    vi.stubGlobal(
      "fetch",
      vi.fn((_url: string, init?: RequestInit) => {
        posted.push(bodyEntries(init));
        console.log("fetch internals talking");
        return Promise.resolve(
          new Response(null, {
            status: 204,
            headers: { "X-Forge-Devlog": "2" },
          }),
        );
      }),
    );
    installDevLogging({ dev: true, mirrorToConsole: false });

    console.log("one line");
    vi.advanceTimersByTime(FLUSH_MS * 4);

    // Exactly one POST: the re-entrant log is dropped, not looped.
    expect(posted).toHaveLength(1);
    expect(posted[0]).toEqual([{ level: "log", msg: "one line" }]);
  });

  it("still writes to the real console by default", () => {
    stubFetch();
    const spy = vi.spyOn(console, "log").mockImplementation(() => {});
    installDevLogging({ dev: true });

    console.log("visible in devtools");

    expect(spy).toHaveBeenCalledWith("visible in devtools");
  });

  it("keeps Error stacks intact", async () => {
    const f = stubFetch();
    installDevLogging({ dev: true, mirrorToConsole: false });

    console.error(new Error("boom"));
    await microtasks();

    const entry = f.all()[0];
    expect(entry?.level).toBe("error");
    expect(entry?.msg).toContain("Error: boom");
    // The stack is the reason this feature exists; assert it survived.
    // (Vendored copy: forge's original asserts "devlog.test", its own filename.)
    expect(entry?.msg).toContain("browser-log-forward.test");
  });

  it("describes circular objects and bigints instead of degrading them", () => {
    const f = stubFetch();
    installDevLogging({ dev: true, mirrorToConsole: false });

    const circular: Record<string, unknown> = { name: "root" };
    circular.self = circular;
    expect(() => console.log(circular)).not.toThrow();
    console.log({ big: 9_007_199_254_740_993n });
    vi.advanceTimersByTime(FLUSH_MS);

    const [first, second] = f.all();
    // The old renderer fell back to String(value) here, which is the useless
    // "[object Object]" — the shape is what you were trying to read.
    expect(first?.msg).toContain('"name":"root"');
    expect(first?.msg).toContain("[Circular]");
    expect(second?.msg).toContain("9007199254740993n");
  });

  it("truncates a runaway line", () => {
    const f = stubFetch();
    installDevLogging({ dev: true, mirrorToConsole: false });

    console.log("z".repeat(20_000));
    vi.advanceTimersByTime(FLUSH_MS);

    const msg = f.all()[0]?.msg ?? "";
    expect(msg.length).toBeLessThan(9_000);
    expect(msg).toContain("(truncated)");
  });

  it("forwards uncaught errors that produced no console call", async () => {
    const f = stubFetch();
    const dom = stubDom();
    installDevLogging({ dev: true, mirrorToConsole: false });

    dom.fire("error", { error: new Error("UNCAUGHT") });
    await microtasks();

    expect(f.all()).toHaveLength(1);
    expect(f.all()[0]?.level).toBe("error");
    expect(f.all()[0]?.msg).toContain("UNCAUGHT");
  });

  it("forwards unhandled promise rejections", async () => {
    const f = stubFetch();
    const dom = stubDom();
    installDevLogging({ dev: true, mirrorToConsole: false });

    dom.fire("unhandledrejection", { reason: new Error("REJECTED") });
    await microtasks();

    expect(f.all()).toHaveLength(1);
    expect(f.all()[0]?.msg).toContain("unhandled rejection:");
    expect(f.all()[0]?.msg).toContain("REJECTED");
  });

  it("restores the original console on uninstall and flushes what is buffered", () => {
    const f = stubFetch();
    const before = console.log;
    installDevLogging({ dev: true, mirrorToConsole: false });
    expect(console.log).not.toBe(before);

    console.log("buffered at teardown");
    expect(f.calls()).toBe(0);

    uninstallDevLogging();

    expect(console.log).toBe(before);
    expect(devLoggingInstalled()).toBe(false);
    expect(f.all()).toEqual([{ level: "log", msg: "buffered at teardown" }]);

    const posts = f.calls();
    console.log("after uninstall");
    vi.advanceTimersByTime(FLUSH_MS * 4);
    expect(f.calls()).toBe(posts);
  });
});
