import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { UserUpdateType, EntityType } from "../../gen/reliant/v1/streaming_pb";
import { ChatActivity } from "../../gen/reliant/v1/chat_pb";
import type { UserUpdate } from "../../types/streaming";

// A chat going IDLE is the stream's "reconcile approvals" nudge: the event
// does not carry the resolved approvals, so the generated ApprovalService
// reads are marked stale under approvalServiceKeys.all and refetch.

vi.mock("../../api/streaming-grpc", () => ({
  UserStreamingService: vi.fn().mockImplementation(() => ({
    isConnected: () => false,
    disconnect: vi.fn(),
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
import { approvalServiceKeys } from "../../hooks/approval-service-hooks_gen";
import { useGlobalUpdatesStore } from "../globalUpdatesStore";

function activityChanged(chatId: string, activity: ChatActivity, seq: number): UserUpdate {
  return {
    id: `u-${seq}`,
    user_id: "user-1",
    sequence_number: seq,
    chat_id: chatId,
    project_id: "p1",
    update_type: UserUpdateType.CHAT_ACTIVITY_CHANGED,
    entity_type: EntityType.CHAT,
    entity_id: chatId,
    data: { chat_id: chatId, activity },
    created_at: new Date().toISOString(),
  };
}

const listKey = (chatId: string) => approvalServiceKeys.listApprovalsByChat({ chatId });

beforeEach(() => {
  queryClient.clear();
  queryClient.setQueryData(listKey("c1"), { approvals: [], total: 0 });
});

afterEach(() => queryClient.clear());

describe("globalUpdatesStore → ApprovalService", () => {
  it("a chat going idle invalidates approvalServiceKeys.all", () => {
    useGlobalUpdatesStore.getState().handleUpdate([activityChanged("c1", ChatActivity.IDLE, 1)]);
    expect(queryClient.getQueryState(listKey("c1"))?.isInvalidated).toBe(true);
  });

  it("a chat that is still running leaves approvals alone", () => {
    useGlobalUpdatesStore.getState().handleUpdate([activityChanged("c1", ChatActivity.RUNNING, 2)]);
    expect(queryClient.getQueryState(listKey("c1"))?.isInvalidated).toBe(false);
  });
});
