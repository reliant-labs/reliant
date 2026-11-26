// yours: scaffolded once, never touched again — forge will not overwrite this file
// Where the browser sends API traffic, for RPCs (connect.ts) and sign-in (native-login.ts) alike.
// Dev server: this page's own origin at `/_api` (vite.config.ts proxies it), so a preview URL, tunnel or LAN IP
// works with no CORS and a first-party cookie. A build: the runtime config's API URL (`/_api` on
// Reliant hosting). FORGE_DEV_API_PROXY=off calls the API directly. Depth: `frontend/serving`.

/** The same-origin path the dev server proxies to the API, or "" when it does not. */
const DEV_API_PROXY: string = import.meta.env.DEV
  ? ((import.meta.env.FORGE_DEV_API_PROXY as string | undefined) ?? "")
  : "";

/** What the choice depends on, passed in so each case can be tested by name. */
export interface ApiBaseInputs {
  /** The path the dev server proxies to the API (`/_api`); "" when there is no proxy. */
  devProxyPath: string;
  /** This page's origin; "" when there is no window. */
  origin: string;
  /** The frontend's mount prefix; "" at the host root (this scaffold's shape). */
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
    basePath: "",
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
    basePath: "",
    configured: typeof configured === "string" ? configured : "",
  });
}

// ── Credentials ─────────────────────────────────────────────────────────
//
// Which cookies a request to the API carries: the session cookie, when there
// is one, must reach the API wherever it is, and nothing else may break.
//
//   same origin   "same-origin". The browser sends the cookie, and a page the
//                 platform serves in an OPAQUE origin — an unclaimed preview
//                 on reliantpreview.dev is sandboxed, so its own /_api is
//                 cross-origin to it — still gets its answer: the platform
//                 grants that page `Access-Control-Allow-Origin: *`, and a
//                 browser refuses a wildcard grant only to a request that
//                 sent credentials ("include").
//   other origin  "include". An API on its own host (a literal API_URL, or
//                 the dev loop with FORGE_DEV_API_PROXY=off): the browser's
//                 default would silently drop the session cookie, sign-in
//                 would answer 200 and every call after it 401. The API pairs
//                 this with cors_allow_credentials.

/**
 * credentialsFor is the fetch credentials mode for a request to `url` from a
 * page at `pageOrigin`: "same-origin" on the page's own origin, "include" on
 * any other.
 */
export function credentialsFor(url: string, pageOrigin: string): RequestCredentials {
  if (!pageOrigin) {
    return "include";
  }
  try {
    return new URL(url, pageOrigin).origin === pageOrigin ? "same-origin" : "include";
  } catch {
    return "include";
  }
}

/** apiCredentials is credentialsFor from this page, for a fetch's `input`. */
export function apiCredentials(input: RequestInfo | URL): RequestCredentials {
  const url = input instanceof Request ? input.url : String(input);
  return credentialsFor(url, typeof window === "undefined" ? "" : window.location.origin);
}
