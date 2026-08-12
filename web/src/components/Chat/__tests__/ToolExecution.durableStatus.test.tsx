import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ToolExecution } from "../ToolExecution";
import { ToolCallStatus } from "../../../gen/reliant/v1/chat_pb";

// Regression guard for the bug this slice fixes: a paused workflow used to
// report chat activity IDLE, and ToolExecution inferred "completed" from
// that -- even for a tool that was still executing. Status is now durable
// (persisted in tool_calls and projected onto the block), so a durable
// EXECUTING status must win over any inference from workflow/chat activity.

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
  useWorkflowExecutions: () => ({ allWorkflows: [] }),
}));

describe("ToolExecution durable status", () => {
  it("renders the durable EXECUTING status, not completed, when there is no live status", () => {
    render(
      <ToolExecution
        toolCall={{
          id: "toolu_1",
          name: "bash",
          input: { command: "sleep 600" },
          finished: true,
          durableStatus: ToolCallStatus.EXECUTING,
        }}
        chatId="chat-1"
      />,
    );

    expect(screen.getByText("Executing...")).toBeInTheDocument();
    expect(screen.queryByText("Completed")).not.toBeInTheDocument();
  });

  it("still shows completed when the durable status says so", () => {
    render(
      <ToolExecution
        toolCall={{
          id: "toolu_2",
          name: "bash",
          input: { command: "echo hi" },
          finished: true,
          durableStatus: ToolCallStatus.COMPLETED,
        }}
        chatId="chat-1"
      />,
    );

    expect(screen.getByText("Completed")).toBeInTheDocument();
  });
});
