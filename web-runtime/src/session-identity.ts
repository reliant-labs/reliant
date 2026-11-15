// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The signed-in user, found without the app's help: initBrowserTelemetry's
// `identify` option. Every browser event then carries the user's id, the same
// id the backend's product events carry, so browser and server events join on
// one user.
//
// ── Why the runtime asks the server ──────────────────────────────────────
//
// A forge frontend signs in natively: the session is an HttpOnly cookie no
// script can read, so the bundle holds no token to decode. The one thing that
// knows who is signed in is the API's `GET /auth/session` — the cheap call
// the app's own auth context already makes at load. This module makes it too,
// with the cookie (`credentials: "include"`, for an API on another origin),
// and hands the answer's `subject` to setTelemetryUser. Only `subject` is
// read: the IdP's opaque user id. `email` and `name` in the same answer never
// leave this function, and a subject that looks like an email (contains `@`)
// or is over 128 characters names nobody. Whether the id is then carried is
// the analytics tier's call (track.ts): not under GPC / DNT or in `off`.
//
// ── When it asks ─────────────────────────────────────────────────────────
//
// Once at startup, then only when the answer can have changed: on a route
// change into or out of `/auth/*` (signing in leaves the sign-in screen;
// signing out lands on it), and when the tab becomes visible again (a sign-out
// in another tab). Never once per navigation.
//
// ── What each answer does ────────────────────────────────────────────────
//
//   names a user        `{authenticated: true, subject}` sets that subject
//                       (an unchanged one writes nothing).
//   signed out          `{authenticated: false}`, or a 401 / 403, clears the
//                       user this watch set.
//   names nobody        a 404, any other non-2xx, a body that is not the
//                       session shape, or a network failure ALSO clears the
//                       user this watch set: an event with no user can be
//                       joined later, one tied to the wrong account cannot.
//
// Until this watch has set a user, neither of the last two changes anything:
// a project without native sign-in, or an app whose own IdP calls
// setTelemetryUser, is left alone. A clear happens once; a repeat finds
// nothing set and writes nothing. A later answer naming a user sets it again.
import { isTelemetryDisabled, setTelemetryUser } from "./track.js";

/** initBrowserTelemetry's `identify` option. */
export interface IdentifyOptions {
  /**
   * The session endpoint: the API's `/auth/session`, absolute or relative to
   * this page. Answers `{ authenticated, subject }`.
   */
  sessionUrl: string;
}

/** The route prefix whose screens change who is signed in. */
const AUTH_ROUTE = "/auth";

/** Whether a route is one of the sign-in / sign-out screens. */
export function isAuthRoute(route: string | null): boolean {
  return (
    route !== null &&
    (route === AUTH_ROUTE || route.startsWith(`${AUTH_ROUTE}/`))
  );
}

/** What the session watch hands telemetry.ts. */
export interface SessionIdentity {
  /** Ask the server again now. */
  refresh(): void;
  /**
   * Resolves once every answer asked for so far has been applied (or
   * ignored). For tests and for code that must read the user after a check.
   */
  settled(): Promise<void>;
  /** Called with each new route: refreshes on a move into or out of /auth/*. */
  onRoute(route: string, previous: string | null): void;
  /** Remove the visibility listener; later answers are ignored. */
  stop(): void;
}

/**
 * Start resolving the signed-in user from `options.sessionUrl` and keeping
 * setTelemetryUser in step with it. Asks once at once. Called by
 * initBrowserTelemetry; an app has no reason to call it.
 */
export function watchSessionIdentity(
  options: IdentifyOptions,
): SessionIdentity {
  let active = true;
  // The subject this watch last set, so an unchanged answer writes nothing
  // and a signed-out answer clears only what this watch set.
  let current: string | null = null;
  // Answers can arrive out of order; only the newest request's counts.
  let seq = 0;
  // Every request asked so far, applied or ignored.
  let inFlight: Promise<void> = Promise.resolve();

  const refresh = () => {
    if (!active || isTelemetryDisabled() || typeof fetch !== "function") {
      return;
    }
    const mine = ++seq;
    const applied = ask(options.sessionUrl).then((answer) => {
      if (!active || mine !== seq || isTelemetryDisabled()) {
        return;
      }
      // Signed out ("") and names nobody (null) both mean "no user we can
      // vouch for": clear what this watch set, and nothing else.
      const next = answer === null || answer === "" ? null : answer;
      if (next === current) {
        return;
      }
      current = next;
      setTelemetryUser(current);
    });
    inFlight = Promise.all([inFlight, applied]).then(() => undefined);
  };

  const onVisible = () => {
    if (document.visibilityState === "visible") {
      refresh();
    }
  };
  const doc = typeof document !== "undefined" ? document : undefined;
  if (doc && typeof doc.addEventListener === "function") {
    doc.addEventListener("visibilitychange", onVisible);
  }

  refresh();

  return {
    refresh,
    settled: () => inFlight,
    onRoute: (route, previous) => {
      if (previous !== null && (isAuthRoute(route) || isAuthRoute(previous))) {
        refresh();
      }
    },
    stop: () => {
      active = false;
      doc?.removeEventListener?.("visibilitychange", onVisible);
    },
  };
}

/**
 * The session's subject, "" when signed out (`authenticated: false`, a 401 or
 * a 403), or null when the answer names nobody (a network failure, a 404 or
 * other non-2xx, a body of another shape). Never throws.
 */
async function ask(url: string): Promise<string | null> {
  try {
    const response = await fetch(url, {
      credentials: "include",
      headers: { Accept: "application/json" },
    });
    if (response.status === 401 || response.status === 403) {
      return "";
    }
    if (!response.ok) {
      return null;
    }
    const body = (await response.json()) as unknown;
    if (typeof body !== "object" || body === null) {
      return null;
    }
    const { authenticated, subject } = body as {
      authenticated?: unknown;
      subject?: unknown;
    };
    if (authenticated === false) {
      return "";
    }
    if (authenticated === true && typeof subject === "string") {
      // An opaque subject only: never an email (the product-event contract's
      // id guard, as identifyUser applies it).
      const trimmed = subject.trim();
      return trimmed === "" || trimmed.includes("@") || trimmed.length > 128
        ? null
        : trimmed;
    }
    return null;
  } catch {
    return null;
  }
}
