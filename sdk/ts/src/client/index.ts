// @reliantlabs/sdk/client — Reliant's framework-free client.
//
// Transport and auth on top of forge-web-runtime's interceptors, one
// reconnecting stream with resume by sequence number, typed events and their
// decoders, a pure reducer, and retry-safe sends. No React, no Node-only API.

export { createReliantClient, type ReliantClient, type ReliantClientOptions } from "./client.js";
export { staticTokenProvider, type AuthTokenProvider } from "./auth.js";
export { resolveTransport, sdkInterceptors, serviceClient, type ReliantTransportOptions } from "./transport.js";
export { noopLogger, type Logger } from "./logger.js";

export {
  ReconnectingStream,
  browserWakeSource,
  isConnectionLevelFailure,
  BASE_RECONNECT_DELAY_MS,
  MAX_RECONNECT_DELAY_MS,
  SHORT_LIVED_CONNECTION_MS,
  STREAM_STALE_TIMEOUT_MS,
  WATCHDOG_TICK_MS,
  RESUME_LIVENESS_GRACE_MS,
  RESUME_STALE_AFTER_MS,
  RESUME_REPLACE_AFTER_HIDDEN_MS,
  RESUME_REPLACE_MIN_INTERVAL_MS,
  type StreamEnvironment,
  type StreamLifecycleCallbacks,
  type StreamTuning,
  type WakeHandlers,
  type WakeSource,
} from "./reconnecting-stream.js";
export {
  UserStream,
  type StreamingClient,
  type UserStreamCallbacks,
  type UserStreamOptions,
} from "./user-stream.js";
export {
  ChatStream,
  traceIdFromTraceresponse,
  type ChatCaughtUpInfo,
  type ChatStreamCallbacks,
  type ChatStreamHooks,
  type ChatStreamOptions,
  type ChatSyncKind,
  type SnapshotTiming,
} from "./chat-stream.js";
export {
  ProcessOutputStream,
  type BackgroundClient,
  type OutputLine,
  type ProcessOutputCallbacks,
  type ProcessOutputStreamOptions,
} from "./process-output-stream.js";

export * from "./events.js";
export { decodeChatUpdate, decodeUserUpdate, latestMainThreadContextUsage } from "./decode.js";
export type {
  Attachment,
  ContentBlock,
  ContentBlockBody,
  Message,
  ToolBodies,
  ToolCallState,
  ToolExecutionStateUpdate,
} from "./types.js";
export {
  fetchContentBlockBodies,
  MAX_CONTENT_BLOCK_BODY_IDS,
  type ContentBlockBodies,
  type ContentBlockBodiesClient,
  type FetchContentBlockBodiesOptions,
} from "./content-block-bodies.js";

export { createChatState, reduceChat } from "./chat-reducer.js";
export type { ChatAction, ChatState, ReduceEnv } from "./chat-state.js";
export {
  ChatSession,
  type ChatSessionOptions,
  type SendOptions,
  type SendResult,
} from "./chat-session.js";

export * from "./reducers.js";
export * from "./message-order.js";
export * from "./tool-results.js";
export * from "./send-ids.js";
