// Centralized logger that prevents memory leaks from logging large objects
import { isDev } from './constants';
const MAX_STRING_LENGTH = 200;
const MAX_ARRAY_PREVIEW = 10;

// Track console log count for periodic clearing
let consoleLogCount = 0;
const MAX_CONSOLE_LOGS = 1000; // Clear console after this many logs

// Check if we're in Electron
const isElectron = typeof window !== "undefined" && window.electronAPI;

// Performance optimization: create no-op functions for production
const noop = () => {};

// Helper to safely stringify objects without retaining references
function safeStringify(obj: unknown, seen = new WeakSet()): unknown {
  if (obj === null || obj === undefined) return obj;

  // Primitives are safe
  if (typeof obj !== "object") {
    if (typeof obj === "string" && obj.length > MAX_STRING_LENGTH) {
      return obj.substring(0, MAX_STRING_LENGTH) + `...(${obj.length} chars)`;
    }
    return obj;
  }

  // Prevent circular references
  if (seen.has(obj)) return "[Circular]";
  seen.add(obj);

  // Arrays - show length and first few items
  if (Array.isArray(obj)) {
    if (obj.length === 0) return "[]";
    if (obj.length > MAX_ARRAY_PREVIEW) {
      return `[Array(${obj.length}): ${obj
        .slice(0, 3)
        .map((item) => (typeof item === "object" ? "{...}" : String(item)))
        .join(", ")}...]`;
    }
    return obj.map((item) => safeStringify(item, seen));
  }

  // Special handling for common objects
  if ("content" in obj && typeof obj.content === "string") {
    return {
      ...obj,
      content:
        obj.content.length > 100
          ? obj.content.substring(0, 100) + `...(${obj.content.length} chars)`
          : obj.content,
    };
  }

  if ("messages" in obj && Array.isArray(obj.messages)) {
    return {
      ...obj,
      messages: `[Array(${obj.messages.length})]`,
    };
  }

  // For other objects, create a shallow summary
  const summary: Record<string, unknown> = {};
  const keys = Object.keys(obj);
  const maxKeys = 5;

  for (let i = 0; i < Math.min(keys.length, maxKeys); i++) {
    const key = keys[i];
    const value = (obj as Record<string, unknown>)[key];

    if (typeof value === "object") {
      summary[key] = Array.isArray(value)
        ? `[Array(${value.length})]`
        : "{...}";
    } else if (typeof value === "string" && value.length > 50) {
      summary[key] = value.substring(0, 50) + "...";
    } else {
      summary[key] = value;
    }
  }

  if (keys.length > maxKeys) {
    summary["..."] = `${keys.length - maxKeys} more properties`;
  }

  return summary;
}

// Clear console periodically to prevent memory buildup
function checkAndClearConsole() {
  consoleLogCount++;
  if (consoleLogCount >= MAX_CONSOLE_LOGS) {
    console.clear();
    console.log("[Logger] Console cleared to prevent memory buildup");
    consoleLogCount = 0;
  }
}

// Send log to Electron main process if available (only in dev or when needed)
function sendToElectron(level: string, args: unknown[]) {
  // Skip electron logging in production web builds for performance
  if (!isDev && !isElectron) return;

  if (isElectron && window.electronAPI.log) {
    try {
      // Send to main process for file logging
      window.electronAPI.log(level, ...args.map((arg) => safeStringify(arg)));
    } catch (err) {
      // Fallback to console if IPC fails
      console.error("[Logger] Failed to send to main process:", err);
    }
  }
}

// Production-optimized logger functions
function createLogFunction(level: string, alwaysEnabled = false) {
  if (!isDev && !alwaysEnabled) {
    // Return no-op function for production builds (when not always enabled)
    return noop;
  }

  return (...args: unknown[]) => {
    const safe = args.map((arg) => safeStringify(arg));
    sendToElectron(level, safe);

    if (level === "error" || isDev) {
      // Use the ORIGINAL console methods, not the globals. installConsoleOverride
      // replaces console.* with a wrapper that itself calls sendToElectron, so
      // going through the global here shipped every logger.* call to the main
      // process twice — two IPC round-trips and two disk writes per call.
      switch (level) {
        case "error":
          originalConsole.error(...safe);
          break;
        case "warn":
          originalConsole.warn(...safe);
          break;
        case "debug":
          originalConsole.debug(...safe);
          break;
        case "info":
          originalConsole.info(...safe);
          break;
        default:
          originalConsole.log(...safe);
      }
      checkAndClearConsole();
    }
  };
}

// Store original console methods before override
const originalConsole = {
  log: console.log.bind(console),
  error: console.error.bind(console),
  warn: console.warn.bind(console),
  info: console.info.bind(console),
  debug: console.debug.bind(console),
};

// Flag to prevent recursive logging
let isLogging = false;

// Create a redirected console method that goes through our logging system
function createRedirectedConsole(
  level: string,
  originalMethod: (...args: unknown[]) => void
) {
  return (...args: unknown[]) => {
    // Prevent recursion
    if (isLogging) {
      originalMethod(...args);
      return;
    }

    isLogging = true;
    try {
      const safe = args.map((arg) => safeStringify(arg));

      // Send to Electron for proper log routing
      sendToElectron(level, safe);

      // Also output to original console for dev visibility
      if (level === "error" || level === "warn" || isDev) {
        originalMethod(...safe);
        checkAndClearConsole();
      }
    } finally {
      isLogging = false;
    }
  };
}

// Install console overrides to redirect all console.log calls through our system
let consoleOverrideInstalled = false;

export function installConsoleOverride() {
  if (consoleOverrideInstalled) return;

  console.log = createRedirectedConsole("info", originalConsole.log);
  console.error = createRedirectedConsole("error", originalConsole.error);
  console.warn = createRedirectedConsole("warn", originalConsole.warn);
  console.info = createRedirectedConsole("info", originalConsole.info);
  console.debug = createRedirectedConsole("debug", originalConsole.debug);

  consoleOverrideInstalled = true;
}

// Restore original console methods if needed
export function restoreConsole() {
  if (!consoleOverrideInstalled) return;

  console.log = originalConsole.log;
  console.error = originalConsole.error;
  console.warn = originalConsole.warn;
  console.info = originalConsole.info;
  console.debug = originalConsole.debug;

  consoleOverrideInstalled = false;
}

export const logger = {
  log: createLogFunction("info"),
  error: createLogFunction("error", true), // Always enabled
  warn: createLogFunction("warn", true), // Always enabled
  debug: createLogFunction("debug"),
  info: createLogFunction("info"),

  // Special method for performance logging
  perf: isDev
    ? (label: string, data?: unknown) => {
        const safe = safeStringify(data);
        sendToElectron("info", [`[PERF] ${label}`, safe]);
        originalConsole.log(`[PERF] ${label}`, safe);
        checkAndClearConsole();
      }
    : noop,

  // Metrics logging that's always enabled but safe
  metrics: (label: string, metrics: Record<string, number>) => {
    // Only log numeric metrics, never objects
    const safe = Object.entries(metrics)
      .filter(([, v]) => typeof v === "number")
      .map(([k, v]) => `${k}: ${v}`)
      .join(", ");
    const message = `📊 ${label}: ${safe}`;
    sendToElectron("info", [message]);
    if (isDev) {
      originalConsole.log(message);
      checkAndClearConsole();
    }
  },
};

// Auto-install console override on module load
installConsoleOverride();
