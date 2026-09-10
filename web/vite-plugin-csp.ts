import { createHash } from "node:crypto";
import type { Plugin } from "vite";

/**
 * Emits the app's Content-Security-Policy as a `<meta>` tag at build time.
 *
 * ── Why a meta tag and not a response header ──────────────────────────
 *
 * A header would be the textbook answer, and it is not available to us: the
 * SPA is deployed through forge's `FirebaseHosting` schema (forge kcl/schema.k
 * :1851), which passes `rewrites` through to firebase.json but has NO `headers`
 * field. There is therefore no declarative way to attach a header to the hosted
 * app, and the packaged desktop app is served by our own `app://` handler,
 * which would need its own copy of the same policy.
 *
 * A meta tag ships INSIDE the bundle, so one artifact carries its own policy to
 * both surfaces. That is the property that matters here: the hosted SPA and the
 * desktop renderer are the SAME bytes (control-plane's `reliant_web_vite_env`
 * feeds both), and a policy attached to only one of them would be exactly the
 * web/desktop drift this plugin exists to prevent.
 *
 * The cost is real and bounded: `frame-ancestors`, `report-uri` and `sandbox`
 * are ignored in a meta tag. Clickjacking is covered instead by
 * `X-Frame-Options: DENY`, already set by the Go server (internal/grpc/
 * server.go securityHeaders) and irrelevant to `app://`, which cannot be framed.
 *
 * ── Build-only, deliberately ──────────────────────────────────────────
 *
 * `apply: "build"`. Vite's dev server needs `unsafe-eval` for HMR and opens a
 * websocket to a random port; a policy loose enough for dev would be loose
 * enough to be worthless, and one tight enough for prod would break `npm run
 * dev`. Dev is not the artifact anyone ships, so it keeps its current
 * behaviour and the gate lives where the risk is.
 *
 * ── The inline bootstrap is hashed, never `unsafe-inline` ─────────────
 *
 * index.html runs one inline script before React to apply the saved theme —
 * without it every cold load flashes the wrong colours. Allowing it via
 * `unsafe-inline` would readmit every injected inline script and leave
 * `script-src` doing nothing, so the hash is computed HERE, from the final
 * HTML, on every build. It cannot drift from the script it authorises: edit
 * the bootstrap and the next build emits the matching hash automatically.
 * A hardcoded hash in a doc somewhere is the failure mode this avoids.
 */

/** Origins Stripe.js needs: its own script, and the iframes it mounts. */
const STRIPE_SCRIPT = "https://js.stripe.com";
const STRIPE_FRAMES = ["https://js.stripe.com", "https://hooks.stripe.com"];

/**
 * Monaco is loaded from jsDelivr by `web/src/lib/monacoManager.ts`, which
 * points the AMD loader at `cdn.jsdelivr.net/npm/monaco-editor@…/min/vs`.
 *
 * NOTE the loader probes `new Function("true")` inside a try/catch to decide
 * whether it may eval; under this policy that probe throws, it caches
 * `_canUseEval = false`, and falls back to `importScripts`. So Monaco works
 * WITHOUT `unsafe-eval` — verified against the actual v0.52.2 loader bytes.
 * Do not add `unsafe-eval` to "fix" Monaco; it is not broken.
 */
const MONACO_CDN = "https://cdn.jsdelivr.net";

/**
 * Statsig's SDK resolves these at runtime from inside the vendored client, so
 * they appear in the built bundle rather than in our source. Confirmed by
 * grepping dist/assets for external hosts — which is also why they are listed
 * explicitly instead of trusted to a wildcard.
 */
const STATSIG_HOSTS = [
  "https://statsigapi.net",
  "https://featureassets.org",
  "https://prodregistryv2.org",
  "https://api.statsigcdn.com",
];

/** Icon CDNs used by the MCP server avatars (`Settings/mcpAvatar.tsx`). */
const ICON_HOSTS = ["https://cdn.simpleicons.org", "https://cdn.jsdelivr.net"];

/**
 * Loopback, with any port, for BOTH schemes and websockets.
 *
 * Three separate features need this and none of them can name a fixed port:
 *
 *   1. The packaged desktop renderer dials its local daemon at
 *      `window.RELIANT_CONFIG.grpcUrl` — an ephemeral port chosen at launch.
 *   2. `reliant auth serve` bridges the OAuth loopback callback on :19284 for
 *      browser users (web/src/lib/oauth-local.ts).
 *   3. The interactive terminal is a raw WebSocket to the API server.
 *
 * A port-pinned entry would break (1) on the next launch, which is the kind of
 * failure that only shows up in a packaged build on someone else's machine.
 */
const LOOPBACK = [
  "http://localhost:*",
  "http://127.0.0.1:*",
  "ws://localhost:*",
  "ws://127.0.0.1:*",
];

/** Extract an origin, or "" if the value is absent or unparseable. */
function originOf(value: string | undefined): string {
  if (!value) return "";
  try {
    return new URL(value).origin;
  } catch {
    return "";
  }
}

/**
 * A Sentry DSN is `https://<key>@<host>/<project>`; the browser POSTs envelopes
 * to that host's origin. `new URL` keeps the credential in `origin`'s host
 * position, so parse and rebuild rather than string-slicing.
 */
function sentryOrigin(dsn: string | undefined): string {
  if (!dsn) return "";
  try {
    const { protocol, host } = new URL(dsn);
    return `${protocol}//${host.split("@").pop()}`;
  } catch {
    return "";
  }
}

function unique(values: string[]): string[] {
  return [...new Set(values.filter(Boolean))];
}

/**
 * Build the policy from the same VITE_* environment the bundle is compiled
 * with, so the backends the app is allowed to call are exactly the backends it
 * was built to call. Hardcoding prod hostnames here would be a second endpoint
 * table beside control-plane's KCL — the copy that drifts.
 */
export function buildCSP(
  env: NodeJS.ProcessEnv,
  scriptHashes: string[],
): string {
  const apiOrigins = unique([
    originOf(env.VITE_API_URL),
    originOf(env.VITE_GRPC_URL),
    originOf(env.VITE_CONTROL_PLANE_API_URL),
    originOf(env.VITE_GATEWAY_URL),
    originOf(env.VITE_SUPABASE_URL),
    originOf(env.VITE_OTEL_ENDPOINT),
    originOf(env.VITE_APP_URL),
  ]);

  // wss:// for every https backend: the terminal WebSocket rides the API
  // origin, and `connect-src` matches the scheme, so an https entry alone
  // does NOT authorise the upgrade.
  const secureSockets = apiOrigins
    .filter((origin) => origin.startsWith("https://"))
    .map((origin) => origin.replace("https://", "wss://"));

  const hashes = scriptHashes.map((hash) => `'${hash}'`);

  const directives: Record<string, string[]> = {
    "default-src": ["'self'"],

    // blob: is required for Monaco's web workers, which it instantiates from
    // generated blob URLs rather than files.
    "script-src": ["'self'", ...hashes, STRIPE_SCRIPT, MONACO_CDN, "https://api.statsigcdn.com", "blob:"],

    // 'unsafe-inline' is unavoidable for styles and is NOT the risk that
    // 'unsafe-inline' poses for scripts: Monaco injects <style> elements at
    // runtime, and every CSS-variable theme write sets an inline style. The
    // script side stays strict, which is where injection actually lands.
    "style-src": ["'self'", "'unsafe-inline'", MONACO_CDN],

    "font-src": ["'self'", "data:", MONACO_CDN],

    // https: broadly, because avatars and MCP server icons are user- and
    // registry-supplied URLs we cannot enumerate. Images are the lowest-risk
    // place to be permissive; scripts and connections are not.
    "img-src": ["'self'", "data:", "blob:", "https:"],

    "connect-src": unique([
      "'self'",
      ...apiOrigins,
      ...secureSockets,
      ...LOOPBACK,
      ...STATSIG_HOSTS,
      MONACO_CDN,
      sentryOrigin(env.VITE_SENTRY_DSN),
      // Sentry tunnels envelopes to the project's ingest subdomain; the DSN
      // above names it exactly, but session-replay can address a sibling.
      "https://*.ingest.us.sentry.io",
      "https://*.ingest.sentry.io",
      ...ICON_HOSTS,
    ]),

    "worker-src": ["'self'", "blob:"],
    "child-src": ["'self'", "blob:"],
    "frame-src": ["'self'", ...STRIPE_FRAMES],

    // Hard denials. object-src kills the plugin vector outright; base-uri
    // stops an injected <base> from re-pointing every relative asset URL.
    "object-src": ["'none'"],
    "base-uri": ["'self'"],
    "form-action": ["'self'", ...STRIPE_FRAMES],
  };

  return Object.entries(directives)
    .map(([name, values]) => `${name} ${values.join(" ")}`)
    .join("; ");
}

/** Every inline `<script>` body in the document, in source order. */
export function inlineScripts(html: string): string[] {
  // Deliberately does not match `<script src=…>`: those are external and are
  // authorised by origin, not by hash.
  const pattern = /<script(?![^>]*\bsrc=)[^>]*>([\s\S]*?)<\/script>/g;
  return [...html.matchAll(pattern)].map((match) => match[1]);
}

export function sha256(source: string): string {
  return `sha256-${createHash("sha256").update(source, "utf8").digest("base64")}`;
}

export function cspPlugin(): Plugin {
  return {
    name: "reliant:csp",
    apply: "build",
    transformIndexHtml: {
      // `post` so the hash covers the FINAL document. Vite's own plugins
      // inject and rewrite script tags during this hook; hashing earlier
      // would authorise a version of the bootstrap that never ships and
      // white-screen the app on load.
      order: "post",
      handler(html) {
        const hashes = inlineScripts(html).map(sha256);
        const policy = buildCSP(process.env, hashes);

        return {
          html,
          tags: [
            {
              tag: "meta",
              attrs: {
                "http-equiv": "Content-Security-Policy",
                content: policy,
              },
              injectTo: "head-prepend",
            },
          ],
        };
      },
    },
  };
}
