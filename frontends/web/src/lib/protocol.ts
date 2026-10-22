/**
 * Centralized protocol detection for API URLs
 * 
 * Priority:
 * 1. In Electron: use RELIANT_CONFIG.useTLS from backend
 * 2. In browser: use VITE_DISABLE_TLS env var
 * 
 * This matches the backend's DISABLE_TLS pattern.
 */

/**
 * Check if TLS is disabled (use http instead of https)
 */
export const isTLSDisabled = (): boolean => {
  // In Electron, use the config from backend
  if (typeof window !== "undefined" && window.RELIANT_CONFIG?.isElectron) {
    return window.RELIANT_CONFIG.useTLS === false;
  }
  // In browser dev/prod, use env var
  return import.meta.env.VITE_DISABLE_TLS === "true";
};

/**
 * Get the appropriate protocol (http or https)
 */
export const getProtocol = (): "http" | "https" => {
  return isTLSDisabled() ? "http" : "https";
};

/**
 * Get the appropriate WebSocket protocol (ws or wss)
 */
export const getWsProtocol = (): "ws" | "wss" => {
  return isTLSDisabled() ? "ws" : "wss";
};

/**
 * Build a localhost URL with the correct protocol
 */
export const buildLocalhostUrl = (port: string | number): string => {
  return `${getProtocol()}://localhost:${port}`;
};

/**
 * The scheme the packaged desktop renderer is served from.
 *
 * `app://bundle` (see electron/src/app-protocol.js). Packaged builds used
 * `file://` up to and including v1.6.3; `loadFile()` gave the page a `file://`
 * origin, where the bundle's root-absolute asset paths resolved against the
 * FILESYSTEM root and nothing loaded. v1.7.0 moved to a real origin.
 */
const PACKAGED_SCHEMES = ["app:", "file:"] as const;

/**
 * True when this renderer is the PACKAGED desktop app rather than a browser or
 * a Vite dev server.
 *
 * This is the single discriminator for "no dev-server proxy, and the backend
 * URL arrives asynchronously from preload.js". It exists because that question
 * was previously asked as `window.location.protocol === "file:"` in seven
 * different places. When the packaged renderer moved from `file://` to
 * `app://bundle`, every one of those checks silently flipped to its
 * browser-shaped branch — the packaged app started reporting that its config
 * was ready before preload had injected any, and handed `app://bundle` to
 * identity providers as an OAuth redirect target.
 *
 * Both schemes are matched so the historical `file://` build keeps working, and
 * so the next scheme change is one edit here rather than another silent drift.
 */
export const isPackagedRendererOrigin = (): boolean => {
  if (typeof window === "undefined") return false;
  return (PACKAGED_SCHEMES as readonly string[]).includes(
    window.location.protocol,
  );
};

/**
 * Whether Connect transports should use a SAME-ORIGIN (relative) baseUrl so the
 * Vite dev-server proxy fans every RPC out to its backend (`/reliant.v1.*` →
 * reliant-api, `/controlplane.v1.*` → admin-server). This is the SINGLE root
 * reason web-dev AND electron-dev are CORS-free: the renderer is served over
 * http(s) by Vite, so every RPC is first-party and the proxy (vite.config.ts)
 * does the host routing — no absolute cross-origin backend URLs, no per-port
 * CORS bookkeeping, no random-electron-port allow-listing.
 *
 * Packaged Electron is the ONLY exception: it serves the bundle from its own
 * scheme (`app://bundle`) and there is no dev-server proxy, so it must dial the
 * local daemon at the absolute `window.RELIANT_CONFIG.grpcUrl`. That is the
 * same discriminator `lib/configReady.ts` uses to decide whether to block on
 * the async RELIANT_CONFIG injection — so the whole renderer stays consistent:
 * http-served ⇒ browser-like (same-origin proxy); packaged ⇒ packaged daemon.
 */
export const isSameOriginTransport = (): boolean => {
  if (typeof window === "undefined") return false;
  // Same-origin (relative) baseUrl ONLY works where a dev-server PROXY is present
  // to fan RPCs out to their backends — i.e. the Vite dev server (web-dev +
  // electron-dev), where import.meta.env.DEV is true. A PRODUCTION build
  // (Firebase-hosted app.reliantlabs.io / *.web.app) has NO proxy, so a
  // same-origin RPC hits the SPA's own index.html → "unsupported content type
  // text/html" and the app hangs. Prod builds must dial the absolute per-env
  // backend URLs (VITE_API_URL / VITE_CONTROL_PLANE_API_URL). Packaged Electron
  // (a prod build) is excluded by both checks and uses RELIANT_CONFIG.
  return import.meta.env.DEV && !isPackagedRendererOrigin();
};
