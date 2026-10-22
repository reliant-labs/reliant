import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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

import { queryClient } from "../../lib/query-client";
import { DAEMON_LIST_QUERY_KEY } from "../../hooks/useDaemonStatus";
import { useGlobalDataStore } from "../globalDataStore";
import { useGlobalUpdatesStore } from "../globalUpdatesStore";

const refetch = (data: Record<string, unknown>): UserUpdate => ({
  id: "u-refetch",
  user_id: "user-1",
  sequence_number: 1,
  chat_id: "",
  project_id: "",
  update_type: UserUpdateType.REFETCH,
  entity_type: EntityType.CHAT,
  entity_id: "",
  data,
  created_at: new Date().toISOString(),
});

let refetchModels: ReturnType<typeof vi.fn>;

beforeEach(() => {
  queryClient.clear();
  queryClient.setQueryData(DAEMON_LIST_QUERY_KEY, []);
  refetchModels = vi.fn(async () => {});
  useGlobalDataStore.setState({ refetchModels } as never);
});

afterEach(() => queryClient.clear());

describe("globalUpdatesStore daemon_local_models refetch", () => {
  it("marks the daemon list stale and refetches the model list", () => {
    useGlobalUpdatesStore
      .getState()
      .handleUpdate([refetch({ type: "daemon_local_models", daemon_id: "d1" })]);
    expect(queryClient.getQueryState(DAEMON_LIST_QUERY_KEY)?.isInvalidated).toBe(true);
    expect(refetchModels).toHaveBeenCalledTimes(1);
  });

  it("leaves both alone for other refetch types", () => {
    useGlobalUpdatesStore.getState().handleUpdate([refetch({ type: "file_tree" })]);
    expect(queryClient.getQueryState(DAEMON_LIST_QUERY_KEY)?.isInvalidated).toBe(false);
    expect(refetchModels).not.toHaveBeenCalled();
  });
});
