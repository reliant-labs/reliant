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
//
// ── Analytics tiers ──────────────────────────────────────────────────────
//
// Product analytics run in one of four tiers, ordered off < aggregate <
// identified < full: the LOWEST of the app's mode (initBrowserTelemetry's
// `analytics`, default "identified"), the consent it has signalled, and the
// browser's own privacy signals:
//
//   off         mode "off", optOutOfAnalytics(), or disableTelemetry(): no
//               product events and no user id. Errors and traces still flow.
//   aggregate   mode "aggregate", and always under GPC / DNT: product events
//               with no id at all, even when signed in. NOTHING is stored on
//               the device: no cookie, no localStorage, no sessionStorage
//               (the RUM session id is kept in memory). The platform's
//               ingest edge counts visitors with a daily-salted hash; the
//               browser sends no fingerprinting input.
//   identified  the default: as aggregate, plus the signed-in user's opaque
//               id (setTelemetryUser / identifyUser) once one is named.
//               Still nothing stored.
//   full        mode "full" AND grantAnalyticsConsent(): a persistent
//               visitor id, the RUM session cookie, campaign attribution
//               across days, and session replay when configured.
//
// The tier is decided here, and only here, because every product event and
// the user id pass through this module: track() drops events in `off`, and
// the user id reaches the SDK only in `identified` and `full`.

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
  | {
      kind: "track";
      name: string;
      attributes: Record<string, TrackValue>;
      onSent?: () => void;
      // The user setTelemetryUser had named when the event was recorded
      // (undefined: none yet), stamped again on replay, so a later
      // setTelemetryUser while the SDK loads cannot re-attribute it.
      user?: string;
    }
  | { kind: "user"; id: string };

// Tell an emitter its event reached the sink (recordProductEvent's onSent).
function sent(onSent: (() => void) | undefined): void {
  try {
    onSent?.();
  } catch {
    // Telemetry must never break the app.
  }
}

let sink: TelemetrySink | null = null;
let disabled = false;
let pending: Pending[] = [];

/**
 * The ceiling an app sets on its analytics (initBrowserTelemetry's
 * `analytics`, the runtime config's ANALYTICS_MODE), ordered off < aggregate
 * < identified < full. Unset is `identified`.
 */
export type AnalyticsMode = "off" | "aggregate" | "identified" | "full";
/** The tier a browser's analytics run in right now. See the module header. */
export type AnalyticsTier = "off" | "aggregate" | "identified" | "full";

/**
 * localStorage key of a person's objection to analytics (optOutOfAnalytics).
 * The one thing kept on the device below `full`: remembering a "no" is
 * strictly necessary to honour it.
 */
export const ANALYTICS_OPT_OUT_STORAGE_KEY = "forge.analytics_opt_out";

// What the runtime keeps on the device in `full`, and erases when the tier
// drops below it (revokeAnalyticsConsent, optOutOfAnalytics, or a start in a
// mode that can never reach `full`). The names are the modules' own
// constants (attribution.ts, auth-events.ts, session-cookie-fallback.ts,
// telemetry.ts), spelled here because this module imports nothing; a test
// holds them equal.
const FULL_TIER_LOCAL_KEYS = [
  "forge.visitor_id",
  "forge.attribution",
  "forge.identified_user",
  "forge.auth.seen",
  "forge.rum.session",
];
const FULL_TIER_SESSION_KEYS = ["forge.replay.sampled"];
const RUM_COOKIE = "__rum_sid";

let analyticsMode: AnalyticsMode = "identified";
let analyticsConsent = false;
// null until first read from localStorage; a write that fails still holds here.
let optedOut: boolean | null = null;
// The user the app last named, whether or not the tier lets it be stamped.
let namedUser = "";
// Extra localStorage keys the runtime wrote in `full` (a site's own visitor
// id key), erased with the rest.
const extraLocalKeys = new Set<string>();
const tierListeners = new Set<
  (tier: AnalyticsTier, previous: AnalyticsTier) => void
>();
let warnedReserved = false;
// The id the sink was last given, so a tier change re-stamps only a change.
let lastStamped: string | null = null;

/**
 * Event-name prefixes reserved for the runtime's and forge's own events
 * (`auth_*`, `page_viewed`, `flag_evaluated`, `web_*`). track() still sends
 * such a name, and warns once per page load, so an app's existing event is
 * never lost.
 */
export const RESERVED_EVENT_PREFIXES = ["auth_", "page_", "flag_", "web_"];

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

// The user the queue names at this point: its last held setTelemetryUser.
function heldUser(): string | undefined {
  for (let i = pending.length - 1; i >= 0; i--) {
    const op = pending[i]!;
    if (op.kind === "user") {
      return op.id;
    }
  }
  return undefined;
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
  if (
    typeof name === "string" &&
    !warnedReserved &&
    RESERVED_EVENT_PREFIXES.some((prefix) => name.trim().startsWith(prefix))
  ) {
    warnedReserved = true;
    try {
      console.warn(
        `[forge] track("${name.trim().slice(0, 64)}"): the prefixes ${RESERVED_EVENT_PREFIXES.join(", ")} are reserved for forge's own events. It is sent; rename it to keep the console's built-in panels exact.`,
      );
    } catch {
      // Telemetry must never break the app.
    }
  }
  recordProductEvent(name, props);
}

/**
 * track() without the reserved-prefix warning: how the runtime's own
 * emitters (page views, auth events, exposures) record their events. Not
 * exported from the barrel.
 *
 * `onSent` runs once the event is handed to the SDK's sink: at once when the
 * SDK is running, or when a held event is replayed. It never runs for an
 * event that is dropped (tier `off`, a full queue, a start that failed and
 * cleared the sink, a page that navigated away first), so state that must
 * follow delivery (auth-events.ts's daily marker) is committed there.
 */
export function recordProductEvent(
  name: string,
  props?: TrackProps,
  onSent?: () => void,
): void {
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
      if (analyticsTier() !== "off") {
        sink.track(trimmed, attributes);
        sent(onSent);
      }
    } else {
      enqueue({
        kind: "track",
        name: trimmed,
        attributes,
        onSent,
        user: heldUser(),
      });
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
 * SDK has started: each held event is replayed under the user named when it
 * was recorded, and the last call is what later events carry.
 *
 * The id reaches the SDK only in the `identified` and `full` analytics tiers:
 * under Global Privacy Control or Do Not Track, in modes "off" and
 * "aggregate" and after optOutOfAnalytics() the browser's events carry no
 * user id (an empty `userId`). In mode "identified" or "full", naming a user
 * moves the tier from `aggregate` to `identified`.
 */
export function setTelemetryUser(id: string | null | undefined): void {
  if (typeof window === "undefined" || disabled) {
    return;
  }
  const value =
    typeof id === "string" ? id.trim().slice(0, MAX_USER_ID_CHARS) : "";
  const before = analyticsTier();
  namedUser = value;
  try {
    if (sink) {
      lastStamped = stampable(value);
      sink.setUser(lastStamped);
    } else {
      pending = pending.filter((op) => op.kind !== "user");
      enqueue({ kind: "user", id: value });
    }
  } catch {
    // Telemetry must never break the app.
  }
  notifyTier(before, false, false);
}

// The id the SDK may carry for `id`: itself in `identified` and `full`,
// nothing in `off`, in mode "aggregate" or under GPC / DNT.
function stampable(id: string): string {
  return idsAllowed() ? id : "";
}

function idsAllowed(): boolean {
  return (
    !disabled &&
    analyticsMode !== "off" &&
    analyticsMode !== "aggregate" &&
    !analyticsOptedOut() &&
    !browserPrivacyOptOut()
  );
}

/**
 * Turn browser telemetry off for the rest of this page load: stop the replay
 * recorder, drop anything held, and make track(), setTelemetryUser() and
 * reportException() no-ops. For when the user withdraws consent mid-session.
 * See the module header for what it cannot undo.
 */
export function disableTelemetry(): void {
  const before = analyticsTier();
  disabled = true;
  pending = [];
  notifyTier(before, false, false);
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
  lastStamped = null;
  const held = pending;
  pending = [];
  if (!next || disabled) {
    return;
  }
  // Each held event is sent under the user it was recorded with, and the
  // latest held user is stamped last, for whatever comes after.
  const stamp = (id: string) => {
    const value = stampable(id);
    if (value !== lastStamped) {
      lastStamped = value;
      next.setUser(value);
    }
  };
  let latest: string | undefined;
  for (const op of held) {
    try {
      if (op.kind === "track") {
        if (analyticsTier() !== "off") {
          if (op.user !== undefined) {
            stamp(op.user);
          }
          next.track(op.name, op.attributes);
          sent(op.onSent);
        }
      } else {
        latest = op.id;
      }
    } catch {
      // Telemetry must never break the app.
    }
  }
  if (latest !== undefined) {
    try {
      stamp(latest);
    } catch {
      // Telemetry must never break the app.
    }
  }
}

/**
 * Whether this browser asks not to be tracked: Global Privacy Control, or Do
 * Not Track in any of its spellings. Session replay and the browser's auth
 * events honour it; ordinary error and trace telemetry does not. An
 * unreadable preference fails closed (true).
 */
export function browserPrivacyOptOut(): boolean {
  try {
    if (typeof navigator === "undefined") return false;
    const nav = navigator as Navigator & {
      globalPrivacyControl?: boolean;
      msDoNotTrack?: string;
    };
    const legacy =
      typeof window === "undefined"
        ? undefined
        : (window as Window & { doNotTrack?: string }).doNotTrack;
    const dnt = [nav.doNotTrack, nav.msDoNotTrack, legacy];
    return (
      nav.globalPrivacyControl === true ||
      dnt.some((value) => value === "1" || value === "yes")
    );
  } catch {
    return true;
  }
}

/**
 * The tier this browser's product analytics run in now (see the module
 * header): the lowest of the app's mode, its consent and the browser's
 * GPC / DNT. Never throws.
 */
export function analyticsTier(): AnalyticsTier {
  if (disabled || analyticsMode === "off" || analyticsOptedOut()) {
    return "off";
  }
  if (browserPrivacyOptOut() || analyticsMode === "aggregate") {
    return "aggregate";
  }
  if (analyticsMode === "full" && analyticsConsent) {
    return "full";
  }
  return namedUser !== "" ? "identified" : "aggregate";
}

/**
 * The person agreed to analytics: with the app's mode at "full", the tier
 * becomes `full` (a persistent visitor id, the RUM session cookie, campaign
 * attribution across days, replay from the next page load when configured).
 * Below "full" the tier is unchanged. Clears an earlier optOutOfAnalytics().
 * Consent is not remembered by the runtime: call it on every page load once
 * your consent manager says yes. Never throws; a no-op on the server.
 */
export function grantAnalyticsConsent(): void {
  if (typeof window === "undefined" || disabled) {
    return;
  }
  const before = analyticsTier();
  analyticsConsent = true;
  if (analyticsOptedOut()) {
    optedOut = false;
    try {
      localStorageOrNull()?.removeItem(ANALYTICS_OPT_OUT_STORAGE_KEY);
    } catch {
      // Not persisted: this page load still honours the grant.
    }
  }
  notifyTier(before, false);
}

/**
 * The person withdrew analytics consent: back to `identified` (or
 * `aggregate` while signed out), and everything `full` kept on the device
 * (the visitor id, the stored campaign, the RUM session cookie) is erased.
 * Never throws; a no-op on the server.
 */
export function revokeAnalyticsConsent(): void {
  if (typeof window === "undefined") {
    return;
  }
  const before = analyticsTier();
  analyticsConsent = false;
  eraseAnalyticsDeviceState();
  notifyTier(before, true);
}

/**
 * The person objects to analytics: no product events and no user id from
 * this browser, now and on every later page load, until
 * grantAnalyticsConsent(). The objection is the one thing stored (it must be
 * remembered to be honoured); everything else analytics kept is erased.
 * Error and trace telemetry continue. Never throws; a no-op on the server.
 */
export function optOutOfAnalytics(): void {
  if (typeof window === "undefined") {
    return;
  }
  const before = analyticsTier();
  analyticsConsent = false;
  optedOut = true;
  try {
    localStorageOrNull()?.setItem(ANALYTICS_OPT_OUT_STORAGE_KEY, "1");
  } catch {
    // Not persisted: this page load still honours it.
  }
  eraseAnalyticsDeviceState();
  notifyTier(before, true);
}

/** Whether this browser objected to analytics (optOutOfAnalytics). */
export function analyticsOptedOut(): boolean {
  if (optedOut === null) {
    try {
      optedOut =
        localStorageOrNull()?.getItem(ANALYTICS_OPT_OUT_STORAGE_KEY) === "1";
    } catch {
      optedOut = false;
    }
  }
  return optedOut;
}

/**
 * Set the app's analytics mode. Called by initBrowserTelemetry with its
 * `analytics` option; an app has no reason to call it. Unset or unknown is
 * `identified`.
 */
export function setAnalyticsMode(mode: AnalyticsMode | undefined): void {
  const before = analyticsTier();
  analyticsMode =
    mode === "off" || mode === "aggregate" || mode === "full"
      ? mode
      : "identified";
  notifyTier(before, false);
}

/** The app's analytics mode (setAnalyticsMode). */
export function currentAnalyticsMode(): AnalyticsMode {
  return analyticsMode;
}

/**
 * Called with each tier change (and once more after revokeAnalyticsConsent /
 * optOutOfAnalytics erased the device, even when the tier did not move).
 * Returns the unsubscribe. For telemetry.ts.
 */
export function onAnalyticsTierChange(
  listener: (tier: AnalyticsTier, previous: AnalyticsTier) => void,
): () => void {
  tierListeners.add(listener);
  return () => tierListeners.delete(listener);
}

/** Note a localStorage key the runtime wrote in `full`, erased with the rest. */
export function noteAnalyticsStorageKey(key: string): void {
  extraLocalKeys.add(key);
}

/**
 * The opaque id the app last named, when the tier lets the SDK carry it, else
 * "". For the runtime's own per-person bookkeeping (exposure dedupe).
 */
export function stampedTelemetryUser(): string {
  return stampable(namedUser);
}

/**
 * Erase what the `full` tier keeps on the device: the visitor id, the stored
 * campaign, the identified-user and daily sign-in markers, the RUM session
 * (cookie or its localStorage fallback) and the replay sampling decision.
 * The opt-out flag is kept. Never throws.
 */
export function eraseAnalyticsDeviceState(): void {
  const local = localStorageOrNull();
  for (const key of [...FULL_TIER_LOCAL_KEYS, ...extraLocalKeys]) {
    try {
      local?.removeItem(key);
    } catch {
      // Blocked storage holds nothing of ours either.
    }
  }
  try {
    const session =
      typeof window === "undefined" ? null : window.sessionStorage;
    for (const key of FULL_TIER_SESSION_KEYS) {
      session?.removeItem(key);
    }
  } catch {
    // As above.
  }
  try {
    if (
      typeof document !== "undefined" &&
      typeof document.cookie === "string" &&
      document.cookie.includes(`${RUM_COOKIE}=`)
    ) {
      document.cookie = `${RUM_COOKIE}=; path=/; max-age=0`;
    }
  } catch {
    // A sandboxed document has no cookies to erase.
  }
}

function localStorageOrNull(): Storage | null {
  try {
    return typeof window === "undefined" ? null : (window.localStorage ?? null);
  } catch {
    return null;
  }
}

// Tell listeners when the tier moved (or always, after an erase), and, with
// `restamp`, give the SDK the user id the new tier allows (setTelemetryUser
// has just done that itself; disableTelemetry leaves the SDK as it is).
function notifyTier(
  before: AnalyticsTier,
  always: boolean,
  restamp = true,
): void {
  const after = analyticsTier();
  if (after === before && !always) {
    return;
  }
  const stamp = stampable(namedUser);
  if (restamp && sink && stamp !== (lastStamped ?? "")) {
    try {
      lastStamped = stamp;
      sink.setUser(stamp);
    } catch {
      // Telemetry must never break the app.
    }
  }
  for (const listener of [...tierListeners]) {
    try {
      listener(after, before);
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
  analyticsMode = "identified";
  analyticsConsent = false;
  optedOut = null;
  namedUser = "";
  extraLocalKeys.clear();
  tierListeners.clear();
  warnedReserved = false;
  lastStamped = null;
}
