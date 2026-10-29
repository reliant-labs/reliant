import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { forgeRuntimeConfig } from "@reliantlabs/forge-web-runtime/vite";
import os from "node:os";
import path from "path";
import { browserLogSink } from "./vite-plugin-browser-logs";
import { versionJson } from "./vite-plugin-version-json";
import { cspPlugin } from "./vite-plugin-csp";
import { speedscopeStatic } from "./vite-plugin-speedscope";

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
const maxWorkers = intEnv("VITEST_MAX_WORKERS") ?? Math.max(2, Math.min(8, cores));
const loadFactor = Math.min(6, Math.max(1, Math.ceil(os.loadavg()[0] / cores)));
const testTimeout = intEnv("VITEST_TIMEOUT_MS") ?? 5_000 * loadFactor;
const asyncUtilTimeout = intEnv("VITEST_ASYNC_UTIL_TIMEOUT_MS") ?? Math.round(testTimeout / 2);

// https://vite.dev/config/
export default defineConfig({
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
  plugins: [react(), browserLogSink(), versionJson(), cspPlugin(), speedscopeStatic(), forgeRuntimeConfig()],
  // Load .env files from the checkout root instead of frontends/web
  envDir: path.resolve(__dirname, "../.."),
  // Define global constants available in the app
  define: {
    __WORKTREE_NAME__: JSON.stringify(getWorktreeName()),
  },
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
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
      //   /controlplane.v1.* → admin-server   (cp-forge backend)
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
    exclude: [
      "**/node_modules/**",
      "**/dist/**",
      "**/e2e/**", // Exclude Playwright e2e tests
      "**/.{idea,git,cache,output,temp}/**",
      "**/{karma,rollup,webpack,vite,vitest,jest,ava,babel,nyc,cypress,tsup,build}.config.*",
    ],
  },
});