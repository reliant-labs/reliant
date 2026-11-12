// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The analytics tiers end to end through initBrowserTelemetry, against a
// fake of the HyperDX SDK that keeps its session the way the real one does
// (a `__rum_sid` cookie, written with max-age and read back through
// decodeURIComponent(document.cookie): the only device state the real SDK
// keeps), a cookie jar with a browser's semantics, and real-shaped Storage.
//
// The headline is "stores nothing": in the default `aggregate` tier, after
// every emitter the runtime has has run, document.cookie, localStorage and
// sessionStorage are exactly as empty as they started.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const init = vi.fn();
const addAction = vi.fn();
const setGlobalAttributes = vi.fn();
const stopSessionRecorder = vi.fn();
const recordException = vi.fn();

// The SDK's session module, as @hyperdx/otel-web writes it: read the cookie
// back, mint a session when there is none, write it with max-age=900.
function sdkSessionId(): string {
  const jar = decodeURIComponent(document.cookie).split(";");
  for (const part of jar) {
    const entry = part.trim();
    if (entry.startsWith("__rum_sid=")) {
      return (JSON.parse(entry.slice("__rum_sid=".length)) as { id: string })
        .id;
    }
  }
  const id = `s${Math.random().toString(16).slice(2, 10)}`;
  document.cookie = `__rum_sid=${encodeURIComponent(
    JSON.stringify({ id, startTime: Date.now() }),
  )}; path=/;max-age=900;SameSite=Strict`;
  return id;
}

const client = {
  init: (...args: unknown[]) => {
    sdkSessionId();
    return init(...args);
  },
  recordException: (...args: unknown[]) => recordException(...args),
  addAction: (...args: unknown[]) => addAction(...args),
  setGlobalAttributes: (...args: unknown[]) => setGlobalAttributes(...args),
  stopSessionRecorder: (...args: unknown[]) => stopSessionRecorder(...args),
  getSessionId: () => sdkSessionId(),
};

vi.mock("@hyperdx/browser", () => ({ default: client }));

/** A cookie jar with a browser's semantics, on the prototype like Document's. */
class FakeDocument {
  jar = new Map<string, string>();
  writes: string[] = [];
  referrer = "";
  visibilityState = "visible";
  addEventListener(): void {}
  removeEventListener(): void {}
  get cookie(): string {
    return [...this.jar].map(([k, v]) => `${k}=${v}`).join("; ");
  }
  set cookie(text: string) {
    this.writes.push(text);
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

/** Storage with the real interface, length included. */
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
  clear(): void {
    this.map.clear();
  }
}

const ORIGIN = "https://shop.example.com";

interface Dom {
  doc: FakeDocument;
  local: FakeStorage;
  session: FakeStorage;
  location: { pathname: string; search: string };
  nav: { globalPrivacyControl?: boolean; doNotTrack?: string };
}

function stubBrowser(path = "/", search = ""): Dom {
  const doc = new FakeDocument();
  const local = new FakeStorage();
  const session = new FakeStorage();
  const nav: Dom["nav"] = {};
  const location = {
    origin: ORIGIN,
    host: "shop.example.com",
    pathname: path,
    search,
  };
  const go = (_s: unknown, _u: string, url?: string | URL | null) => {
    if (url) {
      const next = new URL(String(url), ORIGIN);
      location.pathname = next.pathname;
      location.search = next.search;
    }
  };
  vi.stubGlobal("document", doc);
  vi.stubGlobal("navigator", nav);
  vi.stubGlobal("localStorage", local);
  vi.stubGlobal("sessionStorage", session);
  vi.stubGlobal("window", {
    location,
    localStorage: local,
    sessionStorage: session,
    history: { pushState: go, replaceState: go },
    addEventListener: () => {},
    removeEventListener: () => {},
  });
  return { doc, local, session, location, nav };
}

async function load() {
  vi.resetModules();
  const t = await import("./telemetry.js");
  const index = await import("./index.js");
  return { t, index };
}

function actions(name: string): unknown[][] {
  return addAction.mock.calls.filter(([n]) => n === name);
}

function stamped(key: string): unknown[] {
  return setGlobalAttributes.mock.calls
    .map(([attrs]) => (attrs as Record<string, unknown>)[key])
    .filter((v) => v !== undefined);
}

beforeEach(() => {
  for (const m of [
    init,
    addAction,
    setGlobalAttributes,
    stopSessionRecorder,
    recordException,
  ]) {
    m.mockReset();
  }
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("the cookieless modes: identified (the default) and aggregate", () => {
  it.each([
    { mode: undefined, tier: "identified", userIds: ["u-1"] },
    { mode: "identified" as const, tier: "identified", userIds: ["u-1"] },
    { mode: "aggregate" as const, tier: "aggregate", userIds: [] },
  ])(
    "stores nothing in mode $mode: no cookie, no localStorage, no sessionStorage",
    async ({ mode, tier, userIds }) => {
      const dom = stubBrowser(
        "/pricing",
        "?utm_source=news&utm_medium=email&gclid=Cj0KCQ-secret",
      );
      dom.doc.referrer = "https://news.ycombinator.com/item?id=1";
      vi.stubGlobal(
        "fetch",
        vi.fn(
          async () =>
            new Response(
              JSON.stringify({ authenticated: true, subject: "u-1" }),
            ),
        ),
      );
      const { t, index } = await load();

      await t.initBrowserTelemetry({
        serviceName: "web",
        endpoint: "/_otel",
        analytics: mode,
        pageViews: true,
        identify: { sessionUrl: "/auth/session" },
        replay: true,
      });
      // Every emitter the runtime has, in the order an app would hit them.
      index.track("checkout_started", { plan: "pro" });
      window.history.pushState({}, "", "/orders/17");
      index.identifyUser("u-1", { provider: "clerk" });
      index.trackAuthEvent("signed_in", { userId: "u-1", method: "password" });
      index.trackExposure("new-checkout", "treatment");
      index.forgeExposureHook.after(
        { flagKey: "dark-mode", providerMetadata: { name: "flagd" } },
        { flagKey: "dark-mode", value: true, variant: "on" },
      );
      expect(index.withAttribution("https://app.example.com/")).toBe(
        "https://app.example.com/",
      );
      // The SDK checks its session again, as it does every minute.
      const first = client.getSessionId();
      expect(client.getSessionId()).toBe(first);

      expect(index.analyticsTier()).toBe(tier);
      // What the browser's cookie jar holds (the page itself still reads its
      // in-memory session through document.cookie, which is the point).
      expect(dom.doc.jar.size).toBe(0);
      expect(
        Object.getOwnPropertyDescriptor(
          FakeDocument.prototype,
          "cookie",
        )?.get?.call(dom.doc),
      ).toBe("");
      expect(dom.doc.writes).toEqual([]);
      expect(dom.local.length).toBe(0);
      expect(dom.session.length).toBe(0);

      // And it still measured: events flowed, with no persistent id.
      expect(actions("page_viewed").length).toBe(2);
      expect(actions("checkout_started").length).toBe(1);
      expect(actions("flag_evaluated").length).toBe(2);
      expect(actions("auth_signed_in").length).toBeGreaterThan(0);
      expect(actions("user_identified")).toEqual([]);
      expect(stamped("visitor.id")).toEqual([]);
      // identified carries the signed-in subject; aggregate is strictly
      // anonymous, signed in or not.
      expect(stamped("userId").filter((v) => v !== "")).toEqual(
        userIds.length ? expect.arrayContaining(userIds) : [],
      );
      expect(
        (init.mock.calls[0]?.[0] as Record<string, unknown>).disableReplay,
      ).toBe(true);
    },
  );

  it("erases what an earlier page load stored under the old default", async () => {
    const dom = stubBrowser();
    dom.local.setItem("forge.visitor_id", "0b8f3c2e-6a1d-4f7e-9c55");
    dom.local.setItem("forge.attribution", '{"utm_source":"news"}');
    dom.local.setItem("forge.identified_user", "u-1");
    dom.session.setItem("forge.replay.sampled", "1");
    dom.local.setItem("app.own_key", "kept");
    dom.doc.cookie = "__rum_sid=old; path=/; max-age=900";
    dom.doc.writes = [];
    const { t } = await load();
    expect(t.REPLAY_SAMPLE_KEY).toBe("forge.replay.sampled");
    await t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" });

    expect([...dom.local.map.keys()]).toEqual(["app.own_key"]);
    expect(dom.session.length).toBe(0);
    expect(dom.doc.jar.has("__rum_sid")).toBe(false);
  });

  it("names the visit's source on the entry page view, never the click id", async () => {
    const dom = stubBrowser(
      "/landing",
      "?utm_source=news&utm_campaign=launch&utm_content=jane%40example.com&ref=producthunt&fbclid=IwAR-secret",
    );
    dom.doc.referrer = "https://www.google.com/search?q=private+query";
    const { t } = await load();
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      pageViews: true,
    });
    window.history.pushState({}, "", "/pricing");

    expect(actions("page_viewed")).toEqual([
      [
        "page_viewed",
        {
          route: "/landing",
          entry: "true",
          referrer_host: "www.google.com",
          referrer_path: "/search",
          utm_source: "news",
          utm_campaign: "launch",
          ref: "producthunt",
          click_id: "meta",
        },
      ],
      // A client-side navigation: not the entry, and the page's original
      // referrer is not where this view came from.
      ["page_viewed", { route: "/pricing" }],
    ]);
    const sent = JSON.stringify(addAction.mock.calls);
    for (const secret of ["IwAR-secret", "private", "jane"]) {
      expect(sent.includes(secret), secret).toBe(false);
    }
  });

  it("records no referrer from this site's own hosts", async () => {
    const dom = stubBrowser("/");
    dom.doc.referrer = "https://example.com/blog";
    const { t } = await load();
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      pageViews: { siteHosts: ["example.com"] },
    });
    expect(actions("page_viewed")).toEqual([
      ["page_viewed", { route: "/", entry: "true" }],
    ]);
  });
});

describe("GPC and DNT", () => {
  it("cap the browser at aggregate: events flow, no user id, nothing stored, even in full with consent", async () => {
    const dom = stubBrowser();
    dom.nav.globalPrivacyControl = true;
    const { t, index } = await load();
    index.grantAnalyticsConsent();
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      analytics: "full",
      pageViews: true,
    });
    index.setTelemetryUser("u-1");
    index.track("checkout_started");

    expect(index.analyticsTier()).toBe("aggregate");
    expect(stamped("userId").filter((v) => v !== "")).toEqual([]);
    expect(stamped("visitor.id")).toEqual([]);
    expect(actions("checkout_started").length).toBe(1);
    expect(actions("page_viewed").length).toBe(1);
    expect(dom.doc.jar.size).toBe(0);
    expect(dom.local.length).toBe(0);
  });
});

describe("the full tier", () => {
  it("needs mode full AND consent; consent moves the running session to the cookie", async () => {
    const dom = stubBrowser();
    const { t, index } = await load();
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      analytics: "full",
    });
    const session = client.getSessionId();
    expect(index.analyticsTier()).toBe("aggregate");
    expect(dom.doc.jar.size).toBe(0);

    index.setTelemetryUser("u-1");
    index.grantAnalyticsConsent();

    expect(index.analyticsTier()).toBe("full");
    expect(dom.doc.jar.has("__rum_sid")).toBe(true);
    // The same session, not a new one.
    expect(client.getSessionId()).toBe(session);
    const visitor = stamped("visitor.id")[0];
    expect(visitor).toMatch(/^[A-Za-z0-9_-]{8,64}$/);
    expect(dom.local.getItem("forge.visitor_id")).toBe(visitor);
    // The signed-in user is linked to the visitor once consent arrives.
    expect(actions("user_identified")).toEqual([
      ["user_identified", { visitor_id: visitor }],
    ]);
  });

  it("revokeAnalyticsConsent erases the device and keeps the session in memory", async () => {
    const dom = stubBrowser();
    const { t, index } = await load();
    index.grantAnalyticsConsent();
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      analytics: "full",
      replay: true,
    });
    const session = client.getSessionId();
    expect(dom.doc.jar.has("__rum_sid")).toBe(true);
    expect(dom.local.getItem("forge.visitor_id")).not.toBeNull();
    setGlobalAttributes.mockReset();

    index.revokeAnalyticsConsent();

    expect(index.analyticsTier()).toBe("aggregate");
    expect(dom.doc.jar.size).toBe(0);
    expect(dom.local.length).toBe(0);
    expect(dom.session.length).toBe(0);
    expect(setGlobalAttributes).toHaveBeenCalledWith({ "visitor.id": "" });
    expect(stopSessionRecorder).toHaveBeenCalled();
    expect(index.withAttribution("https://app.example.com/")).toBe(
      "https://app.example.com/",
    );
    // The SDK starts a fresh in-memory session; nothing reaches the jar.
    expect(client.getSessionId()).not.toBe(session);
    expect(dom.doc.jar.size).toBe(0);
  });
});

describe("optOutOfAnalytics", () => {
  it("stops product events and the user id, stores only the objection, and holds on the next load", async () => {
    const dom = stubBrowser();
    const { t, index } = await load();
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      pageViews: true,
    });
    index.setTelemetryUser("u-1");
    addAction.mockReset();
    setGlobalAttributes.mockReset();

    index.optOutOfAnalytics();
    index.track("after_objection");
    window.history.pushState({}, "", "/later");
    index.trackExposure("flag", "on");

    expect(index.analyticsTier()).toBe("off");
    expect(addAction).not.toHaveBeenCalled();
    expect(setGlobalAttributes).toHaveBeenCalledWith({ userId: "" });
    expect([...dom.local.map]).toEqual([["forge.analytics_opt_out", "1"]]);
    // Errors still flow: the objection is to analytics, not to error reports.
    index.reportException(new Error("still reported"));
    expect(recordException).toHaveBeenCalledTimes(1);

    // The next page load honours it.
    init.mockReset();
    const next = await load();
    await next.t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      pageViews: true,
    });
    expect(next.index.analyticsTier()).toBe("off");
    expect(addAction).not.toHaveBeenCalled();

    // Until the person agrees again.
    next.index.grantAnalyticsConsent();
    expect(next.index.analyticsTier()).toBe("aggregate");
    expect(dom.local.length).toBe(0);
  });
});

describe("mode off", () => {
  it("records no product events and no user id; errors still flow", async () => {
    stubBrowser();
    const { t, index } = await load();
    await t.initBrowserTelemetry({
      serviceName: "web",
      endpoint: "/_otel",
      analytics: "off",
      pageViews: true,
    });
    index.setTelemetryUser("u-1");
    index.track("ignored");
    index.reportException(new Error("boom"));

    expect(index.analyticsTier()).toBe("off");
    expect(addAction).not.toHaveBeenCalled();
    expect(stamped("userId").filter((v) => v !== "")).toEqual([]);
    expect(recordException).toHaveBeenCalledTimes(1);
  });
});

describe("enableAttribution, deprecated", () => {
  it("still turns the visitor id on for an app on the default mode", async () => {
    const dom = stubBrowser();
    const { t, index } = await load();
    await t.initBrowserTelemetry({ serviceName: "web", endpoint: "/_otel" });
    t.enableAttribution();
    expect(index.analyticsTier()).toBe("full");
    expect(dom.local.getItem("forge.visitor_id")).not.toBeNull();
  });
});
