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
