import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { UserUpdateType, EntityType } from "../../gen/reliant/v1/streaming_pb";
import type { UserUpdate } from "../../types/streaming";

// A config_health refetch event (a config file changed on disk) is the
// stream's nudge for ConfigHealthIndicator, which reads GetConfigHealth
// through the generated hook: it invalidates settingsServiceKeys.all.

vi.mock("../../api/streaming-grpc", () => ({
  UserStreamingService: vi.fn().mockImplementation(() => ({
    isConnected: () => false,
    disconnect: vi.fn(),
    subscribeToChatDetails: vi.fn(),
    unsubscribeFromChatDetails: vi.fn(),
  })),
}));

import { queryClient } from "../../lib/query-client";
import { settingsServiceKeys } from "../../hooks/settings-service-hooks_gen";
import { useGlobalUpdatesStore } from "../globalUpdatesStore";

const refetch = (type: string): UserUpdate => ({
  id: `u-${type}`,
  user_id: "user-1",
  sequence_number: 1,
  chat_id: "",
  project_id: "",
  update_type: UserUpdateType.REFETCH,
  entity_type: EntityType.CHAT,
  entity_id: "",
  data: { type },
  created_at: new Date().toISOString(),
});

const healthKey = settingsServiceKeys.getConfigHealth({ projectId: "p1" });

beforeEach(() => {
  queryClient.clear();
  queryClient.setQueryData(healthKey, { errors: [], errorCount: 0, warningCount: 0 });
});

afterEach(() => queryClient.clear());

describe("globalUpdatesStore → SettingsService", () => {
  it("a config_health refetch invalidates settingsServiceKeys.all", () => {
    useGlobalUpdatesStore.getState().handleUpdate([refetch("config_health")]);
    expect(queryClient.getQueryState(healthKey)?.isInvalidated).toBe(true);
  });

  it("an unrelated refetch leaves config health alone", () => {
    useGlobalUpdatesStore.getState().handleUpdate([refetch("file_tree")]);
    expect(queryClient.getQueryState(healthKey)?.isInvalidated).toBe(false);
  });
});
