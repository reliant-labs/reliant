import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { UserUpdateType, EntityType } from "../../gen/reliant/v1/streaming_pb";
import type { UserUpdate } from "../../types/streaming";

// Approvals, questions and waiting-for-machine changes arrive as
// chat_activity_changed; a run that stops (and with it an automation's health)
// arrives as chat_state_change. Both must mark the Inbox and its badge stale —
// the Inbox does not poll.

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
import { inboxKeys } from "../../hooks/inbox-queries";
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

beforeEach(() => {
  queryClient.clear();
  queryClient.setQueryData(inboxKeys.list(), { items: [] });
  queryClient.setQueryData(inboxKeys.counts(), { blockingCount: 0, hasInformational: false });
});

afterEach(() => queryClient.clear());

describe("globalUpdatesStore → Inbox", () => {
  it.each([
    [UserUpdateType.CHAT_ACTIVITY_CHANGED, { chat_id: "c-automation", activity: 2 }],
    [UserUpdateType.CHAT_STATE_CHANGE, { state: 1, previous_state: 1, reason: "workflow_failed" }],
  ] as const)("update %s marks the inbox list and the badge stale", (type, data) => {
    useGlobalUpdatesStore.getState().handleUpdate([buildUpdate(type, data)]);
    expect(queryClient.getQueryState(inboxKeys.list())?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(inboxKeys.counts())?.isInvalidated).toBe(true);
  });

  it("a title change leaves the inbox alone", () => {
    useGlobalUpdatesStore
      .getState()
      .handleUpdate([buildUpdate(UserUpdateType.CHAT_TITLE_CHANGED, { title: "x" })]);
    expect(queryClient.getQueryState(inboxKeys.list())?.isInvalidated).toBe(false);
  });
});
