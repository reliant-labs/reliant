// Two configuration documents reach the renderer, and this module owns both
// checks:
//
//   window.__FORGE_CONFIG__  the RUNTIME CONFIG (config.js, typed by the
//                            generated config_gen.ts): per-environment
//                            endpoints, identity, telemetry. Present before
//                            the bundle runs. checkWebConfig() below.
//   window.RELIANT_CONFIG    host facts in the desktop app (the local daemon
//                            URL, platform), injected asynchronously by
//                            preload.js. waitForConfig() below.

import { loadConfig, missingRequiredConfig } from "./config_gen";
import { logger } from "./logger";
import { isPackagedRendererOrigin } from "./protocol";

// ============================================================================
// Runtime config (config.js)
// ============================================================================

/** Fields read as `=== "true"`: anything but "true", "false" or unset is a typo. */
const FLAG_KEYS = [
  "DISABLE_TLS",
  "AUTO_ANON_SIGN_IN",
  "CLI_DEFAULTS_BAKED",
  "OTEL_REPLAY",
] as const;

// "self-hosted" is unset spelled out, for a document another project renders
// (it must state its mode; see web_config.proto).
const AUTH_MODES = ["", "cloud", "self-hosted"];

/**
 * Why this renderer's runtime config cannot work, or [] when it can.
 *
 * The generated loadConfig() checks types and supplies defaults, and every
 * field has one, so a document that names nothing is the SELF-HOSTED app,
 * not an error. What it cannot check is that a deployment stated what it
 * needs, which is decided here:
 *
 *   - the packaged desktop app always has a document: its app:// handler
 *     answers config.js from the main process, so none is a broken build;
 *   - a managed deployment (AUTH_MODE = "cloud") needs its endpoints and its
 *     sign-in key, or it would boot against localhost and a missing Supabase
 *     and fail three screens later with nothing naming the cause. Which keys
 *     is the proto's `required_when` (web_config.proto), judged by the
 *     generated missingRequiredConfig() on the raw document: a default
 *     (SUPABASE_URL's local Supabase) is exactly what a managed deployment
 *     must not fall back to;
 *   - a closed-set value outside its set is a typo that would otherwise
 *     silently read as off.
 */
export function checkWebConfig(): string[] {
  const raw = typeof window !== "undefined" ? window.__FORGE_CONFIG__ : undefined;
  if (!raw && isPackagedRendererOrigin()) {
    return [
      "config.js did not load. The desktop app's app:// handler serves it from " +
        "the main process's resolved config (electron/src/app-protocol.js); " +
        "without it this build has no endpoints at all.",
    ];
  }

  // loadConfig() throws on these too, as one combined message; name them by
  // the mode that requires them instead.
  const missing = missingRequiredConfig();
  if (missing.length > 0) {
    const byMode = new Map<string, string[]>();
    for (const m of missing) {
      const mode = `${m.when.key} is "${m.when.equals}"`;
      byMode.set(mode, [...(byMode.get(mode) ?? []), m.key]);
    }
    return [...byMode].map(
      ([mode, keys]) =>
        `${mode}, which requires ${keys.join(", ")}: ` +
        "the managed app cannot reach its backends or sign anyone in without them.",
    );
  }

  let config;
  try {
    config = loadConfig();
  } catch (err) {
    return [err instanceof Error ? err.message : String(err)];
  }

  const problems: string[] = [];
  if (!AUTH_MODES.includes(config.AUTH_MODE)) {
    problems.push(
      `AUTH_MODE is "${config.AUTH_MODE}"; it must be "cloud" (a managed deployment), "self-hosted" or unset.`,
    );
  }
  for (const key of FLAG_KEYS) {
    const value = config[key];
    if (value !== "" && value !== "true" && value !== "false") {
      problems.push(`${key} is "${value}"; it must be "true", "false" or unset.`);
    }
  }
  return problems;
}

/**
 * Replace the page with a plain statement of what is wrong with its runtime
 * config. Text only (values from the document are never parsed as HTML), and
 * styled inline because no stylesheet is guaranteed to have loaded.
 */
export function renderConfigError(problems: string[], root = document.getElementById("root")): void {
  if (!root) return;
  const panel = document.createElement("div");
  panel.setAttribute("role", "alert");
  panel.style.cssText =
    "position:fixed;inset:0;overflow:auto;padding:48px;background:#0b0b0f;color:#e5e5e5;" +
    "font:14px/1.5 system-ui,sans-serif";

  const title = document.createElement("h1");
  title.textContent = "Reliant can't start: this deployment's configuration is incomplete";
  title.style.cssText = "font-size:18px;font-weight:600;margin:0 0 16px";
  panel.append(title);

  const list = document.createElement("ul");
  list.style.cssText = "margin:0 0 16px;padding-left:20px";
  for (const problem of problems) {
    const item = document.createElement("li");
    item.textContent = problem;
    item.style.cssText = "margin-bottom:8px;white-space:pre-wrap";
    list.append(item);
  }
  panel.append(list);

  const hint = document.createElement("p");
  hint.textContent =
    "These values come from config.js (window.__FORGE_CONFIG__), rendered per " +
    "environment from the frontend's runtime_config. The keys it may carry are " +
    "listed in frontends/web/src/lib/config_schema_gen.json.";
  hint.style.cssText = "color:#a1a1aa;margin:0";
  panel.append(hint);

  root.replaceChildren(panel);
}

// ============================================================================
// Desktop host config (window.RELIANT_CONFIG)
// ============================================================================

// Default timeout for waiting for config
const DEFAULT_TIMEOUT_MS = 5000;
const POLL_INTERVAL_MS = 100;

/**
 * Check if backend config is available
 */
export const isConfigReady = (): boolean => {
  if (typeof window === "undefined") return false;

  // In browser mode (dev server or hosted web), config is always "ready" —
  // there is no preload.js; the runtime config (config.js) loaded before
  // the bundle.
  if (!isPackagedRendererOrigin()) {
    return true;
  }

  // In the packaged desktop app, preload.js injects RELIANT_CONFIG
  // asynchronously; until it lands there is no backend URL to dial.
  return !!window.RELIANT_CONFIG?.grpcUrl;
};

/**
 * Wait for backend configuration to be available
 * @param timeoutMs Maximum time to wait (default 5 seconds)
 * @returns Promise that resolves when config is ready or rejects on timeout
 */
export const waitForConfig = async (timeoutMs = DEFAULT_TIMEOUT_MS): Promise<void> => {
  // Outside the packaged app, config is immediately available.
  if (typeof window === "undefined" || !isPackagedRendererOrigin()) {
    return;
  }
  
  // Already ready
  if (isConfigReady()) {
    return;
  }
  
  logger.info("[ConfigReady] Waiting for backend configuration...");
  const startTime = Date.now();
  
  return new Promise((resolve, reject) => {
    let settled = false;
    let pollTimeoutId: ReturnType<typeof setTimeout> | undefined;

    const settle = () => {
      if (settled) return;
      settled = true;
      window.removeEventListener("message", handleMessage);
      if (pollTimeoutId !== undefined) {
        clearTimeout(pollTimeoutId);
        pollTimeoutId = undefined;
      }
    };

    // Listen for config-ready event
    const handleConfigReady = () => {
      logger.info("[ConfigReady] Config ready event received");
      settle();
      resolve();
    };
    
    const handleMessage = (event: MessageEvent) => {
      if (event.source !== window) return;
      if (event.origin !== window.location.origin) return;
      if (event.data?.type === "reliant-config-ready" && event.data?.config) {
        window.RELIANT_CONFIG = event.data.config;
        handleConfigReady();
      }
    };
    
    window.addEventListener("message", handleMessage);
    
    // Also poll in case event was missed
    const poll = () => {
      if (settled) return;

      if (isConfigReady()) {
        settle();
        logger.info("[ConfigReady] Config became available via polling");
        resolve();
        return;
      }
      
      if (Date.now() - startTime >= timeoutMs) {
        settle();
        logger.warn("[ConfigReady] Timeout waiting for backend configuration");
        reject(new Error("Backend configuration not available after timeout"));
        return;
      }
      
      pollTimeoutId = setTimeout(poll, POLL_INTERVAL_MS);
    };
    
    // Start polling
    poll();
  });
};

/**
 * Execute a function after config is ready, with optional timeout
 * @param fn Function to execute
 * @param timeoutMs Timeout for waiting
 * @returns Promise with function result
 */
export const withConfigReady = async <T>(
  fn: () => Promise<T>,
  timeoutMs = DEFAULT_TIMEOUT_MS
): Promise<T> => {
  await waitForConfig(timeoutMs);
  return fn();
};