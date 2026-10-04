// Copyright (c) 2025 Reliant Labs

/**
 * Node-status wiring for a builder test run: the new chat id reaches the update
 * subscription, and the statuses that stream in for THAT chat come back keyed
 * by node id. A stray event for another chat must not paint the canvas.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook } from "@testing-library/react";
import type { Node } from "@xyflow/react";

import { useChatStore } from "../../../store/chatStore";
import { useGlobalUpdatesStore } from "../../../store/globalUpdatesStore";
import { NodeExecutionEventType, NodeExecutionStatus } from "../../../gen/reliant/v1/streaming_pb";
import type { NodeExecutionUpdate } from "../../../types/streaming";
import { useBuilderTestRun, withTestRunStatus } from "./useBuilderTestRun";

function event(chatId: string, nodeId: string, eventType: NodeExecutionEventType, seq: number): NodeExecutionUpdate {
  return {
    update_type: "node_execution",
    event_type: eventType,
    node_id: nodeId,
    node_type: "action",
    status: NodeExecutionStatus.RUNNING,
    workflow_id: chatId,
    chat_id: chatId,
    sequence_number: seq,
  } as NodeExecutionUpdate;
}

describe("useBuilderTestRun", () => {
  const subscribe = vi.fn();
  const unsubscribe = vi.fn();

  beforeEach(() => {
    subscribe.mockReset();
    unsubscribe.mockReset();
    useGlobalUpdatesStore.setState({ subscribeToChatDetails: subscribe, unsubscribeFromChatDetails: unsubscribe });
    useChatStore.setState({ nodeExecutions: {} });
  });
  afterEach(() => useChatStore.setState({ nodeExecutions: {} }));

  it("subscribes to the new chat id and does nothing without one", () => {
    const { rerender } = renderHook(({ id }) => useBuilderTestRun(id, ["a"]), { initialProps: { id: null as string | null } });
    expect(subscribe).not.toHaveBeenCalled();

    rerender({ id: "test-chat-1" });
    expect(subscribe).toHaveBeenCalledWith("test-chat-1");
  });

  it("hands the stream back to the builder assistant when the test view goes away", () => {
    const { unmount } = renderHook(() => useBuilderTestRun("test-chat-1", ["a"], "assistant-chat"));
    unmount();
    expect(unsubscribe).toHaveBeenCalledWith("test-chat-1");
    expect(subscribe).toHaveBeenLastCalledWith("assistant-chat");
  });

  it("reports statuses for the test chat's own nodes by node id", () => {
    useChatStore.setState({
      nodeExecutions: {
        "test-chat-1": [
          event("test-chat-1", "a", NodeExecutionEventType.COMPLETED, 2),
          event("test-chat-1", "b", NodeExecutionEventType.STARTED, 3),
          event("test-chat-1", "unknown-node", NodeExecutionEventType.STARTED, 4),
        ],
        "other-chat": [event("other-chat", "c", NodeExecutionEventType.COMPLETED, 1)],
      },
    });

    const { result } = renderHook(() => useBuilderTestRun("test-chat-1", ["a", "b", "c"]));
    expect(result.current).toEqual({ a: "completed", b: "running" });
  });

  it("paints statuses onto nodes and leaves untouched nodes identical", () => {
    const nodes = [
      { id: "a", position: { x: 0, y: 0 }, data: { label: "A" } },
      { id: "b", position: { x: 0, y: 0 }, data: { label: "B" } },
    ] as Node[];
    const painted = withTestRunStatus(nodes, { a: "failed" });
    expect(painted[0]!.data).toMatchObject({ label: "A", executionStatus: "failed" });
    expect(painted[1]).toBe(nodes[1]);
    expect(withTestRunStatus(nodes, {})).toBe(nodes);
  });
});
