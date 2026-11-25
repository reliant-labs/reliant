// Which sign-in this page runs, and where the forge one talks to.
//
// Two providers sit behind one interface (lib/auth/use-auth.ts):
//
//   "supabase" (the default) — supabase-js holds the session and the transport
//     sends its access token as a bearer, cross-origin, to both APIs.
//   "forge" — the managed identity's login broker on reliant-api (/auth/*,
//     internal/app/login_broker.go) holds the session server-side. The browser
//     holds only an HttpOnly session cookie, so every request goes SAME-ORIGIN
//     (the cookie is first-party to this page's host), with
//     `credentials: "include"` and no Authorization header.
//
// The flag is the runtime config key AUTH_PROVIDER (ReliantWebConfig.auth_provider),
// so one build serves both and flipping it back is the rollback. The desktop
// app reads the same key from its config.js.
//
// ── The two same-origin API routes ─────────────────────────────────────
//
// The web calls two backends. forge's named site APIs (forge#2669) mount each
// under this page's origin: `/_api/@reliant` → reliant-api, `/_api/@cp` → the
// control plane's admin-server. The dev server proxies both (vite.config.ts),
// a hosted site's origin serves them once the control plane advertises
// `serves-named-site-apis`, and the desktop app's app:// handler answers them.
// config.js may name each base itself (`apis`, as forge's apiBaseFor reads it);
// otherwise it is this origin's `/_api/@<name>`.

import { loadConfig } from "@/lib/config_gen";

export type AuthProviderName = "supabase" | "forge";

/** The sign-in provider this page runs (config.js AUTH_PROVIDER). Unknown values are "supabase". */
export function authProviderName(): AuthProviderName {
  return loadConfig().AUTH_PROVIDER === "forge" ? "forge" : "supabase";
}

/** True when the forge login broker signs people in (cookie sessions, no bearer). */
export function isForgeAuth(): boolean {
  return authProviderName() === "forge";
}

/** The named same-origin APIs this page calls in forge mode. */
export type SiteApiName = "reliant" | "cp";

/** The same-origin path forge mounts a named API under. */
export function namedApiPath(name: SiteApiName): string {
  return `/_api/@${name}`;
}

function declaredApiBase(name: SiteApiName): string | undefined {
  if (typeof window === "undefined") return undefined;
  const apis = (window as { __FORGE_CONFIG__?: { apis?: unknown } }).__FORGE_CONFIG__?.apis;
  if (!apis || typeof apis !== "object") return undefined;
  const value = (apis as Record<string, unknown>)[name];
  return typeof value === "string" && value !== "" ? value : undefined;
}

/**
 * The base URL of a named API in forge mode, never with a trailing slash:
 * what config.js names for it, else this page's own `/_api/@<name>`.
 */
export function forgeApiBase(name: SiteApiName): string {
  const declared = declaredApiBase(name);
  const origin = typeof window === "undefined" ? "" : window.location.origin;
  if (declared) {
    // A path is on this origin; an absolute URL is used as given.
    return (declared.startsWith("/") ? `${origin}${declared}` : declared).replace(/\/+$/, "");
  }
  return `${origin}${namedApiPath(name)}`;
}

/** reliant-api's base in forge mode: Connect RPCs and the login broker (/auth/*). */
export function reliantForgeBase(): string {
  return forgeApiBase("reliant");
}

/** The control plane's base in forge mode: controlplane.v1 RPCs, /oauth/*, /api/proxy-session. */
export function controlPlaneForgeBase(): string {
  return forgeApiBase("cp");
}

/**
 * fetch with the session cookie. "include", not "same-origin": a base config.js
 * names on another origin would otherwise drop the cookie silently.
 */
export const credentialedFetch: typeof globalThis.fetch = (input, init) =>
  globalThis.fetch(input, { ...init, credentials: "include" });
