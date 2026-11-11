import { defineConfig, type Plugin, type ViteUserConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { forgeRuntimeConfig } from "@reliantlabs/forge-web-runtime/vite";
import os from "node:os";
import path from "path";
import { browserLogSink } from "./vite-plugin-browser-logs";
import { versionJson } from "./vite-plugin-version-json";
import { cspPlugin } from "./vite-plugin-csp";
import { speedscopeStatic } from "./vite-plugin-speedscope";
import { preloadGeistLatin } from "./vite-plugin-preload-fonts";
import { securityTxt } from "./vite-plugin-security-txt";
import { otelProxy } from "./vite-plugin-otel-proxy";
import { demoBundleGuard, demoSubstitutes } from "./vite-plugin-demo-bundle";

// Get worktree name from the checkout root
// When Vite runs, cwd is frontends/web, so the checkout is two levels up
function getWorktreeName(): string {
  try {
    const cwd = process.cwd();
    const checkoutDir = path.dirname(path.dirname(cwd));
    return path.basename(checkoutDir);
  } catch (error) {
    console.warn('[Vite] Failed to get worktree name:', error);
    return 'dev';
  }
}

// Vitest sizing. A jsdom worker is ~300MB and a core's worth of CPU, and this
// suite (~750 files) shares a machine with other agents' builds, so two knobs
// follow the host instead of assuming an idle box:
//  - workers: one per core, capped at 8 (jsdom workers are memory-heavy; vitest's
//    own default is one per core, 15 on a 16-core box).
//  - timeouts: the 5s default is for an idle machine. At load average 6x the core
//    count (25 tests hit "timed out in 5000ms" at load 97 on 16 cores) a timeout
//    says the host is busy, not that the code regressed. Scales 1x..6x with the
//    1-minute load per core.
//  - waits: Testing Library's findBy*/waitFor give up after a fixed 1s
//    (asyncUtilTimeout), whatever the host is doing. That is the same idle-box
//    assumption, one level down: on a loaded host, rendering a fixture-backed
//    view (an Observability table, a dialog fed by five mocked queries) is
//    honest CPU work that takes longer than 1s, and the test failed while
//    the element was on its way. A wait gets half its test's budget: a first
//    wait that spent all of it would time the test out anyway, and the other
//    half is the test's remaining steps. Only a wait that would otherwise
//    have FAILED is affected — one whose element arrives returns at once.
// VITEST_MAX_WORKERS / VITEST_TIMEOUT_MS / VITEST_ASYNC_UTIL_TIMEOUT_MS
// override them (CI, debugging a hang).
function intEnv(name: string): number | undefined {
  const n = Number.parseInt(process.env[name] ?? "", 10);
  return Number.isFinite(n) && n > 0 ? n : undefined;
}
const cores = os.availableParallelism();
// Workers also follow memory: a vmThreads worker recycles at VM_MEMORY_LIMIT
// but peaks a little above it (~1.1 GiB measured), and the sum of them must
// leave the host room for the vitest parent and whatever else runs beside it.
// So all workers together get at most half the host's memory (its cgroup
// limit when it has one): 7 workers on a 16-GiB runner, ~7.5 GiB at peak.
const VM_MEMORY_LIMIT = "1GB";
const WORKER_PEAK_BYTES = 1.1 * 1024 ** 3;
const constrained = process.constrainedMemory?.() ?? 0;
const hostMemory = constrained > 0 ? Math.min(constrained, os.totalmem()) : os.totalmem();
const memoryWorkers = Math.floor(hostMemory / 2 / WORKER_PEAK_BYTES);
const maxWorkers = intEnv("VITEST_MAX_WORKERS") ?? Math.max(2, Math.min(8, cores, memoryWorkers));
const loadFactor = Math.min(6, Math.max(1, Math.ceil(os.loadavg()[0] / cores)));
const testTimeout = intEnv("VITEST_TIMEOUT_MS") ?? 5_000 * loadFactor;
const asyncUtilTimeout = intEnv("VITEST_ASYNC_UTIL_TIMEOUT_MS") ?? Math.round(testTimeout / 2);

// Vitest pools. Most files run in `vmThreads`: each file gets a fresh VM
// context in a reused worker instead of a fresh jsdom environment in a fresh
// process, which halves the suite (full run at 8 workers, paired: 812s on
// forks, 373s here). A file that needs a real realm runs in `forks` instead.
// In a VM context `window.location` (and `window`) cannot be redefined, and a
// few platform checks see another realm's prototypes. Such a file fails there
// deterministically (`TypeError: Cannot redefine property: location`), and the
// fix is one line here.
const FORKS_POOL_FILES = [
  // Vite/esbuild requires TextEncoder and Uint8Array to share a real realm.
  "vite-demo-target.test.ts",
  // A real node:http server and Buffers, which need the real realm too.
  "vite-plugin-otel-proxy.test.ts",
  "src/__tests__/staleChunkUnloadLoop.test.tsx",
  "src/api/__tests__/transport.errorReporting.test.ts",
  "src/api/__tests__/transport.test.ts",
  "src/api/__tests__/transport.trace.test.ts",
  "src/api/__tests__/unauthInterceptor.signout.test.ts",
  "src/components/Auth/__tests__/CliLogin.test.tsx",
  "src/components/Auth/__tests__/DevicePairing.test.tsx",
  "src/components/Billing/__tests__/LinkIdentityModal.test.tsx",
  "src/components/Settings/__tests__/ConnectorConsent.defaults.test.tsx",
  "src/components/Settings/__tests__/IntegrationsSettings.test.tsx",
  "src/components/__tests__/AppOutOfDateModal.test.tsx",
  "src/components/__tests__/ConnectionOAuthCallback.test.tsx",
  "src/components/__tests__/GitHubOAuthCallback.desktopAndInstall.test.tsx",
  "src/components/__tests__/GitHubOAuthCallback.installReturn.test.tsx",
  "src/components/__tests__/NewBuildPrompt.test.tsx",
  "src/components/__tests__/UpgradeAccount.test.tsx",
  "src/components/workflow/connections/__tests__/ConnectIntegrationDialog.oauth.test.tsx",
  "src/lib/__tests__/appUrl.test.ts",
  "src/lib/__tests__/browser-log-forward.test.ts",
  "src/lib/__tests__/connect.routing.test.ts",
  "src/lib/__tests__/connection-oauth.test.ts",
  "src/lib/__tests__/github-connect.test.ts",
  "src/lib/__tests__/packagedOrigin.test.ts",
  "src/lib/cli-commands.test.ts",
  "src/services/controlPlane/observability/__tests__/rumApi.test.ts",
  "src/store/__tests__/authStore.claim-origin.test.ts",
  "src/store/__tests__/authStore.redirect-origin.test.ts",
  "src/store/__tests__/sidebarStore.test.ts",
  "src/store/__tests__/workspaceStateStore.test.ts",
];
// The default (this directory) plus @reliantlabs/sdk's own tests: the app
// compiles the SDK from source, so one `vitest run` judges both.
const TEST_INCLUDE = ["**/*.{test,spec}.?(c|m)[jt]s?(x)", "../../sdk/ts/src/**/*.test.{ts,tsx}"];
const TEST_EXCLUDE = [
  "**/node_modules/**",
  "**/dist/**",
  "**/e2e/**", // Exclude Playwright e2e tests
  "**/.{idea,git,cache,output,temp}/**",
  "**/{karma,rollup,webpack,vite,vitest,jest,ava,babel,nyc,cypress,tsup,build}.config.*",
];

// ── Browser telemetry (DEV ONLY) ────────────────────────────────────────
//
// otelProxy (vite-plugin-otel-proxy.ts) owns `/_otel`: it forwards a POST to
// /_otel/v1/{traces,logs,metrics} to OTEL_EXPORTER_OTLP_ENDPOINT, the base
// URL `forge env up` gives this dev server, with the stack's browser token
// from FORGE_OTEL_HEADERS_FILE (a HOSTED dev stack: observability.dev_telemetry,
// deploy/kcl/dev/main.k), and refuses everything else. With no upstream it
// answers an accepted request 204.
//
// WHETHER THE PAGE SENDS AT ALL is the dev server's decision too, because the
// page reads OTEL_ENDPOINT only from config.js (config_gen.ts), never from
// VITE_* build variables. When `forge env up` decides this stack's telemetry
// is local and there is no local store, it gives the dev server
// VITE_OTEL_ENDPOINT=off (forge#1950). otelBrowserConfig applies the dev
// server's endpoint to window.__FORGE_CONFIG__ right after config.js runs:
// VITE_OTEL_ENDPOINT when it is set ("off" included), otherwise "off" when
// there is no upstream to forward to, otherwise config.js's own value. So a
// page on a local fallback starts no exporter.
const OTEL_UPSTREAM = (process.env.OTEL_EXPORTER_OTLP_ENDPOINT ?? "").trim().replace(/\/+$/, "");
const DEV_BROWSER_OTEL_ENDPOINT = (process.env.VITE_OTEL_ENDPOINT ?? "").trim() || (OTEL_UPSTREAM ? "" : "off");
const otelBrowserConfig = (): Plugin => ({
  name: "reliant-otel-browser-config",
  apply: "serve",
  // Injected at the end of <head>, after the blocking config.js script and
  // before the deferred module bundle reads the config.
  transformIndexHtml() {
    if (!DEV_BROWSER_OTEL_ENDPOINT) return [];
    return [
      {
        tag: "script",
        injectTo: "head",
        children: `if (window.__FORGE_CONFIG__) window.__FORGE_CONFIG__.OTEL_ENDPOINT = ${JSON.stringify(DEV_BROWSER_OTEL_ENDPOINT)};`,
      },
    ];
  },
});

// https://vite.dev/config/
const appConfig: ViteUserConfig = {
  // browserLogSink is dev-only (apply: "serve"): it gives a plain browser tab
  // the file-backed logging that only the Electron build otherwise has. See
  // vite-plugin-browser-logs.ts.
  // versionJson bakes this bundle's release identity in as __RELIANT_BUILD__
  // (the About page, telemetry's service.version) and emits dist/version.json, a real
  // static file so the SPA rewrite cannot swallow a request for it. It FAILS a
  // production build that cannot name its version. Inactive under vitest. See
  // vite-plugin-version-json.ts.
  // cspPlugin is build-only (apply: "build"): it injects the Content-Security-
  // Policy as a <meta> tag so ONE bundle carries its own policy to both the
  // Firebase-hosted SPA and the packaged app:// renderer. forge's
  // FirebaseHosting schema has no `headers` field, so a response header is not
  // expressible; see vite-plugin-csp.ts.
  // speedscopeStatic serves speedscope's prebuilt viewer at /speedscope/ (dev)
  // and copies it into the build — the Profiles view iframes it, so none of it
  // enters the JS bundle. See vite-plugin-speedscope.ts.
  // forgeRuntimeConfig serves config.js from FORGE_RUNTIME_CONFIG_FILE when
  // the project that declares this frontend's environment rendered it outside
  // this tree (control-plane's stack: .forge/runtime-config/<fe>/config.js).
  // Unset, it does nothing and public/config.js (`forge generate`) is served.
  // preloadGeistLatin (build-only) preloads the default UI face's hashed
  // Latin file; see vite-plugin-preload-fonts.ts.
  // securityTxt (build-only) emits dist/.well-known/security.txt naming the
  // security and abuse contacts; see vite-plugin-security-txt.ts.
  // otelProxy and otelBrowserConfig (dev-only) are browser telemetry; see
  // "Browser telemetry" above.
  plugins: [react(), browserLogSink(), versionJson(), cspPlugin(), speedscopeStatic(), forgeRuntimeConfig(), otelProxy({ upstream: OTEL_UPSTREAM, headersFile: process.env.FORGE_OTEL_HEADERS_FILE ?? "" }), otelBrowserConfig(), preloadGeistLatin(), securityTxt()],
  // Load .env files from the checkout root instead of frontends/web
  envDir: path.resolve(__dirname, "../.."),
  // Define global constants available in the app
  define: {
    __WORKTREE_NAME__: JSON.stringify(getWorktreeName()),
    // Which build this is: the app, or the forge demo (demoConfig below).
    // lib/shell.ts reads it, so the demo bundle is the demo shell whatever
    // host serves it, and the app's build drops the check entirely.
    __WEB_TARGET__: JSON.stringify("app"),
  },
  resolve: {
    // @reliantlabs/sdk is a workspace member, built from source here: the app
    // never needs its dist/. Its generated protos ("#gen/*") resolve to this
    // app's own src/gen, so there is ONE copy of every message and service
    // descriptor in the bundle.
    alias: [
      { find: "@", replacement: path.resolve(__dirname, "./src") },
      { find: "@reliantlabs/sdk/client", replacement: path.resolve(__dirname, "../../sdk/ts/src/client/index.ts") },
      { find: "@reliantlabs/sdk/react", replacement: path.resolve(__dirname, "../../sdk/ts/src/react/index.ts") },
      { find: /^#gen\/(.*)\.js$/, replacement: path.resolve(__dirname, "./src/gen/$1") },
    ],
  },
  // Absolute base: the app is served at the domain root (app.reliantlabs.io) with
  // history routing + SPA fallback. Relative "./" breaks fresh loads/refreshes on
  // any 2+-segment route (e.g. /auth/github/callback) because "./assets/x.js"
  // resolves against the route's dir (/auth/github/) → 404 → index.html is served
  // as text/html → "Expected a JavaScript module" MIME error. "/" always resolves
  // assets to /assets/* regardless of route depth.
  base: "/",
  build: {
    // Optimize build output
    rollupOptions: {
      // Two documents: the app, and the desktop app's find bar, which Electron
      // loads into a view of its own over a window (src/findBar/protocol.ts).
      input: {
        main: path.resolve(__dirname, "index.html"),
        findBar: path.resolve(__dirname, "find-bar.html"),
      },
      output: {
        // Create manual chunks for better code splitting
        manualChunks: {
          // Core vendor libraries
          'vendor-react': ['react', 'react-dom'],
          // UI libraries
          'vendor-ui': ['lucide-react', 'clsx', 'tailwind-merge'],
          // Heavy dependencies
          'vendor-markdown': ['react-markdown', 'remark-gfm', 'rehype-highlight'],
          'vendor-highlight': ['highlight.js'],
          // Monaco Editor - separate chunk for lazy loading
          'vendor-monaco': ['@monaco-editor/react', '@monaco-editor/loader'],
          // Date and form libraries
          'vendor-utils': ['date-fns', 'react-hook-form', '@hookform/resolvers'],
          // State management
          'vendor-state': ['zustand', '@tanstack/react-query'],
          // Router
          'vendor-router': ['@tanstack/react-router'],
        },
      },
    },
    // Increase chunk size warning limit
    chunkSizeWarningLimit: 1000,
    // Minification options for better performance
    minify: 'terser',
    terserOptions: {
      compress: {
        // Drop the chatty levels only. console.error and console.warn stay:
        // they are what lib/clientDiagnostics.ts records for the in-app
        // Diagnostics view and what the telemetry runtime reads errors from,
        // and lib/logger.ts's always-enabled error/warn write through them.
        // `drop_console: true` stripped every console.* call AND turned the
        // `console.error.bind(console)` references those modules capture into
        // no-ops, so a production build recorded no failed RPC or stream
        // error anywhere.
        drop_debugger: true,
        pure_funcs: ['console.log', 'console.info', 'console.debug', 'console.trace'],
      },
    },
    // Generate source maps for production debugging
    sourcemap: true,
    // Asset inlining threshold
    assetsInlineLimit: 4096, // 4kb
  },
  // Optimize dependencies
  optimizeDeps: {
    include: [
      'react',
      'react-dom',
      'react-markdown',
      '@tanstack/react-query',
      '@tanstack/react-router',
      'zustand',
      '@monaco-editor/react',
      '@monaco-editor/loader',
    ],
    // Exclude Monaco Editor's web workers from optimization
    // They need to be loaded dynamically as blob URLs
    exclude: ['monaco-editor'],
  },
  server: {
    // The SDK is a sibling workspace member compiled from source, outside this
    // directory (and outside pnpm's workspace root, which is where vite's
    // default stops).
    fs: { allow: [path.resolve(__dirname, "../..")] },
    host: "127.0.0.1",
    // PORT wins over FRONTEND_PORT.
    //
    // FRONTEND_PORT is the older dev-script convention, and a dev shell often
    // still has it set from whichever stack it started. PORT is what forge
    // FORCE-injects from the
    // KCL `port` binding, above the parent shell, precisely so the launcher's
    // allocation is authoritative.
    //
    // Reading FRONTEND_PORT first inverted that: `forge env up prod --target
    // reliant-web` allocated :3100, and Vite bound the stale FRONTEND_PORT=3000
    // it inherited from a dev shell — a port another stack already held — so
    // strictPort killed the server with "Port 3000 is already in use" while
    // forge reported :3100.
    port: parseInt(process.env.PORT || process.env.FRONTEND_PORT || "5173"),
    strictPort: true,
    // Hosts the dev server will answer for, beyond localhost.
    //
    // Vite rejects unknown Host headers by default (DNS-rebinding defence), so
    // reaching this dev server through a tunnel returns "Blocked request"
    // rather than the app. That matters for the OAuth consent screen: Supabase
    // redirects the user's browser to whatever public URL is configured as the
    // Site URL, and if Vite refuses the Host the flow dies at consent.
    //
    // Tunnel hostnames are regenerated every session, so this allows the
    // provider domains rather than a specific URL that would go stale
    // immediately. DEV ONLY — vite's server block does not apply to a
    // production build, which is served by a real static host.
    allowedHosts: [
      ".ngrok-free.app",
      ".ngrok.app",
      ".ngrok.io",
      ".trycloudflare.com",
      ...(process.env.VITE_ALLOWED_HOSTS?.split(",").map((h) => h.trim()).filter(Boolean) ?? []),
    ],
    proxy: {
      // Same-origin RPC routing. admin-web's Connect transport uses
      // http://localhost:<vite-port> as its baseUrl, so EVERY RPC is
      // first-party from the browser's POV — no CORS, no per-port
      // bookkeeping. Vite fans out by proto service package:
      //
      //   /controlplane.v1.* → platform-api   (cp-forge backend)
      //   /reliant.v1.*      → reliant-api    (this repo's backend)
      //
      // The two backends are HTTP/h2c Connect; ws:false avoids Vite
      // upgrading the connection unnecessarily. Targets come from the
      // env vars the dev stack exports (VITE_CONTROL_PLANE_API_URL,
      // VITE_API_URL), with sensible standalone-dev defaults.
      // NOTE: /auth/github/callback is intentionally NOT proxied — the app owns
      // it as an SPA route (GitHubOAuthCallback) so dev behaves exactly like
      // prod Firebase, which can't proxy the callback to the GKE backend. The
      // route exchanges the code via the ExchangeGithubOAuthCode RPC, which
      // rides the /controlplane.v1. proxy below. /auth/github/authorize is a
      // top-level browser navigation (full-page redirect to GitHub), not an XHR
      // through Vite, so it needs no proxy entry either.
      "/controlplane.v1.": {
        target: process.env.VITE_CONTROL_PLANE_API_URL || "http://127.0.0.1:8090",
        changeOrigin: true,
        ws: false,
      },
      "/observability_billing.v1.": {
        target: process.env.VITE_CONTROL_PLANE_API_URL || "http://127.0.0.1:8090",
        changeOrigin: true,
        ws: false,
      },
      "/reliant.v1.": {
        target: process.env.VITE_API_URL || "http://127.0.0.1:3090",
        changeOrigin: true,
        ws: false,
      },
      // The API port's JSON /health is how the app learns the server's auth
      // mode (src/hooks/useAuthMode.ts fetches `${origin}/health`). Under the
      // same-origin transport that is Vite's origin, which without this entry
      // answers with index.html: the JSON parse fails, the app assumes
      // supabase, and an AUTH_MODE=apikey server (a self-hosted or standalone
      // dev stack) shows a Supabase sign-in it can never complete. Exact
      // path only: /healthz-style SPA routes stay Vite's.
      "^/health$": {
        target: process.env.VITE_API_URL || "http://127.0.0.1:3090",
        changeOrigin: true,
        ws: false,
      },
      // Interactive terminal is a RAW WebSocket (not a Connect RPC), served by
      // reliant-api at /api/v2/terminal/ws (internal/grpc/server.go). It needs
      // its own entry with ws:true so Vite upgrades + forwards the connection;
      // without this the browser opens ws://<vite>/api/v2/terminal/ws, Vite has
      // no rule for it, and the terminal hangs forever on "Connecting…".
      "/api/v2/terminal/ws": {
        target: process.env.VITE_API_URL || "http://127.0.0.1:3090",
        changeOrigin: true,
        ws: true,
      },
    },
  },
  test: {
    globals: true,
    environment: "jsdom",
    maxWorkers,
    testTimeout,
    hookTimeout: Math.max(10_000, testTimeout * 2),
    // Read by src/test/setup.ts, which configures Testing Library with it.
    env: { RELIANT_ASYNC_UTIL_TIMEOUT_MS: String(asyncUtilTimeout) },
    setupFiles: "./src/test/setup.ts",
    // A vmThreads worker keeps every file's VM context reachable until it is
    // recycled: unbounded, the full suite peaked at 30 GiB on 8 workers and a
    // starved worker timed a test out. Recycling at 1 GB keeps the peak at
    // ~1 GiB per worker (8.6 GiB on 8, 4 GiB on 4) for ~2x the forks speed;
    // maxWorkers (above) holds the sum to half the host's memory.
    vmMemoryLimit: VM_MEMORY_LIMIT,
    // Two projects over this config, split by pool (see FORKS_POOL_FILES).
    // Each owns its include and exclude: `extends` concatenates lists, so a
    // root include would put every file in both projects.
    projects: [
      { extends: true, test: { name: "vm", pool: "vmThreads", include: TEST_INCLUDE, exclude: [...TEST_EXCLUDE, ...FORKS_POOL_FILES] } },
      { extends: true, test: { name: "forks", pool: "forks", include: FORKS_POOL_FILES, exclude: TEST_EXCLUDE } },
    ],
  },
};

// ── The forge demo: a second build of this package ──────────────────────────
//
// demo.reliantlabs.io serves the forge demo and nothing else: entry demo.html
// → src/demoBundle/main.tsx, which mounts the demo shell's routes (demoShellRoutes.tsx).
// It builds to dist-demo/, with demo.html published as its index.html, and
// vite-plugin-demo-bundle.ts fails the build if a Reliant-only module (chat,
// onboarding, auth, Supabase, daemon, settings) reaches it. Modules with a
// `.demo.ts` twin (lib/connect.demo.ts: every RPC to the demo's transport) are
// replaced by it there ("DEMO TWINS" in vite-plugin-demo-bundle.ts).
//
//   npm run build:demo        typecheck, then this build
//   npm run dev:demo          the dev server, every page load the demo document
//
// Selected by `--mode demo` or by WEB_BUILD_TARGET=demo. The variable is how
// forge builds it: a forge frontend's build is always `npm run build` in its
// directory, with its `env_vars` as the build's environment, so the
// `forge-demo` frontend (deploy/kcl/demo/main.k) is this package with
// WEB_BUILD_TARGET=demo set.
const DEMO_HTML = "demo.html";

export function isDemoBuild(mode: string, env: NodeJS.ProcessEnv = process.env): boolean {
  return mode === "demo" || env.WEB_BUILD_TARGET === "demo";
}

/** demo.html is the demo site's index.html: in the build's output, and for every page the dev server serves. */
function demoDocument(): Plugin {
  return {
    name: "reliant:demo-document",
    enforce: "post",
    generateBundle(_options, bundle) {
      const doc = bundle[DEMO_HTML];
      if (doc) doc.fileName = "index.html";
    },
    configureServer(server) {
      server.middlewares.use((req, _res, next) => {
        const [pathname, query] = (req.url ?? "/").split("?");
        if (req.headers.accept?.includes("text/html") && !path.extname(pathname)) {
          req.url = `/${DEMO_HTML}${query ? `?${query}` : ""}`;
        }
        next();
      });
    },
  };
}

function demoConfig(app: ViteUserConfig): ViteUserConfig {
  // Not the editor's chunk: an object-form manual chunk is built whether or
  // not anything imports it, and the demo has no editor.
  const { "vendor-monaco": _monaco, ...manualChunks } = (app.build?.rollupOptions?.output as { manualChunks: Record<string, string[]> }).manualChunks;
  return {
    ...app,
    plugins: [demoSubstitutes(), ...(app.plugins ?? []), demoDocument(), demoBundleGuard()],
    define: { ...app.define, __WEB_TARGET__: JSON.stringify("demo") },
    build: {
      ...app.build,
      outDir: "dist-demo",
      rollupOptions: {
        ...app.build?.rollupOptions,
        input: { demo: path.resolve(__dirname, DEMO_HTML) },
        output: { ...app.build?.rollupOptions?.output, manualChunks },
      },
    },
  };
}

export default defineConfig(({ mode }) => (isDemoBuild(mode) ? demoConfig(appConfig) : appConfig));
