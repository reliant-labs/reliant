/**
 * Application-wide constants
 * 
 * Centralizes magic numbers and configuration values for easier maintenance
 * and documentation.
 */

// ============================================================================
// Environment Detection
// ============================================================================

/**
 * Runtime dev-mode detection.
 *
 * Important: in Electron we must prefer RELIANT_CONFIG.isDev (derived from app.isPackaged)
 * over Vite's build-time import.meta.env.DEV to avoid dev/prod mismatches in packaged builds.
 */
export const getIsDev = (): boolean => {
  if (typeof window !== "undefined") {
    const isElectronRuntime = !!window.electronAPI || !!window.RELIANT_CONFIG?.isElectron;
    if (isElectronRuntime) {
      // Primary source in Electron: RELIANT_CONFIG.isDev (derived from app.isPackaged)
      if (typeof window.RELIANT_CONFIG?.isDev === "boolean") {
        return window.RELIANT_CONFIG.isDev;
      }

      // Fallback during very early bootstrap before RELIANT_CONFIG is injected:
      // - file:// means packaged app => production behavior
      // - http(s) in local Electron dev => allow dev behavior
      if (window.location.protocol === "file:") {
        return false;
      }

      return !!import.meta.env.DEV;
    }
  }

  return !!import.meta.env.DEV;
};

/**
 * Backwards-compatible snapshot for existing call sites.
 * Prefer getIsDev() in runtime-sensitive paths (auth, transport, storage).
 */
export const isDev = getIsDev();

// ============================================================================
// Chat Store Constants
// ============================================================================

/**
 * Maximum number of messages to keep in memory per chat.
 * Beyond this limit, older messages are trimmed to prevent memory bloat.
 * Users can still load older messages via pagination.
 */
export const MAX_MESSAGES_IN_MEMORY = 500;

/**
 * Number of messages to retain when trimming.
 * When MAX_MESSAGES_IN_MEMORY is exceeded, we trim down to this count.
 * The gap (500 - 400 = 100) provides hysteresis to avoid frequent trimming.
 */
export const MESSAGES_TO_KEEP_ON_TRIM = 400;

/**
 * Maximum number of error/info/run events to keep per chat.
 * Prevents memory bloat from long-running chats with many events.
 * Older events are trimmed when this limit is exceeded.
 */
export const MAX_EVENTS_IN_MEMORY = 200;

/**
 * Maximum number of tool call states to keep per chat.
 * Prevents memory bloat from chats with many tool executions.
 */
export const MAX_TOOL_CALL_STATES = 500;

/**
 * Maximum number of processed messages to cache per chat.
 * Should match MAX_MESSAGES_IN_MEMORY to avoid cache growing larger than message list.
 */
export const MAX_PROCESSED_MESSAGES = 500;

/**
 * Maximum number of concurrent gRPC/WebSocket streams per client.
 * Older streams are evicted using LRU when this limit is reached.
 * Balances real-time updates against connection resource usage.
 */
export const MAX_CONCURRENT_STREAMS = 5;

/**
 * Delay in ms before flushing streaming buffer if no newline received.
 * Streaming deltas are batched on newlines to reduce re-renders.
 * This timeout ensures content appears even without newlines.
 * 
 * PERFORMANCE: Increased from 100ms to 250ms to reduce re-renders.
 * At typical typing speeds (40 WPM = ~3 chars/sec), 250ms batches
 * about 75 characters per update which is a good balance between
 * responsiveness and performance.
 */
export const STREAMING_FLUSH_TIMEOUT_MS = 250;

// ============================================================================
// gRPC Client Constants
// ============================================================================

/**
 * Default timeout for gRPC requests in milliseconds.
 * Prevents requests from hanging indefinitely.
 * 
 * Note: Browsers limit HTTP/1.1 to ~6 concurrent connections per origin.
 * Short timeouts help prevent connection starvation in local dev.
 */
export const DEFAULT_GRPC_TIMEOUT_MS = 10000;

/**
 * Extended timeout for file operations (read/write large files).
 */
export const FILE_OPERATION_TIMEOUT_MS = 30000;

/**
 * Extended timeout for chat operations that involve workflow initialization.
 */
export const CHAT_OPERATION_TIMEOUT_MS = 30000;

/**
 * Extended timeout for MCP operations (external process startup).
 */
export const MCP_OPERATION_TIMEOUT_MS = 60000;

/**
 * Extended timeout for attachment uploads.
 */
export const UPLOAD_TIMEOUT_MS = 60000;

// Worktree operations timeout - git commands can take 10-30s
export const WORKTREE_OPERATION_TIMEOUT_MS = 30000;

/**
 * Extended timeout for OAuth flows — user must complete login in browser.
 * The actual user-facing timeout is 120s; we add buffer so the gRPC layer
 * never fires before the application-level timeout.
 */
export const OAUTH_TIMEOUT_MS = 150000;

/**
 * Extended timeout for OAuth token exchange with external providers.
 */
export const OAUTH_EXCHANGE_TIMEOUT_MS = 30000;

/**
 * Extended timeout for provider API key validation (external network call).
 */
export const PROVIDER_VALIDATION_TIMEOUT_MS = 20000;

// ============================================================================
// UI Constants
// ============================================================================

/**
 * Collapsed height threshold for tool execution content (in pixels).
 * Content exceeding this height shows expand/collapse controls.
 */
export const TOOL_CONTENT_COLLAPSED_HEIGHT = 100;

/**
 * Maximum reconnection attempts for streaming connections.
 */
export const MAX_RECONNECT_ATTEMPTS = 10;

/**
 * Base delay for reconnection backoff (in ms).
 * Actual delay uses exponential backoff with jitter.
 */
export const RECONNECT_BASE_DELAY_MS = 1000;

/**
 * Maximum reconnection delay (in ms).
 */
export const RECONNECT_MAX_DELAY_MS = 30000;