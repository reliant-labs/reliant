// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The standard auth events: who signed up, signed in and signed out, whatever
// the auth provider. They are ordinary product events (track()), named
// `auth_signed_up`, `auth_signed_in` and `auth_signed_out`, so they land in the
// same product_events table as forge/pkg/observe.TrackAuth's, and the console
// counts users from them:
//
//   - active over a range = distinct users with auth_signed_in in it;
//   - new = distinct users with auth_signed_up in it;
//   - returning = active users with no auth_signed_up in it.
//
// Every count is a distinct count of the user id, so the same sign-in reported
// by the server AND the browser counts once.
//
// ── Where the truth is ───────────────────────────────────────────────────
//
// A server that sees the auth is the authoritative emitter: forge's managed
// auth reports sign-ups and sign-ins from the login broker, and forge's auth
// middleware reports one `resumed` sign-in per user per day for any JWT
// provider. Browser events are lost to blockers, consent and early
// navigation, and anyone can post one. So these calls are for joining
// sessions to users (identifyUser) and for apps whose auth never touches a
// forge server (a static site on Firebase Auth, an SPA on Clerk).
//
// ── Privacy ──────────────────────────────────────────────────────────────
//
// The id is the provider's opaque subject (`auth0|abc`, `user_…`), never an
// email: an id that is empty, longer than MAX_AUTH_USER_ID_CHARS or contains
// '@' is refused, with one console warning per page load. The events honour
// the browser's Global Privacy Control and Do Not Track (browserPrivacyOptOut):
// with either set, no auth event is sent and the user id is not attached
// either (setTelemetryUser carries no id under GPC / DNT: the browser is
// capped at the cookieless `aggregate` analytics tier). They inherit track()'s
// gates: nothing before initBrowserTelemetry starts, nothing after
// disableTelemetry() or optOutOfAnalytics(). Nothing is stored on the device
// below the `full` tier (the daily `resumed` marker is kept in memory).
//
// The vocabulary (event names, prop keys, bounded values) is declared once
// below and mirrored once in forge/pkg/observe/auth_events.go; the Node entry
// reuses authEventRecord, so a revision touches those two files only.
//
// Dependency-free apart from track.ts, like attribution.ts.
import {
  analyticsTier,
  browserPrivacyOptOut,
  isTelemetryDisabled,
  recordProductEvent as track,
  setTelemetryUser,
  type TrackValue,
} from "./track.js";

/** The three standard auth events, without their `auth_` prefix. */
export type AuthEventKind = "signed_up" | "signed_in" | "signed_out";

/** How the person proved who they are. Anything else is recorded as `other`. */
export type AuthMethod =
  | "password"
  | "passkey"
  | "magic_link"
  | "social"
  | "invite"
  | "verify"
  | "reset"
  | "sso"
  | "other";

export interface AuthEventProps {
  /** The provider's opaque subject. Never an email. */
  userId: string;
  /** How the person authenticated (signed_up, signed_in). */
  method?: AuthMethod | (string & {});
  /**
   * The auth provider: "auth0", "clerk", "supabase", "firebase", "cognito",
   * "custom", or a name of your own matching ^[a-z][a-z0-9_]{0,31}$ (anything
   * else is recorded as "other").
   */
  provider?: string;
  /** With method "social": google, github, microsoft, apple (else "other"). */
  socialProvider?: string;
  /** Whether the new account's email is verified (signed_up only). */
  verified?: boolean;
}

/** Options for identifyUser. */
export interface IdentifyUserOptions {
  /** The auth provider, as AuthEventProps.provider. */
  provider?: string;
}

/** Longest user id an auth event accepts. */
export const MAX_AUTH_USER_ID_CHARS = 128;
/**
 * localStorage key of the last daily `resumed` sign-in sent from this
 * browser, as `<userId>:<YYYY-MM-DD>` (UTC). Kept there only in the `full`
 * analytics tier; below it nothing is stored, so the marker lives for the
 * page load and a reload may send one more `resumed` that day (every count
 * is distinct by user, so it counts once).
 */
export const AUTH_SEEN_STORAGE_KEY = "forge.auth.seen";

/** The product event name of each auth event. */
export const AUTH_EVENT_NAMES: Readonly<Record<AuthEventKind, string>> = {
  signed_up: "auth_signed_up",
  signed_in: "auth_signed_in",
  signed_out: "auth_signed_out",
};

const METHODS: ReadonlySet<string> = new Set([
  "password",
  "passkey",
  "magic_link",
  "social",
  "invite",
  "verify",
  "reset",
  "sso",
  "other",
]);
const SOCIAL_PROVIDERS: ReadonlySet<string> = new Set([
  "google",
  "github",
  "microsoft",
  "apple",
  "other",
]);
const PROVIDER_RE = /^[a-z][a-z0-9_]{0,31}$/;

let warned = false;
let memorySeen: string | undefined;

/** Whether `id` can be an auth event's user id: an opaque subject. */
export function validAuthUserId(id: unknown): id is string {
  return (
    typeof id === "string" &&
    id !== "" &&
    id.length <= MAX_AUTH_USER_ID_CHARS &&
    !id.includes("@")
  );
}

/**
 * The event name and props for one auth event, bounded to the vocabulary, or
 * null when it must not be sent (an unknown kind, an id that is not an opaque
 * subject). Pure apart from the one-time warning; shared with the Node entry.
 */
export function authEventRecord(
  kind: AuthEventKind,
  props: AuthEventProps,
  signInKind: "credential" | "resumed" = "credential",
): {
  name: string;
  userId: string;
  attributes: Record<string, TrackValue>;
} | null {
  if (kind !== "signed_up" && kind !== "signed_in" && kind !== "signed_out") {
    return null;
  }
  const userId = props?.userId;
  if (!validAuthUserId(userId)) {
    if (!warned) {
      warned = true;
      try {
        console.warn(
          "[forge] auth event dropped: userId must be the auth provider's opaque subject (not empty, no '@', at most 128 characters)",
        );
      } catch {
        // Telemetry must never break the app.
      }
    }
    return null;
  }
  const attributes: Record<string, TrackValue> = {};
  if (kind === "signed_in") {
    attributes["kind"] = signInKind;
  }
  if (kind !== "signed_out" && typeof props.method === "string") {
    const method = METHODS.has(props.method) ? props.method : "other";
    attributes["method"] = method;
    if (method === "social" && typeof props.socialProvider === "string") {
      const sp = props.socialProvider.trim().toLowerCase();
      attributes["social_provider"] = SOCIAL_PROVIDERS.has(sp) ? sp : "other";
    }
  }
  if (typeof props.provider === "string" && props.provider !== "") {
    attributes["provider"] = PROVIDER_RE.test(props.provider)
      ? props.provider
      : "other";
  }
  if (kind === "signed_up" && typeof props.verified === "boolean") {
    attributes["verified"] = props.verified;
  }
  return { name: AUTH_EVENT_NAMES[kind], userId, attributes };
}

/**
 * Record a standard auth event from the browser, for an app whose auth never
 * touches a forge server (forge's managed auth already reports these from the
 * server). Call it where your auth SDK reports the outcome:
 *
 *   trackAuthEvent("signed_in", { userId: user.sub, method: "social", provider: "auth0" });
 *
 * `signed_up` and `signed_in` attach the user to telemetry first
 * (setTelemetryUser), so the event carries the id; `signed_out` sends the
 * event, then clears it. Never throws; inherits track()'s queue and consent
 * gate, and sends nothing under GPC or DNT.
 */
export function trackAuthEvent(
  kind: AuthEventKind,
  props: AuthEventProps,
): void {
  if (typeof window === "undefined" || isTelemetryDisabled()) {
    return;
  }
  try {
    const record = authEventRecord(kind, props);
    if (!record) {
      return;
    }
    const optOut = browserPrivacyOptOut();
    if (kind === "signed_out") {
      if (!optOut) {
        setTelemetryUser(record.userId);
        track(record.name, record.attributes);
      }
      setTelemetryUser(null);
      return;
    }
    setTelemetryUser(record.userId);
    if (optOut) {
      return;
    }
    if (kind === "signed_in") {
      // A credential sign-in is today's sign-in: no `resumed` on top of it.
      // Stored on the device only once the event reached the sink.
      const seen = seenValue(record.userId);
      memorySeen = seen;
      track(record.name, record.attributes, () => persistSeen(seen));
    } else {
      track(record.name, record.attributes);
    }
  } catch {
    // Telemetry must never break the app.
  }
}

/**
 * Tell telemetry who is signed in: call it wherever the app learns that
 * (Clerk's useUser, Auth0's useAuth0, a session refresh), with null on sign
 * out. It attaches the id to every span and replay from here on
 * (setTelemetryUser), and sends one `auth_signed_in {kind: "resumed"}` per
 * user per UTC day per browser, which is what counts a user with a
 * long-lived session as active. Never throws; under GPC or DNT it sends
 * nothing and attaches no id.
 */
export function identifyUser(
  id: string | null | undefined,
  options?: IdentifyUserOptions,
): void {
  if (typeof window === "undefined" || isTelemetryDisabled()) {
    return;
  }
  try {
    if (id === null || id === undefined || id === "") {
      setTelemetryUser(null);
      return;
    }
    const record = authEventRecord(
      "signed_in",
      { userId: id, provider: options?.provider },
      "resumed",
    );
    if (!record) {
      return;
    }
    setTelemetryUser(record.userId);
    if (browserPrivacyOptOut()) {
      return;
    }
    const seen = seenValue(record.userId);
    // This page load's own marker first: the device's may still hold an
    // earlier day's while today's event waits for the SDK.
    if (memorySeen === seen || readSeen() === seen) {
      return;
    }
    // This page load sends it once. The device remembers it (full tier) only
    // after the event reached the sink: a heartbeat lost to an SDK that never
    // loaded, or to a navigation first, is sent again on the next load.
    memorySeen = seen;
    track(record.name, record.attributes, () => persistSeen(seen));
  } catch {
    // Telemetry must never break the app.
  }
}

function seenValue(userId: string): string {
  return `${userId}:${new Date().toISOString().slice(0, 10)}`;
}

// The device store, in the full tier only (track.ts, "Analytics tiers").
function storage(): Storage | null {
  try {
    if (analyticsTier() !== "full") {
      return null;
    }
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null;
  }
}

function readSeen(): string | undefined {
  try {
    return storage()?.getItem(AUTH_SEEN_STORAGE_KEY) ?? memorySeen;
  } catch {
    return memorySeen;
  }
}

function persistSeen(value: string): void {
  try {
    storage()?.setItem(AUTH_SEEN_STORAGE_KEY, value);
  } catch {
    // Not persisted: this page load still remembers it.
  }
}

/** Test seam. */
export function resetAuthEventState(): void {
  warned = false;
  memorySeen = undefined;
}
