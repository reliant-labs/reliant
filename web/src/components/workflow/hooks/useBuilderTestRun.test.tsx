// Copyright (c) 2025 Reliant Labs

/**
 * Wiring for a builder test run: the new chat id reaches the update
 * subscription, and what streams in for THAT chat comes back as the canvas's
 * run — each step's state, the path taken. A stray event for another chat
 * must not paint the canvas.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Edge, Node } from "@xyflow/react";

import { useChatStore } from "../../../store/chatStore";
import { useGlobalUpdatesStore } from "../../../store/globalUpdatesStore";
import { useActivityStore } from "../../../store/activityStore";
import { chatKeys } from "../../../hooks/chat-queries";
import { NodeExecutionEventType, NodeExecutionStatus } from "../../../gen/reliant/v1/streaming_pb";
import { ChatActivity, WorkflowState, WorkflowStopReason } from "../../../gen/reliant/v1/chat_pb";
import type { NodeExecutionUpdate } from "../../../types/streaming";

const chatQuery = vi.hoisted(() => ({ data: undefined as unknown }));
vi.mock("../../../hooks/chat-queries", async () => ({
  ...(await vi.importActual<typeof import("../../../hooks/chat-queries")>("../../../hooks/chat-queries")),
  useChat: () => chatQuery,
}));
vi.mock("../../../hooks/useWorkflowExecutions", () => ({
  useWorkflowExecutions: () => ({ allWorkflows: [], data: null, hasRunningWorkflow: false, isLoading: false, error: null }),
}));

import { useBuilderRun } from "./useBuilderTestRun";

function event(
  chatId: string,
  nodeId: string,
  eventType: NodeExecutionEventType,
  seq: number,
  overrides: Partial<NodeExecutionUpdate> = {},
): NodeExecutionUpdate {
  return {
    update_type: "node_execution",
    event_type: eventType,
    node_id: nodeId,
    node_type: "action",
    status: NodeExecutionStatus.RUNNING,
    workflow_id: chatId,
    chat_id: chatId,
    sequence_number: seq,
    ...overrides,
  } as NodeExecutionUpdate;
}

const step = (id: string): Node => ({ id, position: { x: 0, y: 0 }, data: { step: { id } } });
const start: Node = { id: "workflow", type: "eventNode", position: { x: 0, y: 0 }, data: {} };
const edge = (source: string, target: string): Edge => ({ id: `${source}->${target}`, source, target });

let queryClient: QueryClient;
const wrapper = ({ children }: { children: ReactNode }) => (
  <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
);

describe("useBuilderRun", () => {
  const subscribe = vi.fn();
  const unsubscribe = vi.fn();

  beforeEach(() => {
    subscribe.mockReset();
    unsubscribe.mockReset();
    chatQuery.data = undefined;
    queryClient = new QueryClient();
    useGlobalUpdatesStore.setState({ subscribeToChatDetails: subscribe, unsubscribeFromChatDetails: unsubscribe });
    useChatStore.setState({ nodeExecutions: {} });
  });
  afterEach(() => useChatStore.setState({ nodeExecutions: {} }));

  it("subscribes to the new chat id and does nothing without one", () => {
    const { result, rerender } = renderHook(({ id }) => useBuilderRun(id, [step("a")], []), {
      initialProps: { id: null as string | null },
      wrapper,
    });
    expect(subscribe).not.toHaveBeenCalled();
    expect(result.current).toBeNull();

    rerender({ id: "test-chat-1" });
    expect(subscribe).toHaveBeenCalledWith("test-chat-1");
  });

  it("hands the stream back to the chat that held it before the test run", () => {
    // The editor's chat panel (a normal ChatContainer) holds the single
    // chat-details slot. ChatContainer only re-asserts it when its chat id or
    // the connection changes, so if the test run doesn't hand the slot back,
    // the panel goes silent until a reload.
    useGlobalUpdatesStore.setState({ subscribedChatId: "panel-chat" });
    const { rerender } = renderHook(({ id }) => useBuilderRun(id, [step("a")], []), {
      initialProps: { id: "test-chat-1" as string | null },
      wrapper,
    });
    expect(subscribe).toHaveBeenLastCalledWith("test-chat-1");

    rerender({ id: null });
    expect(unsubscribe).toHaveBeenCalledWith("test-chat-1");
    expect(subscribe).toHaveBeenLastCalledWith("panel-chat");
  });

  it("restores nothing when no chat held the stream before the test run", () => {
    useGlobalUpdatesStore.setState({ subscribedChatId: null });
    const { rerender } = renderHook(({ id }) => useBuilderRun(id, [step("a")], []), {
      initialProps: { id: "test-chat-1" as string | null },
      wrapper,
    });
    rerender({ id: null });
    expect(subscribe).toHaveBeenCalledTimes(1);
  });

  it("reports the test chat's own steps by node id, before the execution tree arrives", () => {
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

    const { result } = renderHook(
      () => useBuilderRun("test-chat-1", [start, step("a"), step("b"), step("c")], [edge("workflow", "a"), edge("a", "b"), edge("a", "c")]),
      { wrapper },
    );
    const view = result.current!.view;
    expect(Object.fromEntries(Object.entries(view.nodes).map(([id, s]) => [id, s.status]))).toEqual({
      a: "completed",
      b: "running",
    });
    expect([...view.takenEdges].sort()).toEqual(["a->b", "workflow->a"]);
  });

  it("shows an approval that is waiting on you, a skipped step, and the failure's error", () => {
    chatQuery.data = {
      workflowState: WorkflowState.ACTIVE,
      workflowStopReason: WorkflowStopReason.UNSPECIFIED,
      activity: ChatActivity.AWAITING_INPUT,
    };
    useChatStore.setState({
      nodeExecutions: {
        "test-chat-1": [
          event("test-chat-1", "review", NodeExecutionEventType.COMPLETED, 2, { status: NodeExecutionStatus.SKIPPED }),
          event("test-chat-1", "post", NodeExecutionEventType.FAILED, 3, {
            status: NodeExecutionStatus.FAILED,
            error_message: "channel_not_found",
          }),
          event("test-chat-1", "approve", NodeExecutionEventType.STARTED, 4, { node_type: "ApprovalCreate" }),
          event("test-chat-1", "approve", NodeExecutionEventType.COMPLETED, 5, {
            node_type: "ApprovalCreate",
            status: NodeExecutionStatus.COMPLETED,
          }),
        ],
      },
    });

    const { result } = renderHook(() => useBuilderRun("test-chat-1", [step("review"), step("post"), step("approve")], []), { wrapper });
    const nodes = result.current!.view.nodes;
    expect(nodes.review?.status).toBe("skipped");
    expect(nodes.post).toEqual({ status: "failed", error: "channel_not_found" });
    expect(nodes.approve?.status).toBe("waiting");
    expect(result.current!.view.failed).toEqual(["post"]);
  });

  it("asks for the run's status again when its activity changes on the stream", () => {
    // The run's status is the chat's, and no stream event patches it; a run
    // that parked on a failed step said "Running" until a reload.
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    renderHook(() => useBuilderRun("test-chat-1", [step("a")], []), { wrapper });
    const chatDetail = () =>
      invalidate.mock.calls.filter(([filters]) => JSON.stringify(filters?.queryKey) === JSON.stringify(chatKeys.detail("test-chat-1"))).length;
    const before = chatDetail();

    act(() => useActivityStore.getState().applyStreamActivity("test-chat-1", ChatActivity.PAUSED, 50));

    expect(chatDetail()).toBe(before + 1);
  });
});
