import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { UserUpdateType, EntityType } from "../../gen/reliant/v1/streaming_pb";
import type { UserUpdate } from "../../types/streaming";

// The Runs list shows chats the sidebar never lists (automation and
// agent-started runs), so the per-chat cache patches the sidebar relies on
// never reach it. The user-update stream has to mark the runs cache stale.

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
    showApprovalRequiredNotification: vi.fn(),
    getNotificationPermission: () => "denied" as const,
  };
});

import { queryClient } from "../../lib/query-client";
import { runKeys } from "../../hooks/run-queries";
import { useGlobalUpdatesStore } from "../globalUpdatesStore";

function buildUpdate(update_type: UserUpdateType, data: Record<string, unknown>): UserUpdate {
  return {
    id: `u-${update_type}`,
    user_id: "user-1",
    sequence_number: 1,
    chat_id: "c-automation",
    project_id: "p1",
    update_type,
    entity_type: EntityType.CHAT,
    entity_id: "c-automation",
    data,
    created_at: new Date().toISOString(),
  };
}

const listKey = runKeys.list({ kind: ["schedule"] });

beforeEach(() => {
  queryClient.clear();
  queryClient.setQueryData(listKey, { pages: [], pageParams: [] });
});

afterEach(() => queryClient.clear());

describe("globalUpdatesStore → Runs list", () => {
  it.each([
    [UserUpdateType.CHAT_STATE_CHANGE, { state: 1, previous_state: 1, reason: "workflow_completed" }],
    [UserUpdateType.CHAT_ACTIVITY_CHANGED, { chat_id: "c-automation", activity: 1 }],
    [UserUpdateType.CHAT_CREATED, { chat_id: "c-automation", title: "Nightly" }],
  ] as const)("update %s marks every runs list stale", (type, data) => {
    expect(queryClient.getQueryState(listKey)?.isInvalidated).toBe(false);
    useGlobalUpdatesStore.getState().handleUpdate([buildUpdate(type, data)]);
    expect(queryClient.getQueryState(listKey)?.isInvalidated).toBe(true);
  });

  it("a title change does not refetch the runs list", () => {
    useGlobalUpdatesStore
      .getState()
      .handleUpdate([buildUpdate(UserUpdateType.CHAT_TITLE_CHANGED, { title: "x" })]);
    expect(queryClient.getQueryState(listKey)?.isInvalidated).toBe(false);
  });
});
