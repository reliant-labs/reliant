// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// WHERE THE BROWSER SENDS API TRAFFIC. src/lib/connect.ts (every RPC) and
// src/lib/auth/native-login.ts (sign-in, session, sign-out) both ask this
// module, so the two can never point at different places.
//
//   under `vite` (dev server) → this page's OWN origin, `<origin>/_api`.
//                               vite.config.ts proxies `/_api/*` to the API.
//   in a build                → the API URL the caller passes in: the runtime
//                               config (config.js), exactly as before. A
//                               built SPA is plain files, with no proxy.
//
// ── Why the dev loop goes through this origin ─────────────────────────
//
// `forge env up` runs the API on one port and this dev server on another.
// The API's own URL (http://localhost:<api port>) only works for a browser on
// the machine running the stack. Open the dev server any other way — a
// Reliant workspace preview URL (https://<port>-<id>.workspaces.reliantapi.com),
// a tunnel to the frontend port, a LAN or container IP — and "localhost" is
// the VIEWER's machine: every RPC and the sign-in POST fail, and Private
// Network Access blocks a public page calling a loopback address anyway.
// Through this origin the browser only ever needs the host it already loaded
// the page from. Same-origin requests also involve no CORS, and the session
// cookie is first-party to whatever host the page is on.
//
// ── Turning it off ────────────────────────────────────────────────────
//
// Start the dev server with FORGE_DEV_API_PROXY=off and the browser calls the
// configured API URL directly (cross-origin, so CORS and
// `credentials: "include"` apply), as it did before. The proxy is also off
// when the dev server has no upstream to forward to: VITE_API_URL unset,
// which `forge env up` never leaves it.
//
// vite.config.ts makes that decision once, when the dev server starts, and
// defines the answer here as import.meta.env.FORGE_DEV_API_PROXY: the proxied
// path, or nothing when there is no proxy. Reading its answer rather than
// re-deriving it keeps the proxy and the transport from ever disagreeing.

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
