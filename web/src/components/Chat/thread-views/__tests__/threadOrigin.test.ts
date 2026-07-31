import { describe, it, expect } from "vitest";
import { renderHook } from "@testing-library/react";

import { useThreads } from "../useThreads";
import { isSpawnOrigin } from "../threadUtils";
import type { WorkflowExecution } from "../../ExecutionSidebar/types";
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
});
