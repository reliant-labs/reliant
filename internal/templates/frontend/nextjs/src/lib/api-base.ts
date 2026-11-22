// yours: scaffolded once, never touched again — forge will not overwrite this file
// Where the browser sends API traffic, for RPCs (connect.ts) and sign-in (native-login.ts) alike.
// Dev server: this page's own origin at `/_api` (next.config.ts rewrites it), so a preview URL, tunnel or LAN IP
// works with no CORS and a first-party cookie. A build: the runtime config's API URL (`/_api` on
// Reliant hosting). FORGE_DEV_API_PROXY=off calls the API directly. Depth: `frontend/serving`.

import { BASE_PATH } from "./basepath_gen";

/** The same-origin path `next dev` proxies to the API, or "" when it does not. */
const DEV_API_PROXY: string =
  process.env.NODE_ENV === "development" ? (process.env.FORGE_DEV_API_PROXY ?? "") : "";

/** What the choice depends on, passed in so each case can be tested by name. */
export interface ApiBaseInputs {
  /** The path `next dev` proxies to the API (`/_api`); "" when there is no proxy. */
  devProxyPath: string;
  /** This page's origin; "" when there is no window (server rendering). */
  origin: string;
  /** The frontend's mount prefix (src/lib/basepath_gen.ts); "" at the host root. */
  basePath: string;
  /** The API URL the environment declares, after the caller's own fallbacks. */
  configured: string;
}

/**
 * chooseApiBase is the decision: this page's own origin while the dev server
 * proxies the API, the configured URL otherwise. Never a trailing slash.
 */
export function chooseApiBase({
  devProxyPath,
  origin,
  basePath,
  configured,
}: ApiBaseInputs): string {
  if (devProxyPath) {
    return `${origin}${basePath}${devProxyPath}`;
  }
  return configured.replace(/\/+$/, "");
}

/**
 * apiBase returns the base URL for API requests from this page, given the
 * API URL the environment configures.
 */
export function apiBase(configured: string): string {
  return chooseApiBase({
    devProxyPath: DEV_API_PROXY,
    origin: typeof window === "undefined" ? "" : window.location.origin,
    basePath: BASE_PATH,
    configured,
  });
}

// ── Named APIs ──────────────────────────────────────────────────────────
//
// A frontend that calls more than one backend names the others in KCL
// (`Frontend.apis = {billing = "billing-api"}`), and each is served on this
// page's own origin too, at `/_api/@<name>`, so the session cookie stays
// first-party for every one of them. Dev server: proxied like `/_api`. A
// build: config.js's `apis.<name>` (`/_api/@<name>` on Reliant hosting, the
// workload's own URL where nothing serves the route). Depth: `frontend/serving`.
//
//     createConnectTransport({ baseUrl: apiBaseFor("billing"), credentials: "include" })

/** chooseApiBaseFor is chooseApiBase for the named API `name`. */
export function chooseApiBaseFor(name: string, inputs: ApiBaseInputs): string {
  if (inputs.devProxyPath) {
    return `${inputs.origin}${inputs.basePath}${inputs.devProxyPath}/@${name}`;
  }
  return inputs.configured.replace(/\/+$/, "");
}

/** The named APIs this environment's config.js publishes: name -> base URL. */
function configuredApis(): Record<string, unknown> {
  if (typeof window === "undefined") return {};
  const apis = (window as { __FORGE_CONFIG__?: { apis?: unknown } }).__FORGE_CONFIG__?.apis;
  return apis !== null && typeof apis === "object" ? (apis as Record<string, unknown>) : {};
}

/**
 * apiBaseFor returns the base URL for requests to the named API `name`. It
 * throws when neither the dev server nor config.js knows the name — a typo,
 * or an environment that does not declare it in Frontend.apis — rather than
 * sending the request to the default API.
 */
export function apiBaseFor(name: string): string {
  const configured = configuredApis()[name];
  if (!DEV_API_PROXY && typeof configured !== "string") {
    throw new Error(
      `[forge] no API named "${name}" in this environment: declare it in the frontend's Frontend.apis`,
    );
  }
  return chooseApiBaseFor(name, {
    devProxyPath: DEV_API_PROXY,
    origin: typeof window === "undefined" ? "" : window.location.origin,
    basePath: BASE_PATH,
    configured: typeof configured === "string" ? configured : "",
  });
}
