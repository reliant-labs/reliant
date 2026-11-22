// The browser half of the standard auth events: what trackAuthEvent and
// identifyUser hand to track() and setTelemetryUser(), the id guard, the
// daily `resumed` dedupe and the GPC/DNT opt-out. Their wiring to the SDK is
// track()'s, pinned in track.test.ts and telemetry.test.ts.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  AUTH_SEEN_STORAGE_KEY,
  authEventRecord,
  identifyUser,
  resetAuthEventState,
  trackAuthEvent,
} from "./auth-events.js";
import {
  disableTelemetry,
  grantAnalyticsConsent,
  optOutOfAnalytics,
  resetTrackState,
  setAnalyticsMode,
  setTelemetrySink,
  type TelemetrySink,
} from "./track.js";

type Op = ["track", string, Record<string, unknown>] | ["user", string];

let ops: Op[];
let store: Map<string, string>;
let nav: { doNotTrack?: string; globalPrivacyControl?: boolean };

function install(): void {
  const sink: TelemetrySink = {
    track: (name, attributes) => ops.push(["track", name, attributes]),
    setUser: (id) => ops.push(["user", id]),
    disable: () => {},
  };
  setTelemetrySink(sink);
}

beforeEach(() => {
  resetTrackState();
  resetAuthEventState();
  ops = [];
  store = new Map();
  nav = {};
  vi.stubGlobal("window", {});
  vi.stubGlobal("navigator", nav);
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
  });
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-11-10T09:00:00Z"));
  install();
});

afterEach(() => {
  vi.useRealTimers();
  resetTrackState();
  resetAuthEventState();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("trackAuthEvent", () => {
  it("attaches the user, then sends the event, so the span carries the id", () => {
    trackAuthEvent("signed_in", {
      userId: "auth0|abc",
      method: "password",
      provider: "auth0",
    });
    expect(ops).toEqual([
      ["user", "auth0|abc"],
      [
        "track",
        "auth_signed_in",
        { kind: "credential", method: "password", provider: "auth0" },
      ],
    ]);
  });

  it("sends signed_up with method, provider and verified", () => {
    trackAuthEvent("signed_up", {
      userId: "user_2x",
      method: "social",
      socialProvider: "Google",
      provider: "clerk",
      verified: false,
    });
    expect(ops).toEqual([
      ["user", "user_2x"],
      [
        "track",
        "auth_signed_up",
        {
          method: "social",
          social_provider: "google",
          provider: "clerk",
          verified: false,
        },
      ],
    ]);
  });

  it("sends signed_out with the id, then clears it", () => {
    trackAuthEvent("signed_out", { userId: "u1", provider: "clerk" });
    expect(ops).toEqual([
      ["user", "u1"],
      ["track", "auth_signed_out", { provider: "clerk" }],
      ["user", ""],
    ]);
  });

  it("bounds the vocabularies", () => {
    const r = authEventRecord("signed_in", {
      userId: "u1",
      method: "carrier_pigeon",
      provider: "Not A Provider",
    });
    expect(r?.attributes).toEqual({
      kind: "credential",
      method: "other",
      provider: "other",
    });
    expect(authEventRecord("exploded" as never, { userId: "u1" })).toBeNull();
  });

  it("refuses an id that is not an opaque subject, warning once", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    trackAuthEvent("signed_in", { userId: "ada@example.com" });
    trackAuthEvent("signed_up", { userId: "" });
    trackAuthEvent("signed_in", { userId: "x".repeat(129) });
    identifyUser("ada@example.com");
    expect(ops).toEqual([]);
    expect(warn).toHaveBeenCalledTimes(1);
    expect(String(warn.mock.calls[0]?.[0])).not.toContain("ada@example.com");
  });

  it("sends nothing and attaches no id under Global Privacy Control", () => {
    // GPC caps the browser at the cookieless aggregate tier: no user id.
    nav.globalPrivacyControl = true;
    trackAuthEvent("signed_in", { userId: "u1" });
    expect(ops).toEqual([["user", ""]]);
  });

  it("sends nothing under Do Not Track", () => {
    nav.doNotTrack = "1";
    trackAuthEvent("signed_out", { userId: "u1" });
    expect(ops).toEqual([["user", ""]]);
  });

  it("is a no-op after disableTelemetry", () => {
    disableTelemetry();
    trackAuthEvent("signed_in", { userId: "u1" });
    identifyUser("u1");
    expect(ops).toEqual([]);
  });
});

describe("identifyUser", () => {
  it("sends one resumed sign-in per user per UTC day", () => {
    identifyUser("u1", { provider: "clerk" });
    identifyUser("u1", { provider: "clerk" });
    expect(ops).toEqual([
      ["user", "u1"],
      ["track", "auth_signed_in", { kind: "resumed", provider: "clerk" }],
      ["user", "u1"],
    ]);
    // Below the full tier nothing is stored: the marker is in memory.
    expect(store.size).toBe(0);

    ops = [];
    vi.setSystemTime(new Date("2026-11-11T00:00:01Z"));
    identifyUser("u1");
    expect(ops).toEqual([
      ["user", "u1"],
      ["track", "auth_signed_in", { kind: "resumed" }],
    ]);
  });

  it("sends again for a different user the same day", () => {
    identifyUser("u1");
    identifyUser("u2");
    expect(ops.filter((o) => o[0] === "track")).toHaveLength(2);
  });

  it("sends no resumed on top of a credential sign-in the same day", () => {
    trackAuthEvent("signed_in", { userId: "u1" });
    identifyUser("u1");
    expect(ops.filter((o) => o[0] === "track")).toEqual([
      ["track", "auth_signed_in", { kind: "credential" }],
    ]);
  });

  it("dedupes in memory when storage is unavailable", () => {
    vi.stubGlobal("localStorage", {
      getItem: () => {
        throw new Error("blocked");
      },
      setItem: () => {
        throw new Error("blocked");
      },
    });
    identifyUser("u1");
    identifyUser("u1");
    expect(ops.filter((o) => o[0] === "track")).toHaveLength(1);
  });

  it("clears the user on null, sending nothing", () => {
    identifyUser(null);
    expect(ops).toEqual([["user", ""]]);
  });

  it("keeps the daily marker in localStorage only in the full tier", () => {
    setAnalyticsMode("full");
    grantAnalyticsConsent();
    identifyUser("u1");
    expect(store.get(AUTH_SEEN_STORAGE_KEY)).toBe("u1:2026-11-10");
    // A new page load in the full tier remembers it.
    resetAuthEventState();
    ops = [];
    identifyUser("u1");
    expect(ops.filter((o) => o[0] === "track")).toEqual([]);
  });

  it("stores the full tier's marker only once the heartbeat reached the sink", () => {
    setAnalyticsMode("full");
    grantAnalyticsConsent();
    // The SDK is still loading: the heartbeat is held, the device remembers nothing.
    setTelemetrySink(null);
    identifyUser("u1");
    identifyUser("u1");
    expect(ops).toEqual([]);
    expect(store.has(AUTH_SEEN_STORAGE_KEY)).toBe(false);

    // The start fails (initBrowserTelemetry clears the sink): the held
    // heartbeat is dropped, and still nothing is stored.
    setTelemetrySink(null);
    expect(store.has(AUTH_SEEN_STORAGE_KEY)).toBe(false);

    // The next load retries: the heartbeat is sent again, and once the SDK
    // takes it, the day is remembered.
    resetAuthEventState();
    identifyUser("u1");
    expect(store.has(AUTH_SEEN_STORAGE_KEY)).toBe(false);
    install();
    expect(ops).toEqual([
      ["user", "u1"],
      ["track", "auth_signed_in", { kind: "resumed" }],
    ]);
    expect(store.get(AUTH_SEEN_STORAGE_KEY)).toBe("u1:2026-11-10");
  });

  it("stores a credential sign-in's marker only once it reached the sink", () => {
    setAnalyticsMode("full");
    grantAnalyticsConsent();
    setTelemetrySink(null);
    trackAuthEvent("signed_in", { userId: "u1" });
    // The same load sends no resumed on top of it, delivered or not.
    identifyUser("u1");
    expect(store.has(AUTH_SEEN_STORAGE_KEY)).toBe(false);
    install();
    expect(ops.filter((o) => o[0] === "track")).toEqual([
      ["track", "auth_signed_in", { kind: "credential" }],
    ]);
    expect(store.get(AUTH_SEEN_STORAGE_KEY)).toBe("u1:2026-11-10");
  });

  it("keeps each held heartbeat's own user when the user changes during startup", () => {
    setAnalyticsMode("full");
    grantAnalyticsConsent();
    setTelemetrySink(null);
    identifyUser("u1");
    identifyUser("u2");
    install();
    expect(ops).toEqual([
      ["user", "u1"],
      ["track", "auth_signed_in", { kind: "resumed" }],
      ["user", "u2"],
      ["track", "auth_signed_in", { kind: "resumed" }],
    ]);
    // Each marker followed its own delivery; the last one is u2's.
    expect(store.get(AUTH_SEEN_STORAGE_KEY)).toBe("u2:2026-11-10");
  });

  it("sends a held sign-out under its user, then clears it", () => {
    setTelemetrySink(null);
    identifyUser("u1");
    trackAuthEvent("signed_out", { userId: "u1" });
    install();
    expect(ops).toEqual([
      ["user", "u1"],
      ["track", "auth_signed_in", { kind: "resumed" }],
      ["track", "auth_signed_out", {}],
      ["user", ""],
    ]);
  });

  it("sends today's heartbeat once while yesterday's marker is still stored", () => {
    setAnalyticsMode("full");
    grantAnalyticsConsent();
    store.set(AUTH_SEEN_STORAGE_KEY, "u1:2026-11-09");
    setTelemetrySink(null);
    identifyUser("u1");
    identifyUser("u1");
    install();
    expect(ops.filter((o) => o[0] === "track")).toHaveLength(1);
    expect(store.get(AUTH_SEEN_STORAGE_KEY)).toBe("u1:2026-11-10");
  });

  it("attaches no id and sends no heartbeat under GPC", () => {
    nav.globalPrivacyControl = true;
    identifyUser("u1");
    expect(ops).toEqual([["user", ""]]);
    expect(store.has(AUTH_SEEN_STORAGE_KEY)).toBe(false);
  });

  it("sends nothing and attaches no id after optOutOfAnalytics", () => {
    optOutOfAnalytics();
    ops = [];
    identifyUser("u1");
    trackAuthEvent("signed_up", { userId: "u1" });
    expect(ops.filter((o) => o[0] === "track")).toEqual([]);
    expect(ops.filter((o) => o[0] === "user" && o[1] !== "")).toEqual([]);
  });

  it("is a no-op on the server", () => {
    vi.stubGlobal("window", undefined);
    identifyUser("u1");
    trackAuthEvent("signed_in", { userId: "u1" });
    expect(ops).toEqual([]);
  });
});
