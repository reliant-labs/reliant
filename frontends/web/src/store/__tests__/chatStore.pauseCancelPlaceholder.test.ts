import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ChatUpdate } from "../../types/streaming";

// A tool call whose input was still streaming when the user paused or cancelled
// renders as "Preparing..." forever — the renderers key that state off
// `input === undefined` (ToolExecution.tsx, messageProcessor.ts).
//
// Both actions must clean up after themselves. Neither can rely on anything
// else to do it:
//
//   - The IDLE activity-changed handler is a TRANSIENT event. Miss it once
//     (the action raced it, the tab was closed) and nothing re-runs it.
//   - The snapshot self-heal in processChatStreamUpdates only fires for a chat
//     the server reports as NOT running. A PAUSED chat's workflow is still
//     alive — RUNNING in Temporal with a pending activity — so that path
//     deliberately leaves its streaming state alone, and correctly so: a
//     mid-run reconnect must not blank a genuinely streaming tool call.
//
// Observed on chat 7fc640a6 (pause) and 995f36f5 (cancel).

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>(
    "../../api/client",
  );
  return {
    ...actual,
    api: {
      ...actual.api,
      chatsV2: {
        ...actual.api.chatsV2,
        pause: vi.fn().mockResolvedValue({}),
        cancel: vi.fn().mockResolvedValue({}),
        dismiss: vi.fn().mockResolvedValue({ changed: false }),
      },
    },
  };
});

import { useChatStore } from "../chatStore";
import { useProjectStore } from "../projectStore";
import { clearAllMessagesCache } from "../../hooks/message-queries";
import { chatKeys } from "../../hooks/chat-queries";
import { queryClient } from "../../lib/query-client";

function seed(chatId: string) {
  clearAllMessagesCache();
  useChatStore.setState({
    activeChatId: null,
    streamingMessages: {},
    toolResultsByCallId: {},
    errorEvents: {},
    infoEvents: {},
    runOutputs: {},
    nodeExecutions: {},
    toolCallStates: {},
  } as never);
  // Both actions bail early via getChatFromCache if the chat is not in the
  // React Query detail cache; seed one so they reach the code under test.
  useProjectStore.setState({
    currentProject: { id: "p1", name: "p", path: "/p" },
  } as never);
  queryClient.setQueryData(chatKeys.detail(chatId), {
    id: chatId,
    projectId: "p1",
    title: "t",
  });
}

// Begin a tool call whose input never arrives — the shape a pause/cancel
// interrupts, and exactly what leaves "Preparing..." on screen.
function startToolCallWithoutInput(chatId: string, messageId: string) {
  useChatStore.getState().processChatStreamUpdates(chatId, [
    {
      update_type: "streaming_delta",
      delta_type: "content_block_start",
      block_index: 0,
      message_id: messageId,
      content_block_type: "tool_use",
      tool_name: "go_symbol_references",
      stream_seq: 1,
    } as unknown as ChatUpdate,
  ]);
}

describe("pause/cancel clear stranded tool-call placeholders", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("pauseChat drops a half-streamed tool call", async () => {
    const chatId = "chat-pause-preparing";
    seed(chatId);
    startToolCallWithoutInput(chatId, "m-pause");

    expect(
      useChatStore.getState().streamingMessages[chatId],
      "precondition: the interrupted tool call is in the streaming slice",
    ).toBeTruthy();

    await useChatStore.getState().pauseChat(chatId);

    expect(
      useChatStore.getState().streamingMessages[chatId],
      "pauseChat must clear the placeholder it orphaned — the snapshot " +
        "self-heal cannot, because a paused chat is still RUNNING",
    ).toBeUndefined();
  });

  it("cancelChat drops a half-streamed tool call", async () => {
    const chatId = "chat-cancel-preparing-e2e";
    seed(chatId);
    startToolCallWithoutInput(chatId, "m-cancel");

    expect(useChatStore.getState().streamingMessages[chatId]).toBeTruthy();

    await useChatStore.getState().cancelChat(chatId);

    expect(
      useChatStore.getState().streamingMessages[chatId],
    ).toBeUndefined();
  });
});
