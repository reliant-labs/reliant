// MUST STAY FIRST. Imports are evaluated in order, and this one wraps console
// before any other module can log. A call in the module body instead of an
// import would run AFTER every import below (hoisting), missing all of
// startup — see lib/browser-log-boot.ts.
import "./lib/browser-log-boot";

import { createRoot } from "react-dom/client";
import "./index.css";
import "./components/workflow/workflow-theme.css";
import "./lib/debug.ts";
import "./lib/theme";
import "./lib/tabSwitchProfiler"; // Tab switch performance profiler
import { initOTelTracing } from "./lib/otel";
import { logger } from "./lib/logger";
import { Root } from "./components/Root";
import { STARTUP_SPINNER_MARKUP } from "./components/icons/StartupSpinnerMark";

// Initialize OpenTelemetry tracing (no-op if VITE_OTEL_ENDPOINT is not set)
initOTelTracing();
import { monacoManager } from "./lib/monacoManager";
import { shouldPreloadMonaco } from "./lib/monacoPreload";
import { waitForConfig, isConfigReady } from "./lib/configReady";
// globalDataStore prefetch is now called from AuthGuard after authentication

// CRITICAL: Wait for Electron backend config BEFORE any gRPC/API calls
// In production Electron builds, RELIANT_CONFIG is injected asynchronously by preload.js
// If we don't wait, gRPC clients throw "not ready" errors and auth/settings fail
const configStart = performance.now();
if (!isConfigReady()) {
  logger.info('[Main] ⏳ Waiting for backend configuration (Electron production mode)...');
  
  // Show loading spinner while waiting for config (matches LoadingSpinner component)
  const rootEl = document.getElementById("root");
  if (rootEl) {
    rootEl.innerHTML = `
      <style>
        @keyframes main-spin { to { transform: rotate(360deg); } }
      </style>
      <div style="position: fixed; inset: 0; background: hsl(240 10% 3.9%); display: flex; flex-direction: column;">
        <div style="flex: 1; display: flex; align-items: center; justify-content: center;">
          <div style="position: relative; width: 128px; height: 128px; display: flex; align-items: center; justify-content: center;">
            <svg style="position: absolute; inset: 0; width: 100%; height: 100%; animation: main-spin 1.5s linear infinite;" viewBox="0 0 100 100" aria-hidden="true" focusable="false">
              <defs>
                <linearGradient id="gradient-ring-main" x1="0%" y1="0%" x2="100%" y2="100%">
                  <stop offset="0%" stop-color="#60a5fa" />
                  <stop offset="50%" stop-color="#a78bfa" />
                  <stop offset="100%" stop-color="#60a5fa" stop-opacity="0" />
                </linearGradient>
              </defs>
              <circle cx="50" cy="50" r="45" fill="none" stroke="url(#gradient-ring-main)" stroke-width="2.5" stroke-linecap="round" />
            </svg>
            ${STARTUP_SPINNER_MARKUP}
          </div>
        </div>
      </div>
    `;
  }
  
  try {
    await waitForConfig(15000); // 15 second timeout (backend may need time to start)
    const configDuration = performance.now() - configStart;
    logger.info('[Main] ✅ Backend configuration ready in', configDuration.toFixed(2), 'ms');
  } catch (error) {
    logger.error('[Main] ❌ Backend configuration timeout:', error);
    // Continue anyway - the app will show errors but at least render
    // This allows users to see something and potentially debug
  }
} else {
  logger.info('[Main] ✅ Backend configuration already available (dev mode or reload)');
}

// Pre-initialize Monaco in the background (non-blocking)
// Monaco will be ready by the time the user opens a code editor/diff view
//
// Skipped on the mobile surface and on unauthenticated entry routes such as
// /oauth/consent — see lib/monacoPreload for why each is excluded. Reading the
// pathname rather than the router is deliberate: this runs before React
// mounts, and the predicate is pure.
if (shouldPreloadMonaco(window.location.pathname)) {
  logger.info('[Main] 🚀 Pre-initializing Monaco Editor (background)...');
  monacoManager.getMonaco().catch((error) => {
    logger.error('[Main] ❌ Monaco pre-initialization FAILED:', error);
  });
} else {
  logger.info('[Main] ⏭️  Skipping Monaco pre-initialization (non-desktop surface)');
}

// NOTE: Static data prefetch moved to AuthGuard.tsx to run AFTER authentication is ready
// This prevents 401 errors when prefetch runs before auth token is available

// NOTE: StrictMode is intentionally disabled to avoid double-mounting effects
// which causes issues with WebSocket connections and gRPC streams
createRoot(document.getElementById("root")!).render(<Root />);