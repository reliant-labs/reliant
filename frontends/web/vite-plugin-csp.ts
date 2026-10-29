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
 * desktop renderer are the SAME bytes, and a policy attached to only one of
 * them would be exactly the web/desktop drift this plugin exists to prevent.
 *
 * ── connect-src is narrowed at RUNTIME, from config.js ────────────────
 *
 * The backends the app may call are the backends its runtime config names
 * (window.__FORGE_CONFIG__, src/lib/config_gen.ts), and those are not known
 * at build time: one bundle serves every environment. So there are TWO
 * policies, and a browser enforces every policy it is given:
 *
 *   1. the build-time meta below: every directive, with connect-src at a
 *      ceiling (`* ws: wss:`, any http/https/ws/wss origin);
 *   2. a meta the inline bootstrap after `<script src=config.js>` appends,
 *      whose connect-src is exactly 'self', the origins config.js names, and
 *      the fixed hosts below (runtimeConnectScript).
 *
 * The effective connect-src is their intersection, i.e. policy 2. The
 * bootstrap runs in <head>, before any module script, so no request the app
 * makes predates it. It is hashed like the theme bootstrap, from the final
 * HTML. A missing config.js narrows to 'self' plus the fixed hosts, the same
 * policy a build with no endpoints got before.
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
 * Monaco is loaded from jsDelivr by `frontends/web/src/lib/monacoManager.ts`, which
 * points the AMD loader at `cdn.jsdelivr.net/npm/monaco-editor@…/min/vs`.
 *
 * NOTE the loader probes `new Function("true")` inside a try/catch to decide
 * whether it may eval; under this policy that probe throws, it caches
 * `_canUseEval = false`, and falls back to `importScripts`. So Monaco works
 * WITHOUT `unsafe-eval` — verified against the actual v0.52.2 loader bytes.
 * Do not add `unsafe-eval` to "fix" Monaco; it is not broken.
 */
const MONACO_CDN = "https://cdn.jsdelivr.net";

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

/**
 * The runtime-config keys whose values are origins the app calls. Their
 * names are config_gen.ts's (proto/reliant/config/v1/web_config.proto);
 * OTEL_ENDPOINT is normally the same-origin `/_otel`, which 'self' covers.
 */
export const RUNTIME_ORIGIN_KEYS = [
  "API_URL",
  "CONTROL_PLANE_API_URL",
  "GATEWAY_URL",
  "SUPABASE_URL",
  "OTEL_ENDPOINT",
  "APP_URL",
] as const;

/** connect-src hosts that are not per environment. */
const FIXED_CONNECT = [
  ...LOOPBACK,
  MONACO_CDN,
  ...ICON_HOSTS,
];

/**
 * The inline bootstrap that appends policy 2 (see the file header). Plain
 * ES5 with no dependencies: it runs before any module, from <head>.
 *
 * For each origin key it allows the http(s) origin, plus wss:// for an https one — the
 * terminal WebSocket rides the API origin, and connect-src matches on scheme,
 * so an https entry alone does NOT authorise the upgrade. Anything absent,
 * relative or unparseable contributes nothing rather than a junk token.
 */
export function runtimeConnectScript(): string {
  const keys = JSON.stringify(RUNTIME_ORIGIN_KEYS);
  const fixed = JSON.stringify(FIXED_CONNECT);
  return (
    "(function(){" +
    "var c=window.__FORGE_CONFIG__||{},s=[\"'self'\"],seen={};" +
    "function add(v){if(v&&!seen[v]){seen[v]=1;s.push(v);}}" +
    "function origin(v){if(typeof v!==\"string\"||!v)return\"\";" +
    "try{var u=new URL(v);return u.protocol===\"http:\"||u.protocol===\"https:\"?u.protocol+\"//\"+u.host:\"\";}" +
    "catch(e){return\"\";}}" +
    `var k=${keys},f=${fixed},i,o;` +
    "for(i=0;i<k.length;i++){o=origin(c[k[i]]);add(o);if(o.indexOf(\"https://\")===0)add(\"wss://\"+o.slice(8));}" +
    "for(i=0;i<f.length;i++)add(f[i]);" +
    "var m=document.createElement(\"meta\");m.httpEquiv=\"Content-Security-Policy\";" +
    "m.content=\"connect-src \"+s.join(\" \");document.head.appendChild(m);" +
    "})();"
  );
}

/**
 * The build-time policy (policy 1). Every directive is final except
 * connect-src, which is the ceiling runtimeConnectScript narrows. No
 * endpoint appears here: the build does not know which environment it is for.
 */
export function buildCSP(scriptHashes: string[]): string {
  const hashes = scriptHashes.map((hash) => `'${hash}'`);

  const directives: Record<string, string[]> = {
    "default-src": ["'self'"],

    // blob: is required for Monaco's web workers, which it instantiates from
    // generated blob URLs rather than files.
    "script-src": ["'self'", ...hashes, STRIPE_SCRIPT, MONACO_CDN, "blob:"],

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

    // The ceiling; the runtime policy names the actual hosts (file header).
    // `*` matches any http(s) origin, which a self-hosted deployment on a
    // plain-http hostname needs; ws:/wss: are spelled out because `*` does
    // not match a WebSocket scheme in every engine.
    "connect-src": ["'self'", "*", "ws:", "wss:"],

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

/** The runtime config document's tag in index.html (see index.html). */
const CONFIG_SCRIPT_TAG = /<script src="[^"]*config\.js"><\/script>/;

/**
 * Insert the runtime connect-src bootstrap right after config.js's tag: after
 * it so window.__FORGE_CONFIG__ is set, and before every module script.
 * A document without that tag is refused rather than shipped with only the
 * ceiling policy.
 */
export function withRuntimeConnectPolicy(html: string): string {
  if (!CONFIG_SCRIPT_TAG.test(html)) {
    throw new Error(
      'reliant:csp: index.html has no <script src="%BASE_URL%config.js"></script>; ' +
        "the runtime config and the runtime connect-src policy both depend on it",
    );
  }
  return html.replace(CONFIG_SCRIPT_TAG, (tag) => `${tag}\n    <script>${runtimeConnectScript()}</script>`);
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
      handler(source) {
        const html = withRuntimeConnectPolicy(source);
        const policy = buildCSP(inlineScripts(html).map(sha256));

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
