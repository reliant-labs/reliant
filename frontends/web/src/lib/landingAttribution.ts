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
 * Only what the runtime itself would capture is kept (a well-formed `rid`, the
 * `utm_*` values, bounded, and the landing visit's referrer host `ref` and entry
 * page `lp`), for at most HOLD_MS, and dropped once telemetry has started with it
 * (a failed start keeps it for the next attempt). Nothing is sent from here: the
 * runtime still applies the app's own consent when telemetry starts, and
 * telemetry.ts drops the held handoff when analytics consent is off.
 *
 * SIGNUP ATTRIBUTION. The same handoff is held a second time, under its own key,
 * for the control plane: api/signupAttribution.ts sends it in the
 * X-Reliant-Attribution header on control-plane requests until one succeeds
 * signed in, and the request that creates the account stores it as the account's
 * first-touch attribution (control-plane internal/signupattr) and puts it on
 * user_signed_up. That copy is not the runtime's, so telemetry starting (or
 * consent being off) does not drop it: it is the landing site's own handoff to
 * its own product, counted server-side.
 */

import { isJourneyId, type JourneyId } from "../components/Onboarding/tourParam";

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

/** A referrer host (`ref`): a hostname, never a URL. */
const REF_RE = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$/;

/** An entry page (`lp`): a path, no query or fragment. */
const LP_RE = /^\/[A-Za-z0-9/_.~-]{0,127}$/;

/** localStorage key of the signup copy of the handoff (see SIGNUP ATTRIBUTION). */
export const SIGNUP_ATTRIBUTION_KEY = "reliant.signup_attribution";

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
  const ref = params.get("ref")?.trim().toLowerCase();
  if (ref && ref.length <= 253 && REF_RE.test(ref)) out.set("ref", ref);
  const lp = params.get("lp")?.trim();
  if (lp && LP_RE.test(lp)) out.set("lp", lp);
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
    storage()?.setItem(SIGNUP_ATTRIBUTION_KEY, JSON.stringify({ search: q, at: now } satisfies Held));
  } catch {
    // Storage blocked or full: the handoff is lost, as it was before.
  }
}

/**
 * The signup copy of the handoff, without its "?", for the X-Reliant-Attribution
 * header; undefined when none is held or it is older than HOLD_MS (then removed).
 */
export function readSignupAttribution(now: number = Date.now()): string | undefined {
  let raw: string | null = null;
  try {
    raw = storage()?.getItem(SIGNUP_ATTRIBUTION_KEY) ?? null;
  } catch {
    return undefined;
  }
  if (!raw) return undefined;
  let q = "";
  try {
    const held = JSON.parse(raw) as Partial<Held>;
    if (typeof held.search === "string" && typeof held.at === "number" && now - held.at <= HOLD_MS && now >= held.at) {
      q = handoffQuery(held.search);
    }
  } catch {
    q = "";
  }
  if (!q) {
    dropSignupAttribution();
    return undefined;
  }
  return q.slice(1);
}

/** Forget the signup copy: a signed-in control-plane request carrying it succeeded. */
export function dropSignupAttribution(): void {
  try {
    storage()?.removeItem(SIGNUP_ATTRIBUTION_KEY);
  } catch {
    // Best-effort.
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

// ─── The journey choice ─────────────────────────────────────────────────────
// The landing page's picker ("Explore a forge app" / "Use AI workflows") links
// here with `?journey=forge|workflows`, and the sign-in round trip loses it
// exactly as it loses `rid`. It is held the same way, but separately and with
// NO consent gate: it is a product choice, not an identifier, so it never
// reaches the runtime and telemetry's consent drop above doesn't touch it.
// Onboarding reads it to preselect the journey picker; the picker drops it
// once the user has chosen (or dismissed) a journey.

/** localStorage key of the held journey choice. */
export const LANDING_JOURNEY_KEY = "reliant.landing_journey";

interface HeldJourney {
  journey: JourneyId;
  at: number;
}

/** The `journey` param in search, when it names a known journey. Pure. */
export function journeyFromSearch(search: string): JourneyId | undefined {
  try {
    const value = new URLSearchParams(search).get("journey")?.trim();
    return isJourneyId(value) ? value : undefined;
  } catch {
    return undefined;
  }
}

/**
 * Hold the journey this page load arrived with. A load without one keeps an
 * earlier one (the sign-in's own page load must not erase it). Never throws.
 */
export function holdLandingJourney(search: string = currentSearch(), now: number = Date.now()): void {
  const journey = journeyFromSearch(search);
  if (!journey) return;
  try {
    storage()?.setItem(LANDING_JOURNEY_KEY, JSON.stringify({ journey, at: now } satisfies HeldJourney));
  } catch {
    // Storage blocked or full: the picker just isn't preselected.
  }
}

/** The held journey, or undefined when none is held or it is older than HOLD_MS. */
export function readLandingJourney(now: number = Date.now()): JourneyId | undefined {
  let raw: string | null = null;
  try {
    raw = storage()?.getItem(LANDING_JOURNEY_KEY) ?? null;
  } catch {
    return undefined;
  }
  if (!raw) return undefined;
  try {
    const held = JSON.parse(raw) as Partial<HeldJourney>;
    if (isJourneyId(held.journey) && typeof held.at === "number" && now - held.at <= HOLD_MS && now >= held.at) {
      return held.journey;
    }
  } catch {
    // fall through: corrupt
  }
  dropLandingJourney();
  return undefined;
}

/** Forget the held journey: the user has chosen one in the app. */
export function dropLandingJourney(): void {
  try {
    storage()?.removeItem(LANDING_JOURNEY_KEY);
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
