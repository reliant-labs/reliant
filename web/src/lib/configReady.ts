// Utility for waiting for backend configuration to be ready
// This is needed in Electron where the config is injected asynchronously

import { logger } from "./logger";
import { isPackagedRendererOrigin } from "./protocol";

// Default timeout for waiting for config
const DEFAULT_TIMEOUT_MS = 5000;
const POLL_INTERVAL_MS = 100;

/**
 * Check if backend config is available
 */
export const isConfigReady = (): boolean => {
  if (typeof window === "undefined") return false;

  // In browser mode (dev server or hosted web), config is always "ready" —
  // it comes from build-time env vars, not from preload.js.
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