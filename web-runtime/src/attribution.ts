// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Visitor identity and campaign attribution: the part of browser telemetry
// that ties an anonymous visit on a marketing site to the user who later signs
// in to the app.
//
// ── The seam ─────────────────────────────────────────────────────────────
//
// A marketing site links to the app with `?rid=<visitor id>&utm_*=…&ref=…&lp=…`
// (withAttribution). The app, on startup, reads those parameters into storage
// (captureAttribution) and adopts `rid` as its visitor id. When a user signs in
// (setTelemetryUser) the runtime emits one `user_identified` event carrying
// `visitor_id`, the stored utm_* values, `referrer_host` and `landing_path`, so
// a funnel can join "visited the site" to "is user X" and say where the visit
// came from. Every event also carries `visitor.id` and `app.client`.
//
// `ref` is the host of the page that sent the visitor to the site (never the
// URL) and `lp` the site's entry page (a path, never a query). The first page
// load that captures with neither in its URL takes them from itself: the
// referrer's host when it is another site, and its own path.
//
// ── Consent ──────────────────────────────────────────────────────────────
//
// Everything here is the `full` analytics tier (track.ts, "Analytics tiers"):
// a persistent visitor id and a stored campaign need the person's consent.
// telemetry.ts starts it (captureAttribution) only once the tier is `full` —
// `analytics: "full"` plus grantAnalyticsConsent() — and stops it
// (stopAttribution) when consent is withdrawn. Until then no visitor id is
// created, read or stamped, nothing is captured or stored, and
// withAttribution returns URLs untouched. Below `full`, a campaign still
// reaches the console without storage: the first page_viewed of a page load
// carries the URL's utm_* (page-views.ts).
//
// ── First or last touch ──────────────────────────────────────────────────
//
// By default a visit that carries `rid`/`utm_*` replaces what was stored: last
// touch wins, which is right for the app (each hop from the marketing site
// carries that site's own view). A marketing site that attributes acquisition
// to the FIRST campaign that brought a visitor passes `attributionTouch:
// "first"`: once a record is stored, later visits never replace it.
// Dependency-free apart from track.ts, like it.
import {
  analyticsTier,
  isTelemetryDisabled,
  noteAnalyticsStorageKey,
} from "./track.js";

/** localStorage key of the captured `{rid, utm_*}` record. */
export const ATTRIBUTION_STORAGE_KEY = "forge.attribution";
/** Default localStorage key of this browser's own anonymous visitor id. */
export const VISITOR_ID_STORAGE_KEY = "forge.visitor_id";
/** localStorage key of the user id `user_identified` last fired for. */
export const IDENTIFIED_USER_STORAGE_KEY = "forge.identified_user";

/** The query parameters captured from the landing URL, besides `rid`. */
export const UTM_PARAMS = [
  "utm_source",
  "utm_medium",
  "utm_campaign",
  "utm_term",
  "utm_content",
] as const;

/** Longest captured attribution value. */
export const MAX_ATTRIBUTION_VALUE_CHARS = 128;

/** A referrer host (`ref`): a hostname, never a URL. */
const REF_RE =
  /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$/;
/** An entry page (`lp`): a path, no query or fragment. */
const LP_RE = /^\/[A-Za-z0-9/_.~-]{0,127}$/;

function cleanRef(value: string | null | undefined): string | undefined {
  const v = typeof value === "string" ? value.trim().toLowerCase() : "";
  return v.length <= 253 && REF_RE.test(v) ? v : undefined;
}

function cleanPath(value: string | null | undefined): string | undefined {
  const v = typeof value === "string" ? value.trim() : "";
  return LP_RE.test(v) ? v : undefined;
}

/** A visitor id: opaque, URL-safe. Anything else in `rid` is ignored. */
const RID_RE = /^[A-Za-z0-9_-]{8,64}$/;

/** Where an event came from. `landing` is for a marketing site; pass any string for another kind. */
export type AppClient = "web" | "electron" | "landing" | (string & {});

export type Attribution = { rid?: string; ref?: string; lp?: string } & Partial<
  Record<(typeof UTM_PARAMS)[number], string>
>;

interface Store {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

let active = false;
let visitorKey = VISITOR_ID_STORAGE_KEY;
let memory: Attribution = {};
let memoryVisitorId: string | undefined;
let memoryIdentified: string | undefined;

function storage(): Store | null {
  try {
    return typeof window !== "undefined" ? window.localStorage : null;
  } catch {
    return null;
  }
}

function read(key: string): string | null {
  try {
    return storage()?.getItem(key) ?? null;
  } catch {
    return null;
  }
}

function write(key: string, value: string): void {
  try {
    storage()?.setItem(key, value);
  } catch {
    // Storage blocked or full: the in-memory copy still serves this page load.
  }
}

/** The `app.client` for this page: an explicit choice, else `electron` inside Electron, else `web`. */
export function detectAppClient(explicit?: string): AppClient {
  if (typeof explicit === "string" && explicit.trim() !== "") {
    return explicit.trim().slice(0, 32);
  }
  if (typeof window === "undefined") {
    return "web";
  }
  try {
    const proc = (
      window as unknown as { process?: { versions?: { electron?: unknown } } }
    ).process;
    if (proc?.versions?.electron) {
      return "electron";
    }
    if (
      typeof navigator !== "undefined" &&
      /\bElectron\//.test(navigator.userAgent)
    ) {
      return "electron";
    }
  } catch {
    // Fall through to web.
  }
  return "web";
}

function cleanValue(value: string | null | undefined): string | undefined {
  if (typeof value !== "string") {
    return undefined;
  }
  // eslint-disable-next-line no-control-regex
  const v = value.replace(/[\u0000-\u001f\u007f]/g, "").trim();
  return v === "" ? undefined : v.slice(0, MAX_ATTRIBUTION_VALUE_CHARS);
}

/** Pull `rid` and `utm_*` out of a query string. Pure; exported for tests. */
export function parseAttribution(search: string): Attribution {
  const out: Attribution = {};
  let params: URLSearchParams;
  try {
    params = new URLSearchParams(search);
  } catch {
    return out;
  }
  const rid = params.get("rid")?.trim();
  if (rid && RID_RE.test(rid)) {
    out.rid = rid;
  }
  for (const key of UTM_PARAMS) {
    const v = cleanValue(params.get(key));
    if (v) {
      out[key] = v;
    }
  }
  const ref = cleanRef(params.get("ref"));
  if (ref) {
    out.ref = ref;
  }
  const lp = cleanPath(params.get("lp"));
  if (lp) {
    out.lp = lp;
  }
  return out;
}

/**
 * The visit itself, for a page load whose URL names no `ref` or `lp`: the
 * referrer's host when it is another site, and this page's path. Pure;
 * exported for tests.
 */
export function selfVisit(
  referrer: string,
  location: { host: string; pathname: string },
): Attribution {
  const out: Attribution = {};
  try {
    const host = referrer ? new URL(referrer).host.toLowerCase() : "";
    const ref =
      host && host !== location.host.toLowerCase()
        ? cleanRef(host.replace(/:\d+$/, ""))
        : undefined;
    if (ref) {
      out.ref = ref;
    }
  } catch {
    // An unparseable referrer is no referrer.
  }
  const lp = cleanPath(location.pathname);
  if (lp) {
    out.lp = lp;
  }
  return out;
}

function currentVisit(): Attribution {
  if (typeof window === "undefined" || !window.location) {
    return {};
  }
  return selfVisit(
    typeof document !== "undefined" ? document.referrer : "",
    window.location,
  );
}

function readStoredAttribution(): Attribution {
  const raw = read(ATTRIBUTION_STORAGE_KEY);
  if (!raw) {
    return memory;
  }
  try {
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed !== "object" || parsed === null) {
      return memory;
    }
    const out: Attribution = {};
    const rid = (parsed as Record<string, unknown>).rid;
    if (typeof rid === "string" && RID_RE.test(rid)) {
      out.rid = rid;
    }
    for (const key of UTM_PARAMS) {
      const v = cleanValue((parsed as Record<string, string>)[key]);
      if (v) {
        out[key] = v;
      }
    }
    const ref = cleanRef((parsed as Record<string, string>).ref);
    if (ref) {
      out.ref = ref;
    }
    const lp = cleanPath((parsed as Record<string, string>).lp);
    if (lp) {
      out.lp = lp;
    }
    return out;
  } catch {
    return memory;
  }
}

/** Which captured record wins when a visit carries new attribution. */
export type AttributionTouch = "first" | "last";

/**
 * Start attribution for this page load: record `rid` and `utm_*` from the URL
 * and note which storage key holds this browser's own visitor id. A visit that
 * carries any replaces what was stored (`touch: "last"`, the default) or is
 * ignored once a record exists (`"first"`); one that carries none keeps it.
 * Called by initBrowserTelemetry and enableAttribution, i.e. only once
 * telemetry may run. Returns the visitor id. Never throws.
 */
export function captureAttribution(options?: {
  search?: string;
  visitorIdStorageKey?: string;
  touch?: AttributionTouch;
}): string {
  if (isTelemetryDisabled()) {
    return "";
  }
  active = true;
  if (options?.visitorIdStorageKey) {
    visitorKey = options.visitorIdStorageKey;
  }
  // Written below in this tier, so erased with the rest when it ends.
  noteAnalyticsStorageKey(visitorKey);
  const search =
    options?.search ??
    (typeof window !== "undefined" ? (window.location?.search ?? "") : "");
  let incoming = parseAttribution(search);
  const stored = readStoredAttribution();
  if (incoming.ref === undefined && incoming.lp === undefined) {
    // Where the visit itself came from, when the URL does not say: a
    // campaign link's visit, a new external referral, or the first visit
    // of all. A same-site page load with nothing new keeps what is stored.
    const self = currentVisit();
    if (
      Object.keys(incoming).length > 0 ||
      self.ref ||
      Object.keys(stored).length === 0
    ) {
      incoming = { ...incoming, ...self };
    }
  }
  const keep = options?.touch === "first" && Object.keys(stored).length > 0;
  if (Object.keys(incoming).length > 0 && !keep) {
    memory = incoming;
    write(ATTRIBUTION_STORAGE_KEY, JSON.stringify(incoming));
  } else {
    memory = readStoredAttribution();
  }
  return visitorId();
}

/** The captured attribution, or `{}` before captureAttribution. */
export function getAttribution(): Attribution {
  return active ? readStoredAttribution() : {};
}

function newId(): string {
  const c = (globalThis as { crypto?: Crypto }).crypto;
  if (c && typeof c.randomUUID === "function") {
    return c.randomUUID();
  }
  let s = "";
  for (let i = 0; i < 4; i++) {
    s += Math.floor(Math.random() * 0x100000000)
      .toString(16)
      .padStart(8, "0");
  }
  return s;
}

/**
 * This browser's visitor id: the `rid` it arrived with, else its own anonymous
 * id (created on first use and kept in localStorage). Empty before
 * captureAttribution.
 */
export function visitorId(): string {
  if (!active) {
    return "";
  }
  const rid = readStoredAttribution().rid;
  if (rid) {
    return rid;
  }
  const held = read(visitorKey);
  if (held && RID_RE.test(held)) {
    return held;
  }
  if (!memoryVisitorId) {
    memoryVisitorId = newId();
  }
  write(visitorKey, memoryVisitorId);
  return memoryVisitorId;
}

/**
 * Append this browser's `rid`, captured `utm_*`, `ref` and `lp` to an outbound
 * URL — the link from a marketing site to the app, or to a download.
 * Parameters the URL already carries are kept. Below the `full` tier nothing
 * identifying is added and nothing is stored: only this page load's `utm_*`
 * and the referrer's host (`ref`), which say where the visit came from and not
 * who made it, and only to a destination on this page's own host or on one of
 * `options.sites`: a link to anywhere else carries nothing, so a campaign or
 * the referrer is never handed to an unrelated third party. Returns the URL
 * unchanged when it is not an absolute http(s) URL, in the `off` tier and
 * after disableTelemetry() — so it is safe to wrap every outbound link.
 */
export function withAttribution(
  url: string,
  options?: WithAttributionOptions,
): string {
  if (isTelemetryDisabled() || typeof url !== "string") {
    return url;
  }
  if (!active) {
    return analyticsTier() === "off"
      ? url
      : withVisitOnly(url, options?.sites ?? []);
  }
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return url;
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    return url;
  }
  const stored = readStoredAttribution();
  const add: Record<string, string> = {};
  const rid = visitorId();
  if (rid) {
    add.rid = rid;
  }
  for (const key of UTM_PARAMS) {
    const v = stored[key];
    if (v) {
      add[key] = v;
    }
  }
  if (stored.ref) {
    add.ref = stored.ref;
  }
  if (stored.lp) {
    add.lp = stored.lp;
  }
  for (const [key, value] of Object.entries(add)) {
    if (!parsed.searchParams.has(key)) {
      parsed.searchParams.set(key, value);
    }
  }
  return parsed.toString();
}

/** Options of withAttribution. */
export type WithAttributionOptions = {
  /**
   * Below the `full` tier, the other sites a link may carry this visit's
   * `utm_*` and referrer host to: a host (`app.example.com`) or a parent
   * domain (`example.com`, which also admits every subdomain of it). The
   * page's own host always may. Ignored in `full`.
   */
  sites?: readonly string[];
};

// passesVisitTo reports whether a below-full link to host may carry the
// visit: this page's own host, or one of sites (exactly, or a subdomain of it).
function passesVisitTo(host: string, sites: readonly string[]): boolean {
  const dest = host.toLowerCase();
  const here =
    typeof window !== "undefined"
      ? (window.location?.hostname ?? window.location?.host ?? "")
      : "";
  if (here !== "" && dest === here.toLowerCase().replace(/:\d+$/, "")) {
    return true;
  }
  return sites.some((site) => {
    const s = site.trim().toLowerCase().replace(/^\.+/, "");
    return s !== "" && (dest === s || dest.endsWith("." + s));
  });
}

// withAttribution below the full tier: this page load's utm_* and the
// referrer's host, from the URL and document, never storage, never an id,
// and only to this site or the caller's named sites (passesVisitTo).
function withVisitOnly(url: string, sites: readonly string[]): string {
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return url;
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    return url;
  }
  if (!passesVisitTo(parsed.hostname, sites)) {
    return url;
  }
  const here =
    typeof window !== "undefined"
      ? parseAttribution(window.location?.search ?? "")
      : {};
  const add: Record<string, string> = {};
  for (const key of UTM_PARAMS) {
    const v = here[key];
    if (v) {
      add[key] = v;
    }
  }
  const ref = here.ref ?? currentVisit().ref;
  if (ref) {
    add.ref = ref;
  }
  if (Object.keys(add).length === 0) {
    return url;
  }
  for (const [key, value] of Object.entries(add)) {
    if (!parsed.searchParams.has(key)) {
      parsed.searchParams.set(key, value);
    }
  }
  return parsed.toString();
}

/**
 * The props of the one-time `user_identified` event for `userId`, or null when
 * it already fired for that user in this browser (or attribution is not
 * running). Marks it fired. Empty ids never identify.
 */
export function takeIdentifyProps(
  userId: string,
): Record<string, string> | null {
  if (!active || isTelemetryDisabled() || userId === "") {
    return null;
  }
  if ((read(IDENTIFIED_USER_STORAGE_KEY) ?? memoryIdentified) === userId) {
    return null;
  }
  memoryIdentified = userId;
  write(IDENTIFIED_USER_STORAGE_KEY, userId);
  const props: Record<string, string> = { visitor_id: visitorId() };
  const stored = readStoredAttribution();
  for (const key of UTM_PARAMS) {
    const v = stored[key];
    if (v) {
      props[key] = v;
    }
  }
  if (stored.ref) {
    props.referrer_host = stored.ref;
  }
  if (stored.lp) {
    props.landing_path = stored.lp;
  }
  return props;
}

/**
 * End attribution for this page load (consent withdrawn): no visitor id,
 * nothing captured, withAttribution returns URLs untouched. What was stored
 * is erased by track.ts (eraseAnalyticsDeviceState).
 */
export function stopAttribution(): void {
  active = false;
  memory = {};
  memoryVisitorId = undefined;
  memoryIdentified = undefined;
}

/** Test seam. */
export function resetAttributionState(): void {
  active = false;
  visitorKey = VISITOR_ID_STORAGE_KEY;
  memory = {};
  memoryVisitorId = undefined;
  memoryIdentified = undefined;
}
