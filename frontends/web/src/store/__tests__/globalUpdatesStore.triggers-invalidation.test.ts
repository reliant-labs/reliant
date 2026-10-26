import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { UserUpdateType, EntityType } from "../../gen/reliant/v1/streaming_pb";
import type { UserUpdate } from "../../types/streaming";

// A fire that launched a run arrives as chat_created, and it moves the
// automation's last event, health and next fire. The update names no
// trigger, so the stream marks every trigger read stale.

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
import { triggerServiceKeys } from "../../hooks/trigger-service-hooks_gen";
import { resetStreamInvalidation, useGlobalUpdatesStore } from "../globalUpdatesStore";

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

// The every-project list (Automations, the sidebar's names), one automation's
// detail and its history.
const listKey = triggerServiceKeys.listTriggers({});
const detailKey = triggerServiceKeys.getTrigger({ id: "trg-1" });
const eventsKey = triggerServiceKeys.listTriggerEvents({ triggerId: "trg-1", limit: 50 });
const triggerReads = [listKey, detailKey, eventsKey];

beforeEach(() => {
  queryClient.clear();
  queryClient.setQueryData(listKey, { triggers: [] });
  queryClient.setQueryData(detailKey, {});
  queryClient.setQueryData(eventsKey, { events: [] });
});

afterEach(() => {
  resetStreamInvalidation();
  queryClient.clear();
});

describe("globalUpdatesStore → trigger reads", () => {
  it("chat_created marks every trigger read stale", () => {
    for (const key of triggerReads) expect(queryClient.getQueryState(key)?.isInvalidated).toBe(false);
    useGlobalUpdatesStore
      .getState()
      .handleUpdate([buildUpdate(UserUpdateType.CHAT_CREATED, { chat_id: "c-automation", title: "Nightly" })]);
    for (const key of triggerReads) expect(queryClient.getQueryState(key)?.isInvalidated).toBe(true);
  });

  it.each([
    [UserUpdateType.CHAT_STATE_CHANGE, { state: 1, previous_state: 1, reason: "workflow_completed" }],
    [UserUpdateType.CHAT_ACTIVITY_CHANGED, { chat_id: "c-automation", activity: 1 }],
    [UserUpdateType.CHAT_TITLE_CHANGED, { title: "x" }],
  ] as const)("update %s leaves the trigger reads alone (they poll)", (type, data) => {
    useGlobalUpdatesStore.getState().handleUpdate([buildUpdate(type, data)]);
    for (const key of triggerReads) expect(queryClient.getQueryState(key)?.isInvalidated).toBe(false);
  });
});
