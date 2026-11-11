// The two decoders of the update streams, in one place. Both take a proto
// envelope whose payload is JSON written by the Go server (snake_case, from
// encoding/json) and produce the typed events of events.ts.

import { fromJsonString } from "@bufbuild/protobuf";
import { MessageSchema } from "#gen/reliant/v1/chat_pb.js";
import { ChatUpdateType } from "#gen/reliant/v1/streaming_pb.js";
import type { ChatUpdateData, UserUpdateData } from "#gen/reliant/v1/streaming_pb.js";
import type { ChatUpdate, ContextUsageInfo, UserUpdate } from "./events.js";
import { noopLogger, type Logger } from "./logger.js";

// Map from proto ChatUpdateType enum to string update_type discriminator
const CHAT_UPDATE_TYPE_MAP: Record<number, string> = {
  [ChatUpdateType.MESSAGE]: "message",
  [ChatUpdateType.APPROVAL]: "approval",
  [ChatUpdateType.THREAD]: "thread",
  [ChatUpdateType.TOOL_CALL]: "tool_call",
  [ChatUpdateType.WORKFLOW_STATUS]: "workflow_status",
  [ChatUpdateType.ERROR]: "error",
  [ChatUpdateType.CHAT]: "chat",
  [ChatUpdateType.RUN_OUTPUT]: "run_output",
  [ChatUpdateType.NODE_EXECUTION]: "node_execution",
  [ChatUpdateType.EXECUTION_LOG]: "execution_log",
  [ChatUpdateType.WORKFLOW_EXECUTION]: "workflow_execution",
  [ChatUpdateType.INFO]: "info",
  [ChatUpdateType.WARNING]: "warning",
  [ChatUpdateType.REFETCH]: "refetch",
  [ChatUpdateType.STREAMING_DELTA]: "streaming_delta",
  [ChatUpdateType.QUESTION]: "question",
  [ChatUpdateType.STREAM_FINALIZED]: "stream_finalized",
  [ChatUpdateType.AGENT_MESSAGES_DRAINED]: "agent_messages_drained",
};

// Valid update types that we expect from the backend
const VALID_UPDATE_TYPES = new Set([
  "message",
  "approval",
  "thread",
  "workflow_status",
  "tool_call",
  "error",
  "info",
  "warning",
  "chat",
  "streaming_delta",
  "run_output",
  "node_execution",
  "execution_log",
  "workflow_execution",
  "refetch",
  "question",
  "stream_finalized",
  "agent_messages_drained",
]);

/**
 * Validate that an update has the required fields for its type
 */
function isValidChatUpdate(
  updateType: string,
  data: Record<string, unknown>,
): boolean {
  if (!VALID_UPDATE_TYPES.has(updateType)) {
    return false;
  }

  switch (updateType) {
    case "message": {
      const wrappedMessage = data.message as Record<string, unknown> | undefined;
      if (wrappedMessage) {
        return (
          typeof wrappedMessage.id === "string" &&
          (typeof wrappedMessage.role === "string" ||
            typeof wrappedMessage.role === "number")
        );
      }
      return (
        typeof data.id === "string" &&
        (typeof data.role === "string" || typeof data.role === "number")
      );
    }
    case "approval":
      return typeof data.id === "string" && typeof data.status === "string";
    case "thread":
      return typeof data.id === "string" && typeof data.status === "string";
    case "workflow_status":
      return (
        typeof data.workflow_id === "string" || typeof data.status === "string"
      );
    case "tool_call":
      return (
        typeof data.tool_call_id === "string" || typeof data.id === "string"
      );
    case "error":
      return (
        typeof data.error_message === "string" ||
        typeof data.activity_type === "string"
      );
    case "info":
    case "warning":
      return typeof data.id === "string" && typeof data.message === "string";
    case "chat":
      return typeof data.id === "string" || typeof data.chat_id === "string";
    case "streaming_delta":
      return typeof data.delta_type === "string";
    case "stream_finalized":
      // The delta identity protocol keys everything off the pre-allocated
      // message id; a marker without one is unusable.
      return typeof data.message_id === "string";
    case "agent_messages_drained":
      // The ids ARE the payload — an announcement without them cannot retire
      // anything from the pending-queue strip.
      return Array.isArray(data.message_ids);
    default:
      return true;
  }
}

/**
 * Convert gRPC ChatUpdateData to ChatUpdate format.
 * ChatUpdateData uses a dataJson field containing the actual update data.
 * The JSON data uses snake_case because it comes from Go's encoding/json.
 * Null when the update is of a kind this SDK does not know or cannot parse.
 */
export function decodeChatUpdate(
  update: ChatUpdateData,
  log: Logger = noopLogger,
): ChatUpdate | null {
  try {
    const dataJson = update.dataJson || "{}";
    const data = JSON.parse(dataJson) as Record<string, unknown>;

    const updateTypeStr = CHAT_UPDATE_TYPE_MAP[update.updateType];
    if (!updateTypeStr) {
      log.warn("Missing update_type in update");
      return null;
    }

    if (!isValidChatUpdate(updateTypeStr, data)) {
      log.warn("Update missing expected fields", {
        updateType: updateTypeStr,
        entityId: update.entityId?.slice(0, 8),
        dataKeys: Object.keys(data),
      });
    }

    if (updateTypeStr === "message") {
      const messagePayload =
        data && typeof data.message === "object" && data.message !== null
          ? (data.message as Record<string, unknown>)
          : data;
      const parsedMessage = fromJsonString(
        MessageSchema,
        JSON.stringify(messagePayload),
        {
          ignoreUnknownFields: true,
        },
      );
      return {
        update_type: "message",
        message: parsedMessage,
      };
    }

    // Remove update_type from data before spreading — the Go backend serializes
    // UpdateType as an integer (proto enum), which would overwrite the correct
    // string discriminator derived from CHAT_UPDATE_TYPE_MAP above.
    delete data.update_type;

    return {
      update_type: updateTypeStr,
      sequence_number: Number(update.sequenceNumber),
      ...data,
    } as ChatUpdate;
  } catch (error) {
    log.error("Failed to parse update data", {
      updateType: CHAT_UPDATE_TYPE_MAP[update.updateType],
      entityId: update.entityId?.slice(0, 8),
      error: error instanceof Error ? error.message : String(error),
    });
    return null;
  }
}

/** Convert gRPC UserUpdateData to the UserUpdate shape. */
export function decodeUserUpdate(
  update: UserUpdateData,
  log: Logger = noopLogger,
): UserUpdate {
  let data: Record<string, unknown> = {};
  try {
    data = update.dataJson ? JSON.parse(update.dataJson) : {};
  } catch (error) {
    log.error("Failed to parse update data", {
      updateType: update.updateType,
      error,
    });
  }

  return {
    id: update.id,
    user_id: update.userId,
    sequence_number: Number(update.sequenceNumber),
    project_id: update.projectId,
    worktree_id: update.worktreeId,
    chat_id: update.chatId,
    update_type: update.updateType,
    entity_type: update.entityType,
    entity_id: update.entityId,
    data,
    created_at: update.createdAt,
    ...(update.orgId ? { org_id: update.orgId } : {}),
  };
}

/**
 * The context usage carried by the latest main-thread message in `updates`,
 * or null when none carries one.
 *
 * Persisted message updates carry the thread's token count and the compaction
 * threshold of the model that produced it, the same pair a snapshot reports.
 * Only the main thread's — the chat's own, keyed by the chat id, which is the
 * root workflow's thread id — drives the chat-level indicator; a spawned
 * thread's context is its own.
 */
export function latestMainThreadContextUsage(
  updates: ChatUpdateData[],
  chatId: string | undefined,
): ContextUsageInfo | null {
  if (!chatId) return null;
  for (let i = updates.length - 1; i >= 0; i--) {
    const update = updates[i];
    if (!update || update.updateType !== ChatUpdateType.MESSAGE) continue;
    let payload: Record<string, unknown>;
    try {
      const data = JSON.parse(update.dataJson || "{}") as Record<string, unknown>;
      payload =
        typeof data.message === "object" && data.message !== null
          ? (data.message as Record<string, unknown>)
          : data;
    } catch {
      continue;
    }
    if (payload.thread !== chatId) continue;
    const tokens = payload.thread_token_count;
    const threshold = payload.compaction_threshold;
    if (typeof tokens !== "number" || typeof threshold !== "number") continue;
    return { threadTokenCount: tokens, compactionThreshold: threshold };
  }
  return null;
}
