/**
 * The landing → app attribution handoff, held from boot until telemetry starts.
 *
 * The landing site links to the app as `https://app.reliantlabs.io/?rid=<visitor
 * id>&utm_*=…` (landing-page src/lib/attribution.ts, Analytics consent only). The
 * forge web runtime reads those parameters when attribution starts, adopts `rid` as
 * this browser's visitor id, and stamps it and the `utm_*` on the one
 * `user_identified` event it emits at sign-in. That event is the only bridge from a
 * landing visitor to a user.
 *
 * But this app starts telemetry only after sign-in and the privacy-settings load
 * (telemetry.ts initTelemetry, from AuthInitializer). By then the router has
 * redirected a signed-out visitor to sign-in, and the sign-in itself (an OAuth round
 * trip, a magic link) has replaced the URL with a page load of its own: the runtime
 * read an address bar with no `rid`, so `user_identified` carried the app's own
 * anonymous id and no `utm_*`. In prod, 0 of 12 `user_identified` matched a landing
 * visitor (growth baseline, 2026-11-04).
 *
 * So the boot (attributionBoot.ts, imported before the router) HOLDS the handoff,
 * and initTelemetry READS it and gives it to the runtime (enableAttribution's
 * `search`). Held in localStorage, not sessionStorage: a magic link opens a new tab.
 * Only what the runtime itself would capture is kept (a well-formed `rid` and the
 * `utm_*` values, bounded), for at most HOLD_MS, and dropped once telemetry has
 * started with it (a failed start keeps it for the next attempt). Nothing is sent
 * from here: the runtime still applies the app's own consent when telemetry starts,
 * and telemetry.ts drops the held handoff when analytics consent is off.
 */

/** localStorage key of the held handoff. */
export const LANDING_ATTRIBUTION_KEY = "reliant.landing_attribution";

/** How long a held handoff waits for telemetry: a sign-up that takes longer is a new visit. */
export const HOLD_MS = 7 * 24 * 60 * 60 * 1000;

/** The campaign parameters the runtime captures (forge-web-runtime UTM_PARAMS). */
const UTM_KEYS = ["utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content"] as const;

/** The runtime's visitor-id shape: anything else in `rid` is ignored there too. */
const RID_RE = /^[A-Za-z0-9_-]{8,64}$/;

/** The runtime's longest captured value. */
const MAX_VALUE_CHARS = 128;

interface Held {
  /** The handoff as a query string the runtime parses ("?rid=…&utm_source=…"). */
  search: string;
  /** When it was held (ms since the epoch). */
  at: number;
}

function storage(): Storage | null {
  try {
    return typeof window !== "undefined" ? window.localStorage : null;
  } catch {
    return null;
  }
}

/** The handoff in search, reduced to what the runtime captures; "" when it carries none. Pure. */
export function handoffQuery(search: string): string {
  let params: URLSearchParams;
  try {
    params = new URLSearchParams(search);
  } catch {
    return "";
  }
  const out = new URLSearchParams();
  const rid = params.get("rid")?.trim();
  if (rid && RID_RE.test(rid)) out.set("rid", rid);
  for (const key of UTM_KEYS) {
    // eslint-disable-next-line no-control-regex
    const v = params.get(key)?.replace(/[\u0000-\u001f\u007f]/g, "").trim();
    if (v) out.set(key, v.slice(0, MAX_VALUE_CHARS));
  }
  const q = out.toString();
  return q ? `?${q}` : "";
}

/**
 * Hold the handoff this page load arrived with. A load that carries none keeps an
 * earlier one (the sign-in's own page load must not erase the landing visit's), and
 * one that carries a handoff replaces it, as the runtime's last-touch capture would.
 * Never throws.
 */
export function holdLandingAttribution(search: string = currentSearch(), now: number = Date.now()): void {
  const q = handoffQuery(search);
  if (!q) return;
  try {
    storage()?.setItem(LANDING_ATTRIBUTION_KEY, JSON.stringify({ search: q, at: now } satisfies Held));
  } catch {
    // Storage blocked or full: the handoff is lost, as it was before.
  }
}

/**
 * The held handoff as a query string for the runtime, or undefined when none is
 * held or it is older than HOLD_MS. Reading it leaves it held: telemetry drops it
 * (dropLandingAttribution) only once the runtime has started with it, so a failed
 * start leaves it for the next attempt. An expired or corrupt record is removed.
 */
export function readLandingAttribution(now: number = Date.now()): string | undefined {
  let raw: string | null = null;
  try {
    raw = storage()?.getItem(LANDING_ATTRIBUTION_KEY) ?? null;
  } catch {
    return undefined;
  }
  if (!raw) return undefined;
  let q: string | undefined;
  try {
    const held = JSON.parse(raw) as Partial<Held>;
    if (typeof held.search === "string" && typeof held.at === "number" && now - held.at <= HOLD_MS && now >= held.at) {
      q = handoffQuery(held.search) || undefined;
    }
  } catch {
    q = undefined;
  }
  if (!q) dropLandingAttribution();
  return q;
}

/** Forget a held handoff: the runtime started with it, or analytics consent is off. */
export function dropLandingAttribution(): void {
  try {
    storage()?.removeItem(LANDING_ATTRIBUTION_KEY);
  } catch {
    // Best-effort.
  }
}

function currentSearch(): string {
  try {
    return typeof window !== "undefined" ? (window.location?.search ?? "") : "";
  } catch {
    return "";
  }
}
