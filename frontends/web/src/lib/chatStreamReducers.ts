/**
 * Pure reducers for the chat stream (processChatStreamUpdates).
 *
 * They live in @reliantlabs/sdk, which owns the stream protocol's rules; this
 * module keeps the app's import path. chatStore composes them inside its
 * stream orchestrator; behavior is pinned by chatStore.streamEvents.test.ts /
 * chatStore.messageMerge.test.ts and the SDK's own tests.
 */
export {
  resolveStreamingBase,
  applyLogSlice,
  bySequenceNumber,
  applyHistoricalErrors,
  TERMINAL_TOOL_STATUSES,
  SETTLED_TOOL_STATUSES,
  applyToolCallStateUpdates,
  isAbortedFinalizeReason,
  toolStatusSurvivesStreamAbort,
  settleCancelledStreamBlocks,
  mergeActiveThreads,
  snapshotReplacesMessages,
  mergeMessages,
} from "@reliantlabs/sdk/client";
