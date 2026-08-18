import { describe, it, expect } from "vitest";
import { renderHook } from "@testing-library/react";

import { useThreads } from "../useThreads";
import { isSpawnOrigin } from "../threadUtils";
import type { WorkflowExecution } from "../../ExecutionSidebar/types";
import type { ActiveThreadUpdate } from "../../../../types/streaming";
import { useActivityStore } from "../../../../store/activityStore";
import { useThreadActivityStore } from "../../../../store/threadActivityStore";

const CHAT = "chat-1";
const SPAWN_THREAD = "thread-spawn";

function wf(overrides: Partial<WorkflowExecution>): WorkflowExecution {
  return {
    id: "wf-1",
    workflowName: "builtin://agent",
    thread: CHAT,
    status: "running",
    createdAt: 0,
    messageCount: 0,
    children: [],
    steps: [],
    ...overrides,
  };
}

describe("isSpawnOrigin", () => {
  it("recognizes only the spawn origin", () => {
    expect(isSpawnOrigin("spawn")).toBe(true);
    expect(isSpawnOrigin("node")).toBe(false);
    expect(isSpawnOrigin("fork")).toBe(false);
    expect(isSpawnOrigin("main")).toBe(false);
    expect(isSpawnOrigin(undefined)).toBe(false);
  });
});

describe("useThreads spawn classification", () => {
  beforeEach(() => {
    useActivityStore.setState({ entries: new Map(), activities: new Map() });
    useThreadActivityStore.setState({ threads: {} });
  });

  /**
   * The regression this whole change exists for.
   *
   * A spawn-tool child used to be recognized by comparing a workflow row's
   * spawnedByNodeId against the sentinel "spawn_tool". The inline executor
   * then wrote a SECOND workflow row for the same thread — named
   * "thread:<node>", with spawnedByNodeId set to that node — and whichever row
   * a reader saw last decided the answer. Reading the second row classified a
   * spawned sub-agent as an ordinary workflow-node thread, and the timeline
   * rendered it inline instead of collapsing it into its spawn tool call.
   *
   * Origin is a property of the thread, so every workflow row on that thread
   * reports the same value and the ordering cannot matter.
   */
  it("classifies a spawn thread as a spawn even when a second workflow row shares the thread", () => {
    const execution = wf({
      thread: CHAT,
      children: [
        wf({
          id: "wf-spawn",
          thread: SPAWN_THREAD,
          origin: "spawn",
          parentThread: CHAT,
          threadTitle: "Researcher",
          // The row that used to win the map write and mislabel the thread.
          children: [
            wf({
              id: "wf-inline",
              thread: SPAWN_THREAD,
              workflowName: "thread:spawn-toolu_01",
              origin: "spawn",
              spawnedByNodeId: "spawn-toolu_01",
            }),
          ],
        }),
      ],
    });

    const { result } = renderHook(() => useThreads([], CHAT, execution));

    const spawn = result.current.find((t) => t.id === SPAWN_THREAD);
    expect(spawn).toBeDefined();
    expect(spawn!.isSpawn).toBe(true);
  });

  it("does not classify a workflow-node thread as a spawn", () => {
    const execution = wf({
      thread: CHAT,
      children: [
        wf({
          id: "wf-node",
          thread: "thread-node",
          origin: "node",
          originNodeId: "review_step",
          parentThread: CHAT,
        }),
      ],
    });

    const { result } = renderHook(() => useThreads([], CHAT, execution));

    const node = result.current.find((t) => t.id === "thread-node");
    expect(node).toBeDefined();
    expect(node!.isSpawn).toBe(false);
  });

  it("keeps running spawn threads classified from active-thread updates when a node thread exists", () => {
    useThreadActivityStore.getState().setThreads(CHAT, [
      {
        update_type: "thread",
        id: SPAWN_THREAD,
        chat_id: CHAT,
        thread: SPAWN_THREAD,
        origin: "spawn",
        status: "running",
        is_planning_mode: false,
        thread_title: "Researcher",
        created_at: "2026-01-01T00:00:00.000Z",
      } as ActiveThreadUpdate,
      {
        update_type: "thread",
        id: "thread-node",
        chat_id: CHAT,
        thread: "thread-node",
        origin: "node",
        origin_node_id: "review_step",
        status: "running",
        is_planning_mode: false,
        thread_title: "Review Step",
        created_at: "2026-01-01T00:00:01.000Z",
      } as ActiveThreadUpdate,
    ]);

    const execution = wf({
      thread: CHAT,
      children: [
        wf({
          id: "wf-node",
          thread: "thread-node",
          origin: "node",
          originNodeId: "review_step",
          parentThread: CHAT,
        }),
      ],
    });

    const { result } = renderHook(() => useThreads([], CHAT, execution));

    const spawn = result.current.find((t) => t.id === SPAWN_THREAD);
    const node = result.current.find((t) => t.id === "thread-node");

    expect(spawn).toBeDefined();
    expect(spawn!.isSpawn).toBe(true);
    expect(node).toBeDefined();
    expect(node!.isSpawn).toBe(false);
  });

  it("still visits spawn children when multiple root workflows share the main thread", () => {
    const execution = wf({
      id: "wf-latest-root",
      thread: CHAT,
      origin: "main",
      children: [
        wf({
          id: "wf-node",
          thread: "thread-node",
          origin: "node",
          originNodeId: "review_step",
          parentThread: CHAT,
        }),
      ],
    });

    const olderRoot = wf({
      id: "wf-older-root",
      thread: CHAT,
      origin: "main",
      children: [
        wf({
          id: "wf-spawn-older",
          thread: SPAWN_THREAD,
          origin: "spawn",
          parentThread: CHAT,
          threadTitle: "Researcher",
        }),
      ],
    });

    const { result } = renderHook(() => useThreads([], CHAT, [execution, olderRoot]));

    const spawn = result.current.find((t) => t.id === SPAWN_THREAD);
    const node = result.current.find((t) => t.id === "thread-node");

    expect(node).toBeDefined();
    expect(node!.isSpawn).toBe(false);
    expect(spawn).toBeDefined();
    expect(spawn!.isSpawn).toBe(true);
  });
});
