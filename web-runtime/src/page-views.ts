// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Page views as product events: one `page_viewed {route}` per route the user
// lands on, so a funnel or a "top pages" panel over product events needs no
// code in the app.
//
// ── Why not the SDK's own routeChange spans ──────────────────────────────
//
// HyperDX already records `documentLoad` and `routeChange` spans, but those
// are traces: they carry the full page URL, and the product-event view reads
// only `custom-action` spans (what track() records). A page view that a
// funnel can use has to be a product event, with a route that is safe to keep.
//
// ── What `route` is ──────────────────────────────────────────────────────
//
// The PATHNAME, never the URL: the query string and fragment are where tokens,
// OAuth codes and search terms live, and they are cut. The frontend's mount
// prefix (basePath) is stripped, so `/admin/billing` under basePath `/admin`
// is `/billing`, the route the app's own router knows.
//
// Every other segment is kept only when it is a plain route word: lowercase
// letters, joined by single `-` or `_` (`settings`, `sign-in`, `order_items`),
// at most MAX_ROUTE_WORD_CHARS long, or a version (`v2`). Anything else — a
// number, a UUID, a ULID, a short base64url id (`aB3xY_9k`), `jane.doe`,
// `~jane`, an encoded or mixed-case segment, anything with a digit — becomes
// `:id`. The rule keeps what a route table is made of and drops what an id
// is made of, rather than recognizing id formats one by one: an id format
// nobody listed would otherwise leave the browser. So `/orders/8812` and
// `/orders/aB3xY_9k` are both `/orders/:id`. The one thing a lexical rule
// cannot tell from a route word is an all-lowercase-letters value
// (`/users/jane`); an app with such routes names them itself with
// `routeName`. forge's generated pages keep ids in the query
// (`/orders/view?id=…`) already; this is for the routes an app writes itself.
//
// `routeName` output is held to the same rule (it is commonly built from the
// pathname), except that it may also use parameter placeholders: `:name`,
// `[name]`, `[...name]`, `[[...name]]` and `*`.
//
// ── How a route change is seen ───────────────────────────────────────────
//
// Next.js's App Router and React Router both navigate with
// history.pushState / replaceState and hear back/forward as popstate. Where
// the browser has the Navigation API, its `currententrychange` event reports
// all of those (and intercepted navigations) without touching `history`;
// elsewhere pushState and replaceState are wrapped — each wrapper calls
// through untouched, then looks at the new pathname — and popstate is
// listened to. The current route is reported once at install.
//
// Consecutive identical routes are one view: a replaceState that only moves
// the query, the hash or scroll state, or a re-render that pushes the same
// path, reports nothing.
//
// ── Where the visit came from, without storing anything ──────────────────
//
// Acquisition panels need the source of a visit, and the cookieless tiers
// store nothing to remember it by, so the page view itself carries it:
//
//   entry          "true" on the first view of a page load (absent after).
//   referrer_host  the host of document.referrer, on the entry view only
//                  (a client-side navigation keeps the page's ORIGINAL
//                  referrer, which is not where this view came from); absent
//                  when it is this site (its own host, a `www.` twin, or one
//                  of `siteHosts`).
//   referrer_path  that referrer's path, scrubbed like a route.
//   utm_source … utm_content, ref
//                  from the URL's query, only when present, each at most
//                  MAX_CAMPAIGN_VALUE_CHARS; `ref` is Plausible's `?ref=` or
//                  `?source=`. A value containing `@` is dropped (email
//                  tools put addresses in utm_content).
//   click_id       WHICH ad network's click id the URL carries (`google`,
//                  `microsoft`, `meta`, `tiktok`, `linkedin`, `other`) —
//                  never the id: a click id is a persistent cross-site
//                  identifier.
//
// Country, device, browser and OS are derived at the ingest edge, not here.
//
// ── How long the view held the reader ────────────────────────────────────
//
// With `engagement` (on wherever initBrowserTelemetry records page views),
// each view also ends in one `page_engaged {route, view_id, engaged_ms, scroll_pct}`:
// the visible, focused, active time and the scroll depth of that view,
// reported at the next route change, hide or unload (page-engagement.ts).
//
// Dependency-free like track.ts: nothing here imports the SDK, and nothing
// runs until initBrowserTelemetry installs it (the consent gate), which also
// removes it again on disableTelemetry().
import { normalizeBasePath } from "./basepath.js";
import { installPageEngagement } from "./page-engagement.js";
import { isTelemetryDisabled, recordProductEvent } from "./track.js";

/** The product event a page view is recorded as. */
export const PAGE_VIEWED_EVENT = "page_viewed";

/** What an id-looking path segment becomes. */
export const ROUTE_ID_PLACEHOLDER = ":id";

/** Longest route reported; a longer one is cut. */
export const MAX_ROUTE_CHARS = 200;

/** Longest path segment kept as a route word. */
export const MAX_ROUTE_WORD_CHARS = 32;

/** Longest campaign value (utm_*, ref) a page view carries. */
export const MAX_CAMPAIGN_VALUE_CHARS = 128;

/** The campaign parameters a page view carries when the URL has them. */
export const CAMPAIGN_PARAMS = [
  "utm_source",
  "utm_medium",
  "utm_campaign",
  "utm_term",
  "utm_content",
] as const;

// Ad click ids by network. Only the network name is ever sent.
const CLICK_IDS: ReadonlyArray<readonly [string, string]> = [
  ["gclid", "google"],
  ["gbraid", "google"],
  ["wbraid", "google"],
  ["dclid", "google"],
  ["msclkid", "microsoft"],
  ["fbclid", "meta"],
  ["ttclid", "tiktok"],
  ["li_fat_id", "linkedin"],
  ["twclid", "other"],
  ["epik", "other"],
  ["rdt_cid", "other"],
  ["sccid", "other"],
  ["yclid", "other"],
];

// A route word: lowercase letters, single `-` / `_` joins. No digits: a digit
// is the commonest mark of an id.
const ROUTE_WORD_RE = /^[a-z]+(?:[-_][a-z]+)*$/;
// An API-style version segment.
const VERSION_RE = /^v\d{1,2}$/;
// What a route TABLE writes for a parameter, accepted from routeName only.
const PLACEHOLDER_RE =
  /^(?::[A-Za-z_][A-Za-z0-9_]{0,31}|\[{1,2}(?:\.\.\.)?[A-Za-z_][A-Za-z0-9_]{0,31}\]{1,2}|\*)$/;

function keepSegment(segment: string, placeholders: boolean): boolean {
  if (segment.length > MAX_ROUTE_WORD_CHARS) {
    return false;
  }
  return (
    ROUTE_WORD_RE.test(segment) ||
    VERSION_RE.test(segment) ||
    (placeholders && PLACEHOLDER_RE.test(segment))
  );
}

// The path without query and fragment, every segment either kept or `:id`,
// no empty segments, capped. Always starts with `/`.
function scrubRoute(path: string, placeholders: boolean): string {
  const cut = path.search(/[?#]/);
  const bare = cut === -1 ? path : path.slice(0, cut);
  const segments = bare
    .split("/")
    .filter((s) => s !== "")
    .map((s) => (keepSegment(s, placeholders) ? s : ROUTE_ID_PLACEHOLDER));
  return `/${segments.join("/")}`.slice(0, MAX_ROUTE_CHARS);
}

/**
 * The route a pathname is reported as: without query or fragment, without
 * the basePath prefix, with every segment that is not a plain route word
 * replaced by `:id` (see the header), no trailing slash and no empty
 * segments. Always starts with `/`. Pure; exported so an app can name its
 * own events' routes the same way.
 *
 *   normalizeRoute("/orders/8812?tab=items")        -> "/orders/:id"
 *   normalizeRoute("/admin/billing/", "/admin")     -> "/billing"
 *   normalizeRoute("/share/aB3xY_9k/edit")          -> "/share/:id/edit"
 */
export function normalizeRoute(pathname: string, basePath = ""): string {
  let path = typeof pathname === "string" ? pathname : "";
  const cut = path.search(/[?#]/);
  if (cut !== -1) {
    path = path.slice(0, cut);
  }
  const base = basePath.startsWith("/") ? normalizeBasePath(basePath) : "";
  if (base && (path === base || path.startsWith(`${base}/`))) {
    path = path.slice(base.length);
  }
  return scrubRoute(path, false);
}

function campaignValue(value: string | null): string | undefined {
  if (typeof value !== "string") {
    return undefined;
  }
  // eslint-disable-next-line no-control-regex
  const v = value.replace(/[\u0000-\u001f\u007f]/g, "").trim();
  if (v === "" || v.includes("@")) {
    return undefined;
  }
  return v.slice(0, MAX_CAMPAIGN_VALUE_CHARS);
}

/**
 * The campaign props of a page view from the URL's query string: utm_*,
 * `ref` (`?ref=` or `?source=`) and `click_id` (the ad network, never the
 * id). Only what is present. Pure; exported for tests.
 */
export function campaignProps(search: string): Record<string, string> {
  const out: Record<string, string> = {};
  let params: URLSearchParams;
  try {
    params = new URLSearchParams(search);
  } catch {
    return out;
  }
  for (const key of CAMPAIGN_PARAMS) {
    const v = campaignValue(params.get(key));
    if (v) {
      out[key] = v;
    }
  }
  const ref =
    campaignValue(params.get("ref")) ?? campaignValue(params.get("source"));
  if (ref) {
    out.ref = ref;
  }
  const lower = new Set([...params.keys()].map((k) => k.toLowerCase()));
  for (const [param, network] of CLICK_IDS) {
    if (lower.has(param)) {
      out.click_id = network;
      break;
    }
  }
  return out;
}

// A host without a leading `www.`, lowercased: `www.example.com` and
// `example.com` are one site.
function bareHost(host: string): string {
  const h = host.toLowerCase();
  return h.startsWith("www.") ? h.slice(4) : h;
}

/**
 * The referrer props of an entry page view: the referrer's host and scrubbed
 * path, or nothing when there is no referrer or it is this site (`ownHost`
 * or one of `siteHosts`, `www.` ignored). Pure; exported for tests.
 */
export function referrerProps(
  referrer: string,
  ownHost: string,
  siteHosts: readonly string[] = [],
): Record<string, string> {
  if (typeof referrer !== "string" || referrer === "") {
    return {};
  }
  let url: URL;
  try {
    url = new URL(referrer);
  } catch {
    return {};
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    return {};
  }
  const host = bareHost(url.host);
  const own = [ownHost, ...siteHosts].map(bareHost);
  if (host === "" || own.includes(host)) {
    return {};
  }
  return {
    referrer_host: url.host.toLowerCase(),
    referrer_path: scrubRoute(url.pathname, false),
  };
}

/** How installPageViews names and reports routes. */
export interface PageViewOptions {
  /**
   * The frontend's mount prefix (`/admin`), stripped from every route. A
   * value that does not start with `/` (Vite's relative `./`) is ignored.
   */
  basePath?: string;
  /**
   * Names the route for a pathname (query and fragment already cut, basePath
   * still on), replacing normalizeRoute — an app with a route table maps
   * `/users/jane` to `/users/[id]` itself. Return null or "" to report
   * nothing for that pathname. The name is scrubbed like a pathname before
   * it is sent (query and fragment cut, every segment not a route word or a
   * parameter placeholder replaced by `:id`), so a name built from the
   * pathname cannot carry an id out.
   */
  routeName?: (pathname: string) => string | null | undefined;
  /**
   * Hosts that are this site besides the page's own (another frontend of the
   * same app, an auth domain): a referrer from one of them is not an
   * acquisition source. `www.` is ignored either way.
   */
  siteHosts?: string[];
  /**
   * Record `page_engaged {route, view_id, engaged_ms, scroll_pct}` once per page view
   * (page-engagement.ts): visible, focused, active time and scroll depth.
   * Default true wherever page views are recorded; false turns it off alone.
   */
  engagement?: boolean;
}

/** What telemetry.ts installs: the route watch, with or without the events. */
export interface RouteWatchOptions extends PageViewOptions {
  /** Record `page_viewed {route}` for each new route. */
  pageViews: boolean;
  /**
   * Called with each new route and the one before it (null at install),
   * after its page view is recorded. Never throws into the app.
   */
  onRoute?: (route: string, previous: string | null) => void;
}

/** The slice of the Navigation API used here. */
interface NavigationLike {
  currentEntry?: { url?: string | null } | null;
  addEventListener(type: string, listener: () => void): void;
  removeEventListener(type: string, listener: () => void): void;
}

type HistoryMethod = History["pushState"];

/**
 * Watch the page's route and report each new one: a `page_viewed` event when
 * `pageViews` is on, and `onRoute` either way. Reports the current route at
 * once. Returns the function that removes every hook it installed; a wrapper
 * someone else has since wrapped stays in place, inert. A no-op without a
 * DOM. Called by initBrowserTelemetry; an app has no reason to call it.
 */
export function installPageViews(options: RouteWatchOptions): () => void {
  if (typeof window === "undefined" || isTelemetryDisabled()) {
    return () => {};
  }
  const basePath = options.basePath ?? "";
  let last: string | null = null;
  let active = true;
  // Engaged time rides with page views; RouteWatchOptions leaves it off
  // unless asked, so a route watch kept only for the session identity
  // reports nothing.
  const engagement =
    options.pageViews && options.engagement === true
      ? installPageEngagement()
      : null;

  const routeOf = (pathname: string): string | null => {
    if (options.routeName) {
      const cut = pathname.search(/[?#]/);
      const named = options.routeName(
        cut === -1 ? pathname : pathname.slice(0, cut),
      );
      return typeof named === "string" && named !== ""
        ? scrubRoute(named, true)
        : null;
    }
    return normalizeRoute(pathname, basePath);
  };

  const report = (pathname: string | undefined) => {
    if (!active || isTelemetryDisabled() || typeof pathname !== "string") {
      return;
    }
    try {
      const route = routeOf(pathname);
      if (route === null || route === last) {
        return;
      }
      const previous = last;
      last = route;
      if (options.pageViews) {
        recordProductEvent(
          PAGE_VIEWED_EVENT,
          viewProps(route, previous === null),
        );
      }
      engagement?.view(route, previous === null);
      options.onRoute?.(route, previous);
    } catch {
      // Telemetry must never break the app.
    }
  };

  // The view's props: the route, plus the visit's source (see the header).
  const viewProps = (route: string, entry: boolean): Record<string, string> => {
    const props: Record<string, string> = { route };
    if (entry) {
      props.entry = "true";
      Object.assign(
        props,
        referrerProps(
          typeof document !== "undefined" ? document.referrer : "",
          window.location?.host ?? "",
          options.siteHosts,
        ),
      );
    }
    Object.assign(props, campaignProps(window.location?.search ?? ""));
    return props;
  };

  const nav = navigationApi();
  const stops: Array<() => void> = [];
  if (nav) {
    const onEntry = () => report(entryPathname(nav) ?? currentPathname());
    nav.addEventListener("currententrychange", onEntry);
    stops.push(() => nav.removeEventListener("currententrychange", onEntry));
  } else {
    const history = window.history as History | undefined;
    if (history) {
      for (const method of ["pushState", "replaceState"] as const) {
        const original = history[method];
        if (typeof original !== "function") {
          continue;
        }
        const wrapped: HistoryMethod = function (this: History, ...args) {
          const result = original.apply(this, args);
          report(currentPathname());
          return result;
        };
        history[method] = wrapped;
        stops.push(() => {
          if (history[method] === wrapped) {
            history[method] = original;
          }
        });
      }
    }
    if (typeof window.addEventListener === "function") {
      const onPop = () => report(currentPathname());
      window.addEventListener("popstate", onPop);
      stops.push(() => window.removeEventListener?.("popstate", onPop));
    }
  }

  if (engagement) {
    stops.push(() => engagement.stop());
  }

  report(currentPathname());

  return () => {
    active = false;
    for (const stop of stops) {
      try {
        stop();
      } catch {
        // Telemetry must never break the app.
      }
    }
  };
}

function navigationApi(): NavigationLike | null {
  const nav = (window as unknown as { navigation?: NavigationLike }).navigation;
  return nav && typeof nav.addEventListener === "function" ? nav : null;
}

function entryPathname(nav: NavigationLike): string | undefined {
  const url = nav.currentEntry?.url;
  if (!url) {
    return undefined;
  }
  try {
    return new URL(url).pathname;
  } catch {
    return undefined;
  }
}

function currentPathname(): string | undefined {
  const path = window.location?.pathname;
  return typeof path === "string" ? path : undefined;
}
