import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ChatState } from "../../gen/reliant/v1/chat_pb";
import { UserUpdateType, EntityType } from "../../gen/reliant/v1/streaming_pb";
import type { UserUpdate } from "../../types/streaming";

vi.mock("../../api/streaming-grpc", () => ({
  UserStreamingService: vi.fn().mockImplementation(() => ({
    isConnected: () => false,
    disconnect: vi.fn(),
    subscribeToChatDetails: vi.fn(),
    unsubscribeFromChatDetails: vi.fn(),
  })),
}));

vi.mock("../../lib/notifications", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/notifications")>();
  return {
    ...actual,
    showWorkflowCompletionNotification: vi.fn(),
    showWorkflowFailedNotification: vi.fn(),
    showApprovalRequiredNotification: vi.fn(),
    getNotificationPermission: () => "granted" as const,
  };
});

import {
  showWorkflowCompletionNotification,
  showWorkflowFailedNotification,
} from "../../lib/notifications";
import { queryClient } from "../../lib/query-client";
import { useChatStore } from "../chatStore";
import { useGlobalUpdatesStore } from "../globalUpdatesStore";

let counter = 0;
function stateChange(data: Record<string, unknown>): UserUpdate {
  counter += 1;
  return {
    id: `fail-update-${counter}`,
    user_id: "user-1",
    sequence_number: counter,
    chat_id: "c1",
    project_id: "",
    update_type: UserUpdateType.CHAT_STATE_CHANGE,
    entity_type: EntityType.CHAT,
    entity_id: "c1",
    data: { state: ChatState.IDLE, previous_state: ChatState.IDLE, title: "Hourly sync", ...data },
    created_at: new Date(Date.now() + 60_000).toISOString(),
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  useChatStore.setState({ activeChatId: null });
});

afterEach(() => {
  queryClient.clear();
});

describe("failure notifications", () => {
  it("workflow_failed + unread raises the failure notification, not the completion one", () => {
    useGlobalUpdatesStore.getState().handleUpdate([stateChange({ reason: "workflow_failed", unread: true })]);
    expect(showWorkflowFailedNotification).toHaveBeenCalledTimes(1);
    expect((showWorkflowFailedNotification as any).mock.calls[0][1]).toBe("Hourly sync");
    expect(showWorkflowCompletionNotification).not.toHaveBeenCalled();
  });

  it("workflow_failed without unread raises nothing", () => {
    useGlobalUpdatesStore.getState().handleUpdate([stateChange({ reason: "workflow_failed", unread: false })]);
    expect(showWorkflowFailedNotification).not.toHaveBeenCalled();
    expect(showWorkflowCompletionNotification).not.toHaveBeenCalled();
  });

  it("workflow_completed still raises the completion notification", () => {
    useGlobalUpdatesStore.getState().handleUpdate([stateChange({ reason: "workflow_completed", unread: true })]);
    expect(showWorkflowCompletionNotification).toHaveBeenCalledTimes(1);
    expect(showWorkflowFailedNotification).not.toHaveBeenCalled();
  });
});
