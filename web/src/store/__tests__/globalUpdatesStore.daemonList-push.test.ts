/**
 * The daemon list is kept fresh by the user stream, not a fast poll. These pin
 * each signal that must reach the list:
 *
 *   - the gateway's `daemons` refetch (attach, detach, lifecycle change);
 *   - a daemon's heartbeats going silent, which is the only trace a gateway
 *     that died outright leaves — the registry flips the daemon to offline
 *     90s later with no event of its own;
 *   - the stream reconnecting, because the announcements are ephemeral and
 *     anything sent while it was down is gone.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { EntityType, UserUpdateType } from "../../gen/reliant/v1/streaming_pb";
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
import { useChatStore } from "../chatStore";
import { resetDaemonHeartbeatWatchdogs, useGlobalUpdatesStore } from "../globalUpdatesStore";

const GATE_KEY = ["onboarding", "daemons", "gate", 123] as const;

function update(type: UserUpdateType, data: Record<string, unknown>, entityId = ""): UserUpdate {
  return {
    id: `u-${Math.random()}`,
    user_id: "user-1",
    sequence_number: 0,
    chat_id: "",
    project_id: "",
    update_type: type,
    entity_type: EntityType.SYSTEM,
    entity_id: entityId,
    data,
    created_at: new Date().toISOString(),
  };
}

const heartbeat = (daemonId: string) =>
  update(UserUpdateType.DAEMON_HEARTBEAT, { daemon_id: daemonId, last_heartbeat: 1 }, daemonId);

function listInvalidated(): boolean | undefined {
  return queryClient.getQueryState(DAEMON_LIST_QUERY_KEY)?.isInvalidated;
}

beforeEach(() => {
  vi.useFakeTimers();
  queryClient.clear();
  queryClient.setQueryData(DAEMON_LIST_QUERY_KEY, []);
  queryClient.setQueryData(GATE_KEY, []);
  resetDaemonHeartbeatWatchdogs();
});

afterEach(() => {
  resetDaemonHeartbeatWatchdogs();
  queryClient.clear();
  vi.useRealTimers();
});

describe("daemon list push", () => {
  it("a `daemons` refetch marks every daemon list stale, the onboarding gate's included", () => {
    useGlobalUpdatesStore
      .getState()
      .handleUpdate([update(UserUpdateType.REFETCH, { type: "daemons", daemon_id: "d1" }, "d1")]);

    expect(listInvalidated()).toBe(true);
    expect(queryClient.getQueryState(GATE_KEY)?.isInvalidated).toBe(true);
  });

  it("other refetch types leave the daemon list alone", () => {
    useGlobalUpdatesStore
      .getState()
      .handleUpdate([update(UserUpdateType.REFETCH, { type: "worktree_changes" }, "wt-1")]);

    expect(listInvalidated()).toBe(false);
  });

  it("re-reads the list once a heartbeating daemon goes silent past the registry's lease", () => {
    const store = useGlobalUpdatesStore.getState();
    store.handleUpdate([heartbeat("d1")]);

    // Heartbeats every 15s keep pushing the deadline out.
    for (let i = 0; i < 6; i++) {
      vi.advanceTimersByTime(15_000);
      store.handleUpdate([heartbeat("d1")]);
    }
    expect(listInvalidated()).toBe(false);

    // Then they stop. The registry keeps reporting it attached for 90s.
    vi.advanceTimersByTime(90_000);
    expect(listInvalidated()).toBe(false);

    vi.advanceTimersByTime(5_000);
    expect(listInvalidated()).toBe(true);
  });

  it("refreshes the daemon list after the stream reconnects from a disruption", () => {
    vi.spyOn(useChatStore.getState(), "loadChats").mockResolvedValue(undefined as never);
    const store = useGlobalUpdatesStore.getState();

    store.handleStatusChange("error");
    expect(listInvalidated()).toBe(false);

    store.handleStatusChange("connected");
    expect(listInvalidated()).toBe(true);
  });
});
