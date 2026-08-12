import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ToolExecution } from "../ToolExecution";
import { ToolCallStatus, ChatWorkflowStatus } from "../../../gen/reliant/v1/chat_pb";

// Regression guard for async spawn: the spawn tool call receives its result
// (the dispatch handle) in milliseconds, while the agent it started keeps
// running for minutes. The child workflow, not the tool call, must be the
// authority on the displayed status.

let mockAllWorkflows: Array<{ id: string; status: ChatWorkflowStatus; children: unknown[] }> = [];

vi.mock("../../../store/chatStoreHooks", () => ({
  useChat: () => undefined,
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

describe("ToolExecution spawn status under async spawn", () => {
  it("does not render completed for a spawn whose child workflow is still running, even though the tool call already has a result", () => {
    mockAllWorkflows = [{ id: "wf-child-1", status: ChatWorkflowStatus.RUNNING, children: [] }];

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
      />,
    );

    expect(screen.getByText("Executing...")).toBeInTheDocument();
    expect(screen.queryByText("Completed")).not.toBeInTheDocument();
  });

  it("renders completed for a spawn whose child workflow has completed", () => {
    mockAllWorkflows = [{ id: "wf-child-2", status: ChatWorkflowStatus.COMPLETED, children: [] }];

    render(
      <ToolExecution
        toolCall={{
          id: "toolu_spawn_2",
          name: "spawn",
          input: { title: "reviewer" },
          finished: true,
          durableStatus: ToolCallStatus.COMPLETED,
          childWorkflowId: "wf-child-2",
        }}
        toolResult={{ name: "spawn", content: "Spawned agent_id: wf-child-2", is_error: false }}
        chatId="chat-1"
      />,
    );

    expect(screen.getByText("Completed")).toBeInTheDocument();
  });

  it("treats a spawn with no matching child workflow row yet (just dispatched) as executing", () => {
    mockAllWorkflows = [];

    render(
      <ToolExecution
        toolCall={{
          id: "toolu_spawn_3",
          name: "spawn",
          input: { title: "reviewer" },
          finished: true,
          durableStatus: ToolCallStatus.COMPLETED,
          childWorkflowId: "wf-child-not-yet-created",
        }}
        toolResult={{ name: "spawn", content: "Spawned agent_id: wf-child-not-yet-created", is_error: false }}
        chatId="chat-1"
      />,
    );

    expect(screen.getByText("Executing...")).toBeInTheDocument();
    expect(screen.queryByText("Completed")).not.toBeInTheDocument();
  });
});
