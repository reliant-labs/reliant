// The auth-provider adapters: each provider's callbacks, mapped onto what
// identifyUser / trackAuthEvent hand to the telemetry sink. The events' own
// rules (dedupe, GPC/DNT, tiers) are auth-events.test.ts's.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { resetAuthEventState } from "../auth-events.js";
import {
  resetTrackState,
  setTelemetrySink,
  type TelemetrySink,
} from "../track.js";
import { reportAuth0SignOut, syncAuth0 } from "./auth0.js";
import { createClerkAuthTelemetry, type ClerkResourcesLike } from "./clerk.js";
import {
  createFirebaseAuthTelemetry,
  firebaseMethod,
  reportFirebaseSignIn,
  reportFirebaseSignOut,
  type FirebaseUserLike,
} from "./firebase.js";
import {
  createSupabaseAuthTelemetry,
  supabaseAuthTelemetryListener,
  type SupabaseSessionLike,
} from "./supabase.js";

type Op = [string, ...unknown[]];
let ops: Op[];

beforeEach(() => {
  resetTrackState();
  resetAuthEventState();
  ops = [];
  vi.stubGlobal("window", {});
  vi.stubGlobal("navigator", {});
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-11-11T09:00:00Z"));
  const sink: TelemetrySink = {
    track: (name, attributes) => ops.push(["track", name, attributes]),
    setUser: (id) => ops.push(["user", id]),
    disable: () => {},
  };
  setTelemetrySink(sink);
});

afterEach(() => {
  vi.useRealTimers();
  resetTrackState();
  resetAuthEventState();
  vi.unstubAllGlobals();
});

// What the sink receives when the user is cleared.
const CLEARED: Op = ["user", ""];
const tracked = () => ops.filter((o) => o[0] === "track");
const session = (id: string, anonymous = false): SupabaseSessionLike => ({
  user: { id, is_anonymous: anonymous },
});

describe("supabase", () => {
  it("identifies a signed-in user once a day, and signs them out", () => {
    const on = supabaseAuthTelemetryListener();
    on("INITIAL_SESSION", session("uuid-1"));
    on("TOKEN_REFRESHED", session("uuid-1"));
    on("SIGNED_IN", session("uuid-1")); // a tab refocus, not a sign-in
    on("SIGNED_OUT", null);
    expect(tracked()).toEqual([
      ["track", "auth_signed_in", { kind: "resumed", provider: "supabase" }],
      ["track", "auth_signed_out", { provider: "supabase" }],
    ]);
    expect(ops.at(-1)).toEqual(CLEARED);
  });

  it("never counts an anonymous session as a signed-in user", () => {
    const on = supabaseAuthTelemetryListener();
    on("INITIAL_SESSION", session("anon-1", true));
    on("TOKEN_REFRESHED", session("anon-1", true));
    on("SIGNED_OUT", null); // an anonymous visitor leaving signs nobody out
    expect(tracked()).toEqual([]);
    expect(ops.filter((o) => o[0] === "user" && o[1])).toEqual([]);

    // The same id is identified once the account is upgraded.
    on("USER_UPDATED", session("anon-1", false));
    expect(tracked()).toEqual([
      ["track", "auth_signed_in", { kind: "resumed", provider: "supabase" }],
    ]);
  });

  it("identifies anonymous sessions only when asked to", () => {
    const on = supabaseAuthTelemetryListener({ anonymous: "identify" });
    on("INITIAL_SESSION", session("anon-2", true));
    expect(ops).toContainEqual(["user", "anon-2"]);
  });

  it("clears a real user replaced by an anonymous session without signing them out", () => {
    const on = supabaseAuthTelemetryListener();
    on("SIGNED_IN", session("uuid-3"));
    on("SIGNED_IN", session("anon-3", true));
    expect(tracked().map((o) => o[1])).toEqual(["auth_signed_in"]);
    expect(ops.at(-1)).toEqual(CLEARED);
  });

  it("subscribes through onAuthStateChange and unsubscribes", () => {
    let cb: ((e: string, s: SupabaseSessionLike | null) => void) | undefined;
    const unsubscribe = vi.fn();
    const stop = createSupabaseAuthTelemetry({
      auth: {
        onAuthStateChange(callback) {
          cb = callback;
          return { data: { subscription: { unsubscribe } } };
        },
      },
    });
    cb?.("INITIAL_SESSION", session("uuid-4"));
    expect(ops).toContainEqual(["user", "uuid-4"]);
    stop();
    expect(unsubscribe).toHaveBeenCalledOnce();
  });

  it("never throws from a broken client", () => {
    const stop = createSupabaseAuthTelemetry({
      auth: {
        onAuthStateChange() {
          throw new Error("boom");
        },
      },
    });
    expect(() => stop()).not.toThrow();
  });
});

describe("clerk", () => {
  it("waits for Clerk to load, identifies the user and signs them out", () => {
    let cb: ((r: ClerkResourcesLike) => void) | undefined;
    createClerkAuthTelemetry({
      addListener(callback) {
        cb = callback;
        return () => {};
      },
    });
    cb?.({}); // not loaded
    cb?.({ user: { id: "user_1" } });
    cb?.({ user: { id: "user_1" } });
    cb?.({ user: null });
    expect(tracked()).toEqual([
      ["track", "auth_signed_in", { kind: "resumed", provider: "clerk" }],
      ["track", "auth_signed_out", { provider: "clerk" }],
    ]);
  });
});

describe("auth0", () => {
  const client = (sub?: string) => ({
    isAuthenticated: async () => sub !== undefined,
    getUser: async () => (sub ? { sub } : undefined),
  });

  it("identifies the signed-in user, or clears the id", async () => {
    await syncAuth0(client("auth0|abc"));
    await syncAuth0(client());
    expect(tracked()).toEqual([
      ["track", "auth_signed_in", { kind: "resumed", provider: "auth0" }],
    ]);
    expect(ops.at(-1)).toEqual(CLEARED);
  });

  it("reports the sign-out before logout navigates away", async () => {
    await reportAuth0SignOut(client("auth0|abc"));
    expect(tracked()).toEqual([
      ["track", "auth_signed_out", { provider: "auth0" }],
    ]);
  });

  it("never rejects", async () => {
    const broken = {
      isAuthenticated: async () => {
        throw new Error("boom");
      },
      getUser: async () => {
        throw new Error("boom");
      },
    };
    await expect(syncAuth0(broken)).resolves.toBeUndefined();
    await expect(reportAuth0SignOut(broken)).resolves.toBeUndefined();
  });
});

describe("firebase", () => {
  it("identifies real users only, and reports exact sign-ups", () => {
    let cb: ((u: FirebaseUserLike | null) => void) | undefined;
    createFirebaseAuthTelemetry((callback) => {
      cb = callback;
      return () => {};
    });
    cb?.({ uid: "anon", isAnonymous: true });
    expect(tracked()).toEqual([]);

    reportFirebaseSignIn(
      { uid: "anon", isAnonymous: true },
      { isNewUser: true },
    );
    expect(tracked()).toEqual([]);

    reportFirebaseSignIn(
      { uid: "fb-1" },
      { isNewUser: true, providerId: "google.com" },
    );
    cb?.({ uid: "fb-1" });
    reportFirebaseSignOut();
    expect(tracked()).toEqual([
      [
        "track",
        "auth_signed_up",
        { method: "social", social_provider: "google", provider: "firebase" },
      ],
      ["track", "auth_signed_in", { kind: "resumed", provider: "firebase" }],
      ["track", "auth_signed_out", { provider: "firebase" }],
    ]);
  });

  it("maps provider ids onto the method vocabulary", () => {
    expect(firebaseMethod("password")).toEqual({ method: "password" });
    expect(firebaseMethod("emailLink")).toEqual({ method: "magic_link" });
    expect(firebaseMethod("saml.okta")).toEqual({ method: "sso" });
    expect(firebaseMethod("facebook.com")).toEqual({
      method: "social",
      socialProvider: "other",
    });
    expect(firebaseMethod(null)).toEqual({});
  });
});
