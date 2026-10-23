// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Product events, the signed-in user and the consent kill-switch: the part of
// browser telemetry an app calls from ordinary components.
//
// Dependency-free on purpose, for the same reason as error-reporter.ts. This
// module is exported from the barrel, which every frontend imports —
// including ones that never install the HyperDX SDK — so it cannot import the
// SDK. The `/telemetry` subpath, which does, installs the real sink here once
// the SDK has initialised. Until then (the SDK chunk is still loading) calls
// are held, bounded, and replayed in order; with telemetry off they are
// dropped. That is what makes `track()` safe to call unconditionally from a
// click handler or an effect.
//
// ── What `track` sends ───────────────────────────────────────────────────
//
// `track(name, props)` becomes one span from the SDK's `custom-action` tracer
// (HyperDX.addAction), so a product event inherits the session id, page,
// trace context and release the SDK stamps on every span — which is what lets
// a funnel step link to the replay and the errors of the same session.
//
// Props are user data in flight, so they are bounded and scrubbed HERE, before
// the SDK sees them, and not left to a server-side filter:
//   - at most MAX_PROP_KEYS keys; a key must match PROP_KEY_RE or it is dropped
//   - values are string | number | boolean; anything else is dropped
//   - string values have credential shapes redacted, then are cut to
//     MAX_PROP_VALUE_CHARS
//
// ── Consent ──────────────────────────────────────────────────────────────
//
// `disableTelemetry()` is the runtime half of "the user turned it off": it
// stops the replay recorder and makes track, setTelemetryUser and
// reportException no-ops for the rest of the page load. It cannot recall what
// the SDK already batched, and the SDK's own auto-instrumentation (page
// loads, fetch/XHR, uncaught errors) has no off switch once running — an app
// that must send nothing without consent must not call initBrowserTelemetry
// until it has it. There is no re-enable; init again on the next page load.

/** Most events held while the SDK is still loading. Later calls are dropped. */
export const MAX_QUEUED_EVENTS = 100;
/** Most props kept on one event; the rest are dropped in insertion order. */
export const MAX_PROP_KEYS = 20;
/** Longest string prop value, after redaction. */
export const MAX_PROP_VALUE_CHARS = 256;
/** Longest event name. */
export const MAX_EVENT_NAME_CHARS = 128;
/** Longest user id. */
export const MAX_USER_ID_CHARS = 128;
/** A prop key is a dotted lowercase identifier. */
export const PROP_KEY_RE = /^[a-z0-9_.]{1,64}$/;
/** What a redacted credential becomes. */
export const REDACTED = "[redacted]";

/** A prop value an event may carry. Anything else is dropped. */
export type TrackValue = string | number | boolean;
/** Props accepted by track(); non-conforming entries are dropped, not thrown on. */
export type TrackProps = Record<string, unknown>;

/** What the `/telemetry` subpath installs once the SDK is running. */
export interface TelemetrySink {
  track(name: string, attributes: Record<string, TrackValue>): void;
  setUser(id: string): void;
  disable(): void;
}

type Pending =
  | { kind: "track"; name: string; attributes: Record<string, TrackValue> }
  | { kind: "user"; id: string };

let sink: TelemetrySink | null = null;
let disabled = false;
let pending: Pending[] = [];

// Credential shapes, the same set the reliant web app scrubs from error text
// (and the backend scrubs server-side). Kept here so a product event cannot
// carry what an error message may not.
const SECRET_PATTERNS: ReadonlyArray<readonly [RegExp, string]> = [
  // URL userinfo: postgres://user:password@host
  [/([A-Za-z][A-Za-z0-9+.-]*:\/\/)[^\s/@:]+:[^\s/@]+@/g, `$1${REDACTED}@`],
  // key=value / "key": "value" for credential-named keys.
  [
    /\b(authorization|x-api-key|api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|client[_-]?secret|secret|password|passwd|token)(["']?\s*[:=]\s*["']?)(?:(?:bearer|basic|token)\s+)?[^\s"'&,;]+/gi,
    `$1$2${REDACTED}`,
  ],
  // A bare authorization scheme outside a key=value pair.
  [/\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]+/gi, `$1 ${REDACTED}`],
  // JWTs.
  [/\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]*/g, REDACTED],
  // Provider and platform key prefixes.
  [
    /\b(?:sk-ant-[A-Za-z0-9_-]{8,}|sk-[A-Za-z0-9_-]{16,}|sk_(?:live|test)_[A-Za-z0-9]{8,}|(?:rlat|dpat|gho|ghp|ghu|ghs|ghr|github_pat|glpat|xox[abprs])[_-][A-Za-z0-9_-]{10,}|AIza[0-9A-Za-z_-]{20,}|ya29\.[0-9A-Za-z_-]+|AKIA[0-9A-Z]{16})/g,
    REDACTED,
  ],
  // Long opaque runs (digests, base64 blobs). '-', '/' and '.' are excluded so
  // UUIDs, paths and dotted names survive.
  [/[A-Za-z0-9+=_]{40,}/g, REDACTED],
];

/** Redact credential shapes from a string. Exported for tests. */
export function redactSecrets(value: string): string {
  let out = value;
  for (const [pattern, replacement] of SECRET_PATTERNS) {
    out = out.replace(pattern, replacement);
  }
  return out;
}

/**
 * Reduce caller props to what an event may carry: bounded, typed, redacted.
 * Pure; exported for tests.
 */
export function sanitizeProps(
  props: TrackProps | undefined,
): Record<string, TrackValue> {
  const out: Record<string, TrackValue> = {};
  if (typeof props !== "object" || props === null) {
    return out;
  }
  let kept = 0;
  for (const key of Object.keys(props)) {
    if (kept >= MAX_PROP_KEYS) {
      break;
    }
    if (!PROP_KEY_RE.test(key)) {
      continue;
    }
    const value = props[key];
    if (typeof value === "string") {
      // Cut first so redaction never scans an unbounded string, with headroom
      // so a secret straddling the final cut is still seen whole.
      out[key] = redactSecrets(value.slice(0, MAX_PROP_VALUE_CHARS * 4)).slice(
        0,
        MAX_PROP_VALUE_CHARS,
      );
    } else if (typeof value === "number" && Number.isFinite(value)) {
      out[key] = value;
    } else if (typeof value === "boolean") {
      out[key] = value;
    } else {
      continue;
    }
    kept++;
  }
  return out;
}

function enqueue(op: Pending): void {
  if (pending.length < MAX_QUEUED_EVENTS) {
    pending.push(op);
  }
}

/**
 * Record a product event — a funnel step, a feature used — as a span on the
 * current session. Never throws, and is a no-op on the server, after
 * disableTelemetry(), and (once the SDK has started) whenever telemetry is
 * off. Calls made while the SDK is still loading are held, up to
 * MAX_QUEUED_EVENTS, and replayed in order.
 *
 * `name` is a stable, low-cardinality event name ("onboarding_completed"),
 * not a sentence. `props` is bounded and redacted as described in the module
 * header; do not put user content in it.
 */
export function track(name: string, props?: TrackProps): void {
  if (typeof window === "undefined" || disabled) {
    return;
  }
  if (typeof name !== "string") {
    return;
  }
  const trimmed = name.trim().slice(0, MAX_EVENT_NAME_CHARS);
  if (trimmed === "") {
    return;
  }
  const attributes = sanitizeProps(props);
  try {
    if (sink) {
      sink.track(trimmed, attributes);
    } else {
      enqueue({ kind: "track", name: trimmed, attributes });
    }
  } catch {
    // Telemetry must never break the app.
  }
}

/**
 * Attach the signed-in user's id to every span and replay from here on, so an
 * error, a funnel step and a replay can be joined to a user. Pass an opaque
 * id — never an email, a name or anything else a person would recognise. Pass
 * null (sign-out) to clear it.
 *
 * The id is carried as the `userId` global attribute. The SDK has no way to
 * delete an attribute, so clearing writes an empty `userId`. Held until the
 * SDK has started; the last call wins.
 */
export function setTelemetryUser(id: string | null | undefined): void {
  if (typeof window === "undefined" || disabled) {
    return;
  }
  const value =
    typeof id === "string" ? id.trim().slice(0, MAX_USER_ID_CHARS) : "";
  try {
    if (sink) {
      sink.setUser(value);
      return;
    }
    pending = pending.filter((op) => op.kind !== "user");
    enqueue({ kind: "user", id: value });
  } catch {
    // Telemetry must never break the app.
  }
}

/**
 * Turn browser telemetry off for the rest of this page load: stop the replay
 * recorder, drop anything held, and make track(), setTelemetryUser() and
 * reportException() no-ops. For when the user withdraws consent mid-session.
 * See the module header for what it cannot undo.
 */
export function disableTelemetry(): void {
  disabled = true;
  pending = [];
  try {
    sink?.disable();
  } catch {
    // Telemetry must never break the app.
  }
}

/** Whether disableTelemetry() has been called. Read by the installed recorder. */
export function isTelemetryDisabled(): boolean {
  return disabled;
}

/**
 * Install (or clear, with null) the sink track() forwards to, replaying
 * whatever was held. Called by initBrowserTelemetry; an app has no reason to
 * call it directly. Clearing also drops what was held, so a start that failed
 * does not keep a queue nobody will drain.
 */
export function setTelemetrySink(next: TelemetrySink | null): void {
  sink = next;
  const held = pending;
  pending = [];
  if (!next || disabled) {
    return;
  }
  for (const op of held) {
    try {
      if (op.kind === "track") {
        next.track(op.name, op.attributes);
      } else {
        next.setUser(op.id);
      }
    } catch {
      // Telemetry must never break the app.
    }
  }
}

/** Test seam. */
export function resetTrackState(): void {
  sink = null;
  disabled = false;
  pending = [];
}
