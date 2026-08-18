import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ToolExecution } from "../ToolExecution";
import { ToolCallStatus, WorkflowState, WorkflowStopReason } from "../../../gen/reliant/v1/chat_pb";

// Regression guard: "push to background" reaches a shell process running in
// the daemon via ConvertToBackground. A spawn is a Temporal child workflow,
// not a daemon-managed process, so the button does nothing there and is now
// also redundant — spawn is unconditionally non-blocking already. It must
// stay available for tools it actually works for, like an executing shell.

let mockAllWorkflows: Array<{ id: string; state: WorkflowState; stopReason: WorkflowStopReason; children: unknown[] }> = [];

vi.mock("../../../store/chatStoreHooks", () => ({
  useChat: () => undefined,
  useChatMessages: () => [],
  useStreamingMessages: () => [],
  useToolCallStates: () => new Map(),
}));
vi.mock("../../../hooks/approval-queries", () => ({
  useApproveToolRequest: () => ({ mutate: vi.fn() }),
  useDenyToolRequest: () => ({ mutate: vi.fn() }),
}));
vi.mock("../../../hooks/task-queries", () => ({
  useTasksForChat: () => ({ data: undefined }),
}));
vi.mock("../../../store/threadActivityStore", () => ({
  useActiveThreads: () => [],
}));
vi.mock("../../../hooks/useWorkflowExecutions", () => ({
  useWorkflowExecutions: () => ({ allWorkflows: mockAllWorkflows }),
}));

describe("ToolExecution push-to-background button", () => {
  it("does not render for an executing spawn", () => {
    mockAllWorkflows = [{ id: "wf-child-1", state: WorkflowState.ACTIVE, stopReason: WorkflowStopReason.UNSPECIFIED, children: [] }];

    render(
      <ToolExecution
        toolCall={{
          id: "toolu_spawn_1",
          name: "spawn",
          input: { title: "reviewer" },
          finished: true,
          durableStatus: ToolCallStatus.COMPLETED,
          childWorkflowId: "wf-child-1",
        }}
        toolResult={{ name: "spawn", content: "Spawned agent_id: wf-child-1", is_error: false }}
        chatId="chat-1"
        onConvertToBackground={vi.fn()}
        onCancel={vi.fn()}
      />,
    );

    expect(screen.getByText("Executing...")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Push tool execution to background" }),
    ).not.toBeInTheDocument();
  });

  it("still renders for an executing backgroundable tool", () => {
    mockAllWorkflows = [];

    render(
      <ToolExecution
        toolCall={{
          id: "toolu_bash_1",
          name: "bash",
          input: { command: "sleep 100" },
          finished: false,
        }}
        status="executing"
        chatId="chat-1"
        onConvertToBackground={vi.fn()}
        onCancel={vi.fn()}
      />,
    );

    expect(
      screen.getByRole("button", { name: "Push tool execution to background" }),
    ).toBeInTheDocument();
  });
});
