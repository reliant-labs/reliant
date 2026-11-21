// The contract of the automatic page views: what a route is (pathname only,
// basePath off, ids replaced), when one is reported (once per new route,
// through either the Navigation API or wrapped history), and that it leaves
// nothing behind once stopped or after disableTelemetry().
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  campaignProps,
  installPageViews,
  MAX_CAMPAIGN_VALUE_CHARS,
  MAX_ROUTE_CHARS,
  normalizeRoute,
  PAGE_VIEWED_EVENT,
  referrerProps,
} from "./page-views.js";
import {
  disableTelemetry,
  resetTrackState,
  setTelemetrySink,
  type TelemetrySink,
} from "./track.js";

function sink() {
  return {
    track: vi.fn<TelemetrySink["track"]>(),
    setUser: vi.fn<TelemetrySink["setUser"]>(),
    disable: vi.fn<TelemetrySink["disable"]>(),
  };
}

type Listener = () => void;

// A window whose history updates location.pathname the way a browser does.
function stubHistoryWindow(initial = "/") {
  const listeners = new Map<string, Listener[]>();
  const location = { pathname: initial };
  const go = (_state: unknown, _unused: string, url?: string | URL | null) => {
    if (url !== undefined && url !== null) {
      location.pathname = new URL(String(url), "http://app.test").pathname;
    }
  };
  // The real methods, kept apart from window.history, which gets wrapped.
  const history = {
    pushState: vi.fn(go),
    replaceState: vi.fn(go),
  };
  const win = {
    location,
    history: { ...history } as unknown as History,
    addEventListener: (type: string, fn: Listener) =>
      listeners.set(type, [...(listeners.get(type) ?? []), fn]),
    removeEventListener: (type: string, fn: Listener) =>
      listeners.set(
        type,
        (listeners.get(type) ?? []).filter((l) => l !== fn),
      ),
  };
  vi.stubGlobal("window", win);
  const popTo = (path: string) => {
    location.pathname = path;
    for (const fn of listeners.get("popstate") ?? []) fn();
  };
  return { win, history, listeners, popTo };
}

function routes(s: ReturnType<typeof sink>): string[] {
  return s.track.mock.calls
    .filter(([name]) => name === PAGE_VIEWED_EVENT)
    .map(([, attrs]) => attrs.route as string);
}

beforeEach(() => {
  resetTrackState();
});

afterEach(() => {
  resetTrackState();
  vi.unstubAllGlobals();
});

describe("normalizeRoute", () => {
  it("keeps only the pathname", () => {
    expect(normalizeRoute("/orders/view?id=42#items")).toBe("/orders/view");
    expect(normalizeRoute("/search?q=secret%20thing")).toBe("/search");
  });

  it("strips the basePath, and only as a whole segment", () => {
    expect(normalizeRoute("/admin/billing", "/admin")).toBe("/billing");
    expect(normalizeRoute("/admin", "/admin/")).toBe("/");
    expect(normalizeRoute("/administrators", "/admin")).toBe("/administrators");
    // A relative base (Vite's "./") names no prefix.
    expect(normalizeRoute("/billing", "./")).toBe("/billing");
    expect(normalizeRoute("/billing", "/")).toBe("/billing");
  });

  it("replaces id-looking segments with :id and keeps words", () => {
    expect(normalizeRoute("/orders/8812")).toBe("/orders/:id");
    expect(
      normalizeRoute("/u/0b8f3c2e-6a1d-4f7e-9c55-2d1e8a7b4c90/settings"),
    ).toBe("/u/:id/settings");
    expect(
      normalizeRoute("/commits/9fceb02d0ae598e95dc970b74767f19372d61af8"),
    ).toBe("/commits/:id");
    expect(normalizeRoute("/runs/01ARZ3NDEKTSV4RRFFQ69G5FAV")).toBe(
      "/runs/:id",
    );
    expect(normalizeRoute("/share/V1StGXR8Z5jdHi6BmyT2Q9a")).toBe("/share/:id");
    expect(normalizeRoute("/people/jane@example.com")).toBe("/people/:id");
    expect(normalizeRoute("/people/jane%40example.com")).toBe("/people/:id");
    // Words, slugs and short version-ish segments survive.
    expect(normalizeRoute("/blog/how-to-ship-it/v2")).toBe(
      "/blog/how-to-ship-it/v2",
    );
    expect(normalizeRoute("/settings/billing")).toBe("/settings/billing");
  });

  it("sends no segment that is not a plain route word, whatever the id format", () => {
    for (const id of [
      "aB3xY_9k", // short base64url
      "Kx9", // short opaque
      "user42",
      "jane.doe",
      "~jane",
      "JaneDoe",
      "j%C3%A9r%C3%B4me",
      "x-1",
      "abc_def-9",
      "pr_01h2xcejqtf2nbrexx3vqjhp41",
      "a".repeat(33),
    ]) {
      expect(normalizeRoute(`/u/${id}/edit`), id).toBe("/u/:id/edit");
    }
    expect(normalizeRoute("/api/v2/sign-in/order_items")).toBe(
      "/api/v2/sign-in/order_items",
    );
  });

  it("drops trailing and doubled slashes, and caps the length", () => {
    expect(normalizeRoute("/billing/")).toBe("/billing");
    expect(normalizeRoute("//a///b")).toBe("/a/b");
    expect(normalizeRoute("")).toBe("/");
    expect(normalizeRoute("/abc".repeat(100)).length).toBe(MAX_ROUTE_CHARS);
  });
});

describe("installPageViews", () => {
  it("reports the current route at once, then each new one", () => {
    const { history, popTo } = stubHistoryWindow("/");
    const s = sink();
    setTelemetrySink(s);
    installPageViews({ pageViews: true });

    window.history.pushState({}, "", "/orders/12?tab=a");
    window.history.pushState({}, "", "/orders/13");
    window.history.replaceState({}, "", "/orders/13?tab=b#x");
    window.history.pushState({}, "", "/billing");
    popTo("/orders/12");

    expect(routes(s)).toEqual(["/", "/orders/:id", "/billing", "/orders/:id"]);
    // Every call reached the real history methods, arguments untouched.
    expect(history.pushState).toHaveBeenCalledTimes(3);
    expect(history.replaceState).toHaveBeenCalledWith(
      {},
      "",
      "/orders/13?tab=b#x",
    );
  });

  it("strips the basePath, or names routes with routeName", () => {
    stubHistoryWindow("/admin");
    const s = sink();
    setTelemetrySink(s);
    const stop = installPageViews({ pageViews: true, basePath: "/admin" });
    window.history.pushState({}, "", "/admin/users/7");
    stop();
    expect(routes(s)).toEqual(["/", "/users/:id"]);

    const named = sink();
    setTelemetrySink(named);
    installPageViews({
      pageViews: true,
      routeName: (p) => (p.startsWith("/admin/users/") ? "/users/[id]" : null),
    });
    window.history.pushState({}, "", "/admin/users/8?x=1");
    window.history.pushState({}, "", "/admin/hidden");
    expect(routes(named)).toEqual(["/users/[id]"]);
  });

  it("scrubs what routeName returns: no query, fragment or id survives it", () => {
    stubHistoryWindow("/");
    const s = sink();
    setTelemetrySink(s);
    let next = "";
    installPageViews({ pageViews: true, routeName: () => next });
    for (const [named, want] of [
      ["/users/jane.doe?token=abc#frag", "/users/:id"],
      [
        "/orders/:orderId/items/[itemId]/[...rest]",
        "/orders/:orderId/items/[itemId]/[...rest]",
      ],
      ["/files/*/aB3xY_9k", "/files/*/:id"],
      ["users/Kx9", "/users/:id"],
    ] as const) {
      next = named;
      window.history.pushState({}, "", `/p/${Math.random()}`);
      expect(routes(s).at(-1), named).toBe(want);
    }
  });

  it("uses the Navigation API where the browser has one, and leaves history alone", () => {
    const { history } = stubHistoryWindow("/");
    const navListeners: Listener[] = [];
    const navigation = {
      currentEntry: { url: "http://app.test/" },
      addEventListener: vi.fn((_t: string, fn: Listener) =>
        navListeners.push(fn),
      ),
      removeEventListener: vi.fn(),
    };
    (window as unknown as Record<string, unknown>).navigation = navigation;
    const pushState = window.history.pushState;
    const s = sink();
    setTelemetrySink(s);
    const stop = installPageViews({ pageViews: true });

    expect(window.history.pushState).toBe(pushState);
    navigation.currentEntry = { url: "http://app.test/projects/5?tab=x" };
    for (const fn of navListeners) fn();
    expect(routes(s)).toEqual(["/", "/projects/:id"]);
    expect(navigation.addEventListener).toHaveBeenCalledWith(
      "currententrychange",
      expect.any(Function),
    );

    stop();
    expect(navigation.removeEventListener).toHaveBeenCalledWith(
      "currententrychange",
      navListeners[0],
    );
    expect(history.pushState).not.toHaveBeenCalled();
  });

  it("records no events with pageViews off, but still reports routes to onRoute", () => {
    stubHistoryWindow("/auth/sign-in");
    const s = sink();
    setTelemetrySink(s);
    const onRoute = vi.fn();
    installPageViews({ pageViews: false, onRoute });
    window.history.pushState({}, "", "/home");

    expect(s.track).not.toHaveBeenCalled();
    expect(onRoute.mock.calls).toEqual([
      ["/auth/sign-in", null],
      ["/home", "/auth/sign-in"],
    ]);
  });

  it("puts history back on stop, and goes inert when someone wrapped it after", () => {
    const { win, history } = stubHistoryWindow("/");
    const original = win.history.pushState;
    const s = sink();
    setTelemetrySink(s);
    const stop = installPageViews({ pageViews: true });
    expect(win.history.pushState).not.toBe(original);
    stop();
    expect(win.history.pushState).toBe(original);
    expect(win.history.replaceState).toBe(history.replaceState);

    // A router that wraps after us keeps its wrapper; ours stops reporting.
    const stop2 = installPageViews({ pageViews: true });
    const ours = win.history.pushState;
    const theirs: History["pushState"] = function (this: History, ...args) {
      return ours.apply(this, args);
    };
    win.history.pushState = theirs;
    stop2();
    expect(win.history.pushState).toBe(theirs);
    s.track.mockReset();
    window.history.pushState({}, "", "/later");
    expect(s.track).not.toHaveBeenCalled();
  });

  it("reports nothing after disableTelemetry, and installs nothing once disabled", () => {
    const { win } = stubHistoryWindow("/");
    const s = sink();
    setTelemetrySink(s);
    installPageViews({ pageViews: true });
    disableTelemetry();
    s.track.mockReset();
    window.history.pushState({}, "", "/after");
    expect(s.track).not.toHaveBeenCalled();

    const before = win.history.pushState;
    installPageViews({ pageViews: true });
    expect(win.history.pushState).toBe(before);
  });

  it("is a no-op without a DOM", () => {
    expect(() => installPageViews({ pageViews: true })()).not.toThrow();
  });
});

describe("campaignProps", () => {
  it("carries utm_*, ref and the ad network, only when present", () => {
    expect(campaignProps("")).toEqual({});
    expect(
      campaignProps(
        "?utm_source=news&utm_medium=email&utm_campaign=launch&utm_term=t&utm_content=c&ref=ph",
      ),
    ).toEqual({
      utm_source: "news",
      utm_medium: "email",
      utm_campaign: "launch",
      utm_term: "t",
      utm_content: "c",
      ref: "ph",
    });
    expect(campaignProps("?source=newsletter")).toEqual({ ref: "newsletter" });
  });

  it("names the ad network of a click id and never sends the id", () => {
    for (const [param, network] of [
      ["gclid", "google"],
      ["wbraid", "google"],
      ["msclkid", "microsoft"],
      ["fbclid", "meta"],
      ["ttclid", "tiktok"],
      ["li_fat_id", "linkedin"],
      ["twclid", "other"],
    ]) {
      const props = campaignProps(`?${param}=SECRET-CLICK-ID`);
      expect(props).toEqual({ click_id: network });
      expect(JSON.stringify(props)).not.toContain("SECRET");
    }
  });

  it("drops a value carrying an email address, and caps the rest", () => {
    expect(campaignProps("?utm_content=jane%40example.com")).toEqual({});
    const long = "x".repeat(MAX_CAMPAIGN_VALUE_CHARS + 50);
    expect(campaignProps(`?utm_source=${long}`).utm_source).toHaveLength(
      MAX_CAMPAIGN_VALUE_CHARS,
    );
  });
});

describe("referrerProps", () => {
  it("keeps the host and a scrubbed path, never the query", () => {
    expect(
      referrerProps(
        "https://news.ycombinator.com/item/12345?id=1",
        "shop.example.com",
      ),
    ).toEqual({
      referrer_host: "news.ycombinator.com",
      referrer_path: "/item/:id",
    });
  });

  it("records nothing for this site, its www twin or a declared site host", () => {
    expect(
      referrerProps("https://shop.example.com/a", "shop.example.com"),
    ).toEqual({});
    expect(referrerProps("https://www.example.com/", "example.com")).toEqual(
      {},
    );
    expect(
      referrerProps("https://auth.example.com/cb", "shop.example.com", [
        "auth.example.com",
      ]),
    ).toEqual({});
    expect(referrerProps("", "shop.example.com")).toEqual({});
    expect(referrerProps("android-app://x", "shop.example.com")).toEqual({});
  });
});
