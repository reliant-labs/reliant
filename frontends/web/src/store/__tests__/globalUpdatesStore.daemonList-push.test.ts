/**
 * The daemon list is kept fresh by the user stream, not a fast poll. These pin
 * each signal that must reach the list:
 *
 *   - the gateway's `daemons` refetch (attach, detach, lifecycle change);
 *   - a daemon's heartbeats going silent, which is the only trace a gateway
 *     that died outright leaves — the registry flips the daemon to offline
 *     90s later with no event of its own;
 *   - the stream reconnecting, because the announcements are ephemeral and
 *     anything sent while it was down is gone;
 *   - a heartbeat from a daemon the cached list does not show as connected,
 *     which is proof the list is stale (prod 2026-10-11: a serving machine
 *     read "disconnected" for hours and nothing made the list re-read).
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { EntityType, UserUpdateType } from "../../gen/reliant/v1/streaming_pb";
import type { UserUpdate } from "../../types/streaming";

vi.mock("../../api/streaming-grpc", () => ({
  UserStreamingService: vi.fn().mockImplementation(() => ({
    isConnected: () => false,
    disconnect: vi.fn(),
  })),
}));

import { queryClient } from "../../lib/query-client";
import { resetDaemonHeartbeatReconcile } from "../../hooks/useDaemonStatus";
import { DAEMON_LIST_QUERY_KEY } from "../../hooks/daemon-registry-queries";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import {
  DaemonStatus,
  ListDaemonsResponseSchema,
  type DaemonInfoSchema,
} from "../../gen/reliant/v1/daemon_registry_pb";
import { useChatStore } from "../chatStore";
import { resetDaemonHeartbeatWatchdogs, useGlobalUpdatesStore } from "../globalUpdatesStore";

/** Seed the one daemon list, as a read of ListDaemons would leave it. */
function seedList(daemons: MessageInitShape<typeof DaemonInfoSchema>[] = []) {
  queryClient.setQueryData(DAEMON_LIST_QUERY_KEY, create(ListDaemonsResponseSchema, { daemons }));
}

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
  seedList();
  resetDaemonHeartbeatWatchdogs();
  resetDaemonHeartbeatReconcile();
});

afterEach(() => {
  resetDaemonHeartbeatWatchdogs();
  queryClient.clear();
  vi.useRealTimers();
});

describe("daemon list push", () => {
  it("a `daemons` refetch marks the daemon list stale", () => {
    useGlobalUpdatesStore
      .getState()
      .handleUpdate([update(UserUpdateType.REFETCH, { type: "daemons", daemon_id: "d1" }, "d1")]);

    expect(listInvalidated()).toBe(true);
  });

  it("other refetch types leave the daemon list alone", () => {
    useGlobalUpdatesStore
      .getState()
      .handleUpdate([update(UserUpdateType.REFETCH, { type: "worktree_changes" }, "wt-1")]);

    expect(listInvalidated()).toBe(false);
  });

  it("re-reads the list once a heartbeating daemon goes silent past the registry's lease", () => {
    // A heartbeating daemon is one the list shows attached.
    seedList([{ daemonId: "d1", status: DaemonStatus.ACTIVE }]);
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

  it("re-reads the list when a daemon it calls disconnected heartbeats", () => {
    seedList([{ daemonId: "d1", status: DaemonStatus.DISCONNECTED }]);

    useGlobalUpdatesStore.getState().handleUpdate([heartbeat("d1")]);

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

  it("does not re-read on reconnect what the user already re-read since the stream dropped", () => {
    // Returning to the tab: the window-focus refetch lands before the stream
    // finishes reconnecting. Re-reading the same lists again on "connected"
    // was the duplicate half of every resume burst.
    vi.spyOn(useChatStore.getState(), "loadChats").mockResolvedValue(undefined as never);
    const store = useGlobalUpdatesStore.getState();
    const CHAT_LIST = ["chats", "list", "p1"] as const;
    const ARCHIVED = ["chats", "archived"] as const;
    queryClient.setQueryData(ARCHIVED, []);

    vi.advanceTimersByTime(1_000);
    store.handleStatusChange("error");

    vi.advanceTimersByTime(1_000);
    seedList();
    queryClient.setQueryData(CHAT_LIST, { chats: [], lastUserUpdateSequence: 0 });

    store.handleStatusChange("connected");
    expect(listInvalidated()).toBe(false);
    expect(queryClient.getQueryState(CHAT_LIST)?.isInvalidated).toBe(false);
    // Read before the drop: may have missed an ephemeral signal.
    expect(queryClient.getQueryState(ARCHIVED)?.isInvalidated).toBe(true);
  });
});
