/**
 * The reconnect sweep. REFETCH signals are ephemeral: one sent while the
 * stream is down is never replayed, so on reconnect every cache the stream
 * invalidates is re-read once — the reads made before the outage, which are
 * the ones that can have missed a signal. Without the sweep the only catch-up
 * is a window-focus refetch of an entry already past its staleTime, which a
 * tab that stays focused through a network blip never gets.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { QueryObserver, type QueryKey } from "@tanstack/react-query";
import { EntityType, UserUpdateType } from "../../gen/reliant/v1/streaming_pb";
import { ChatActivity } from "../../gen/reliant/v1/chat_pb";
import type { ChatUpdate, UserUpdate } from "../../types/streaming";
import type { RefetchType } from "../refetchStore";

vi.mock("../../api/streaming-grpc", () => ({
  UserStreamingService: vi.fn().mockImplementation(() => ({
    isConnected: () => false,
    disconnect: vi.fn(),
    subscribeToChatDetails: vi.fn(),
    unsubscribeFromChatDetails: vi.fn(),
  })),
}));

import { queryClient } from "../../lib/query-client";
import { useChatStore } from "../chatStore";
import { resetStreamInvalidation, useGlobalUpdatesStore } from "../globalUpdatesStore";
import { useGlobalDataStore } from "../globalDataStore";
import { STREAM_INVALIDATED_SCOPES, type StreamInvalidatedScope } from "../streamInvalidatedScopes";

const ROOTS: Record<string, QueryKey> = STREAM_INVALIDATED_SCOPES;

const unsubscribers: Array<() => void> = [];

/** A mounted read under `root`, as a component's useQuery would hold it. */
function mountRead(root: QueryKey, tag: string) {
  const queryFn = vi.fn(async () => ({ tag, at: Date.now() }));
  const observer = new QueryObserver(queryClient, {
    queryKey: [...root, "reconnect-probe", tag],
    queryFn,
    staleTime: Infinity,
    retry: false,
  });
  unsubscribers.push(observer.subscribe(() => {}));
  return queryFn;
}

/** Let due timers fire and the fetches they start resolve. */
async function settle() {
  for (let i = 0; i < 5; i++) await vi.advanceTimersByTimeAsync(0);
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(1_000_000);
  queryClient.clear();
  resetStreamInvalidation();
  vi.spyOn(useChatStore.getState(), "loadChats").mockResolvedValue(undefined as never);
});

afterEach(async () => {
  while (unsubscribers.length) unsubscribers.pop()!();
  resetStreamInvalidation();
  queryClient.clear();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("globalUpdatesStore reconnect sweep", () => {
  it.each(Object.entries(ROOTS))(
    "a reconnect after an outage refetches a mounted read under %s",
    async (_name, root) => {
      const queryFn = mountRead(root, "before");
      await settle();
      expect(queryFn).toHaveBeenCalledTimes(1);

      const store = useGlobalUpdatesStore.getState();
      vi.advanceTimersByTime(1_000);
      store.handleStatusChange("error");
      vi.advanceTimersByTime(5_000);
      store.handleStatusChange("connecting");
      store.handleStatusChange("connected");
      await settle();

      expect(queryFn).toHaveBeenCalledTimes(2);
    },
  );

  it("re-reads only what was read before the outage, and once per reconnect", async () => {
    const before = Object.entries(ROOTS).map(([name, root]) => mountRead(root, `${name}-before`));
    await settle();

    const store = useGlobalUpdatesStore.getState();
    vi.advanceTimersByTime(1_000);
    store.handleStatusChange("error");
    vi.advanceTimersByTime(1_000);
    // Read after the drop (the window-focus refetch usually beats the
    // reconnect): already current, so the sweep leaves it alone.
    const after = Object.entries(ROOTS).map(([name, root]) => mountRead(root, `${name}-after`));
    await settle();
    vi.advanceTimersByTime(1_000);

    store.handleStatusChange("connected");
    // A second "connected" without a new disruption sweeps nothing more.
    store.handleStatusChange("connected");
    await settle();
    vi.advanceTimersByTime(10_000);
    await settle();

    for (const queryFn of before) expect(queryFn).toHaveBeenCalledTimes(2);
    for (const queryFn of after) expect(queryFn).toHaveBeenCalledTimes(1);
  });

  it("an undisturbed stream that connects sweeps nothing", async () => {
    const reads = Object.values(ROOTS).map((root) => mountRead(root, "steady"));
    await settle();

    useGlobalUpdatesStore.getState().handleStatusChange("connected");
    await settle();

    for (const queryFn of reads) expect(queryFn).toHaveBeenCalledTimes(1);
  });
});

// ── Guards: nothing the stream invalidates is missing from the registry ─────

/** Every refetch type the server sends; a new one fails to compile here. */
const REFETCH_TYPES = Object.keys({
  worktree_changes: true,
  workflow_executions: true,
  config_health: true,
  plan_tasks: true,
  file_tree: true,
  agent_mailbox: true,
} satisfies Record<RefetchType, true>) as RefetchType[];

/** The refetch types handled before refetchStore (not RefetchType members). */
const STORE_ONLY_REFETCH_TYPES = ["daemons", "daemon_local_models", "inbox"];

function userUpdate(type: UserUpdateType, data: Record<string, unknown>): UserUpdate {
  return {
    id: `u-${Math.random()}`,
    user_id: "user-1",
    sequence_number: 0,
    chat_id: "chat-1",
    project_id: "project-1",
    worktree_id: "worktree-1",
    update_type: type,
    entity_type: EntityType.CHAT,
    entity_id: "entity-1",
    data,
    created_at: new Date().toISOString(),
  };
}

/** One payload per handler branch that invalidates something. */
function everyStreamEvent(): UserUpdate[] {
  const kitchenSink = {
    state: "idle",
    previous_state: "idle",
    reason: "",
    activity: ChatActivity.RUNNING,
    chat_id: "chat-1",
    title: "t",
    process_id: "proc-1",
    command: "cmd",
    working_dir: "/",
    status: "running",
    start_time: "",
    daemon_id: "daemon-1",
    last_heartbeat: 1,
    draft_id: "draft-1",
    slug: "s",
    version: 1,
  };
  const types = Object.values(UserUpdateType).filter((v): v is UserUpdateType => typeof v === "number");
  return [
    ...types.map((type) => userUpdate(type, kitchenSink)),
    userUpdate(UserUpdateType.CHAT_STATE_CHANGE, { ...kitchenSink, state: "archived", previous_state: "idle" }),
    userUpdate(UserUpdateType.CHAT_STATE_CHANGE, { ...kitchenSink, state: "idle", previous_state: "archived" }),
    userUpdate(UserUpdateType.CHAT_ACTIVITY_CHANGED, { ...kitchenSink, activity: ChatActivity.WAITING_FOR_USAGE_LIMIT }),
    userUpdate(UserUpdateType.CHAT_ACTIVITY_CHANGED, { ...kitchenSink, activity: ChatActivity.IDLE }),
    ...[...REFETCH_TYPES, ...STORE_ONLY_REFETCH_TYPES].map((type) =>
      userUpdate(UserUpdateType.REFETCH, { type }),
    ),
  ];
}

function isUnderRoot(key: QueryKey, root: QueryKey): boolean {
  return root.length <= key.length && root.every((part, i) => JSON.stringify(part) === JSON.stringify(key[i]));
}

describe("STREAM_INVALIDATED_SCOPES covers every stream handler", () => {
  it("every query key a user- or chat-stream event invalidates is under a registered root", async () => {
    vi.spyOn(useGlobalDataStore.getState(), "refetchModels").mockResolvedValue(undefined as never);
    const invalidated: Array<QueryKey | undefined> = [];
    const spy = vi.spyOn(queryClient, "invalidateQueries");
    spy.mockImplementation(async (filters) => {
      invalidated.push(filters?.queryKey);
    });

    const store = useGlobalUpdatesStore.getState();
    for (const event of everyStreamEvent()) store.handleUpdate([event]);
    // The chat stream's refetch signals (chatStore) reach the same subscribers.
    useGlobalUpdatesStore.setState({ subscribedChatId: "chat-1" });
    store.handleChatUpdate(
      REFETCH_TYPES.map((type) => ({ update_type: "refetch", type }) as unknown as ChatUpdate),
    );
    // refetchStore debounces before notifying its subscribers.
    await vi.advanceTimersByTimeAsync(1_000);
    await settle();
    useGlobalUpdatesStore.setState({ subscribedChatId: null });

    const roots = Object.values(STREAM_INVALIDATED_SCOPES);
    const unregistered = invalidated.filter((key) => !key || !roots.some((root) => isUnderRoot(key, root)));
    expect(unregistered).toEqual([]);
    // The drive reached the handlers at all (a vacuous pass otherwise).
    for (const root of [
      STREAM_INVALIDATED_SCOPES.worktrees,
      STREAM_INVALIDATED_SCOPES.projects,
      STREAM_INVALIDATED_SCOPES.settings,
      STREAM_INVALIDATED_SCOPES.plans,
      STREAM_INVALIDATED_SCOPES.daemonRegistry,
    ]) {
      expect(invalidated.some((key) => key && isUnderRoot(key, root))).toBe(true);
    }
  });

  it("every refetchStore subscriber is accounted for", () => {
    // Subscribers mounted by hooks are not reached by the drive above, so each
    // one is listed with the registry scope its callback invalidates — or why
    // it holds no query cache. A new subscriber fails here until it is.
    const SUBSCRIBERS: Record<string, StreamInvalidatedScope | { noQueryCache: string }> = {
      "hooks/worktree-queries.ts": "worktrees",
      "hooks/useWorkflowExecutions.ts": "chatService",
      "hooks/queued-agent-messages.ts": "chatService",
      "components/Chat/changes/useRepoChanges.ts": { noQueryCache: "component state, with its own fallback poll" },
      "components/FileBrowser/useFileTreePushRefresh.ts": { noQueryCache: "the file tree store, refreshed on show" },
    };

    const src = join(__dirname, "..", "..");
    const found: string[] = [];
    const walk = (dir: string) => {
      for (const entry of readdirSync(dir)) {
        const path = join(dir, entry);
        if (statSync(path).isDirectory()) {
          if (entry !== "__tests__" && entry !== "gen" && entry !== "test") walk(path);
          continue;
        }
        if (!/\.tsx?$/.test(entry) || /\.test\.tsx?$/.test(entry)) continue;
        const rel = relative(src, path).split("\\").join("/");
        if (rel === "store/refetchStore.ts") continue;
        if (readFileSync(path, "utf8").includes("subscribeToRefetch(")) found.push(rel);
      }
    };
    walk(src);

    expect(found.sort()).toEqual(Object.keys(SUBSCRIBERS).sort());
    for (const scope of Object.values(SUBSCRIBERS)) {
      if (typeof scope === "string") expect(STREAM_INVALIDATED_SCOPES).toHaveProperty(scope);
    }
  });
});
