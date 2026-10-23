// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Visitor identity and campaign attribution: the part of browser telemetry
// that ties an anonymous visit on a marketing site to the user who later signs
// in to the app.
//
// ── The seam ─────────────────────────────────────────────────────────────
//
// A marketing site links to the app with `?rid=<visitor id>&utm_*=…`
// (withAttribution). The app, on startup, reads those parameters into storage
// (captureAttribution) and adopts `rid` as its visitor id. When a user signs in
// (setTelemetryUser) the runtime emits one `user_identified` event carrying
// `visitor_id` and the stored utm_* values, so a funnel can join "visited the
// site" to "is user X". Every event also carries `visitor.id` and `app.client`.
//
// ── Consent ──────────────────────────────────────────────────────────────
//
// Nothing here runs until initBrowserTelemetry does, and that is the point an
// app gates on consent. Before it (or after disableTelemetry) captureAttribution
// stores nothing and withAttribution returns the URL untouched. Dependency-free
// apart from track.ts, like it.
import { isTelemetryDisabled } from "./track.js";

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

/** A visitor id: opaque, URL-safe. Anything else in `rid` is ignored. */
const RID_RE = /^[A-Za-z0-9_-]{8,64}$/;

/** Where an event came from. `landing` is for a marketing site; pass any string for another kind. */
export type AppClient = "web" | "electron" | "landing" | (string & {});

export type Attribution = { rid?: string } & Partial<
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
  return out;
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
    return out;
  } catch {
    return memory;
  }
}

/**
 * Start attribution for this page load: record `rid` and `utm_*` from the URL
 * (a visit that carries any replaces what was stored — last touch wins; one
 * that carries none keeps it) and note which storage key holds this browser's
 * own visitor id. Called by initBrowserTelemetry, i.e. only once telemetry may
 * run. Returns the visitor id. Never throws.
 */
export function captureAttribution(options?: {
  search?: string;
  visitorIdStorageKey?: string;
}): string {
  if (isTelemetryDisabled()) {
    return "";
  }
  active = true;
  if (options?.visitorIdStorageKey) {
    visitorKey = options.visitorIdStorageKey;
  }
  const search =
    options?.search ??
    (typeof window !== "undefined" ? (window.location?.search ?? "") : "");
  const incoming = parseAttribution(search);
  if (Object.keys(incoming).length > 0) {
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
 * Append this browser's `rid` and captured `utm_*` to an outbound URL — the
 * link from a marketing site to the app, or to a download. Parameters the URL
 * already carries are kept. Returns the URL unchanged when it is not an
 * absolute http(s) URL, before telemetry has started, and after
 * disableTelemetry() — so it is safe to wrap every outbound link.
 */
export function withAttribution(url: string): string {
  if (!active || isTelemetryDisabled() || typeof url !== "string") {
    return url;
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
  return props;
}

/** Test seam. */
export function resetAttributionState(): void {
  active = false;
  visitorKey = VISITOR_ID_STORAGE_KEY;
  memory = {};
  memoryVisitorId = undefined;
  memoryIdentified = undefined;
}
