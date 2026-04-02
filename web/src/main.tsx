import { createRoot } from "react-dom/client";
import "./index.css";
import "./components/workflow/workflow-theme.css";
import "./lib/debug.ts";
import "./lib/theme";
import "./lib/tabSwitchProfiler"; // Tab switch performance profiler
import { initOTelTracing } from "./lib/otel";
import { logger } from "./lib/logger";
import { Root } from "./components/Root";

// Initialize OpenTelemetry tracing (no-op if VITE_OTEL_EXPORTER_OTLP_ENDPOINT is not set)
initOTelTracing();
import { monacoManager } from "./lib/monacoManager";
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
            <svg style="position: absolute; inset: 0; width: 100%; height: 100%; animation: main-spin 1.5s linear infinite;" viewBox="0 0 100 100">
              <defs>
                <linearGradient id="gradient-ring-main" x1="0%" y1="0%" x2="100%" y2="100%">
                  <stop offset="0%" stop-color="#60a5fa" />
                  <stop offset="50%" stop-color="#a78bfa" />
                  <stop offset="100%" stop-color="#60a5fa" stop-opacity="0" />
                </linearGradient>
              </defs>
              <circle cx="50" cy="50" r="45" fill="none" stroke="url(#gradient-ring-main)" stroke-width="2.5" stroke-linecap="round" />
            </svg>
            <svg style="width: 64px; height: 64px;" viewBox="0 0 1187.07 682.09" xmlns="http://www.w3.org/2000/svg">
              <path fill="#0d8cff" d="M551.54,73.53l-98.13,98.13c-33.34-22.45-72.48-34.48-113.5-34.48-54.46,0-105.63,21.22-144.18,59.72-31.87,31.92-51.86,72.48-57.76,116.47H0c6.4-80.67,40.88-155.62,98.77-213.51C163.21,35.49,248.81,0,339.9,0c77.78,0,151.59,25.88,211.63,73.53Z"/>
              <path fill="#0d8cff" d="M822.2,341.05l-241.13,241.13c-9.47,9.47-19.34,18.25-29.59,26.29-61.73,48.98-136.68,73.44-211.59,73.44-87.34,0-174.64-33.24-241.13-99.73C40.88,524.33,6.4,449.38,0,368.71h138.01c5.85,43.95,25.84,84.55,57.71,116.47,38.55,38.5,89.72,59.68,144.18,59.72,41.02,0,80.16-12.03,113.54-34.48,10.88-7.32,21.13-15.73,30.64-25.24l241.13-241.13,5.76,5.72,91.23,91.27Z"/>
              <path fill="#6e39ff" d="M1187.07,368.71c-6.36,80.67-40.88,155.62-98.82,213.46-64.34,64.43-150.04,99.87-241.08,99.92-77.78,0-151.59-25.88-211.63-73.58l98.18-98.13c79.16,53.14,187.63,44.72,257.59-25.2,31.92-31.92,51.95-72.53,57.85-116.47h137.92Z"/>
              <path fill="#6e39ff" d="M1187.07,313.38h-137.92c-5.9-43.99-25.93-84.55-57.85-116.47-38.5-38.5-89.67-59.72-144.14-59.72-41.02,0-80.21,12.03-113.5,34.48-10.88,7.32-21.13,15.73-30.68,25.24l-109.43,109.47-34.66,34.66-96.99,96.99-5.76-5.76-91.27-91.23,91.27-91.23,40.42-40.42,96.99-97.04,12.48-12.48c9.42-9.42,19.21-18.2,29.5-26.34C695.58,25.88,769.39,0,847.17,0c91.05,0,176.74,35.49,241.17,99.87,57.85,57.89,92.37,132.84,98.73,213.51Z"/>
            </svg>
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

// Pre-initialize Monaco BEFORE React renders (critical for performance)
// This loads Monaco and workers synchronously to ensure they're cached before any components mount
logger.info('[Main] 🚀 Pre-initializing Monaco Editor (BLOCKING)...');
const monacoStart = performance.now();

// CRITICAL: Wait for Monaco before rendering React
// This ensures DiffEditor components can use cached Monaco immediately
await monacoManager.getMonaco().catch((error) => {
  logger.error('[Main] ❌ Monaco pre-initialization FAILED:', error);
  // Continue anyway - Monaco will load on first use
});

const monacoDuration = performance.now() - monacoStart;
logger.info('[Main] ✅ Monaco pre-initialized in', monacoDuration.toFixed(2), 'ms');

// NOTE: Static data prefetch moved to AuthGuard.tsx to run AFTER authentication is ready
// This prevents 401 errors when prefetch runs before auth token is available

// NOTE: StrictMode is intentionally disabled to avoid double-mounting effects
// which causes issues with WebSocket connections and gRPC streams
createRoot(document.getElementById("root")!).render(<Root />);