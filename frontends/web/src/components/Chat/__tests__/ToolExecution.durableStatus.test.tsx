import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
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

  it("says what happened and how long it took, with no Completed pill", () => {
    render(
      // The edit's file is a FileLink, which reads the workspace cache.
      <QueryClientProvider client={new QueryClient()}>
      <ToolExecution
        toolCall={{
          id: "toolu_3",
          name: "edit",
          input: { file_path: "src/cart/totals.ts", old_string: "a", new_string: "b" },
          finished: true,
          durableStatus: ToolCallStatus.COMPLETED,
          durableStartedAt: "2026-11-01T10:00:00.000Z",
          durableCompletedAt: "2026-11-01T10:00:04.100Z",
        }}
        chatId="chat-1"
      />
      </QueryClientProvider>,
    );

    expect(screen.getByText("Edited")).toBeInTheDocument();
    expect(screen.getByText("4.1s")).toBeInTheDocument();
    // Success is announced, not drawn as a pill.
    expect(screen.getByText("Completed")).toHaveClass("sr-only");
  });

  it("calls a failed call Failed, not Warning", () => {
    render(
      <ToolExecution
        toolCall={{ id: "toolu_4", name: "shell", input: { command: "npm test" }, finished: true }}
        toolResult={{ tool_call_id: "toolu_4", name: "shell", content: "1 failing", is_error: true }}
        chatId="chat-1"
      />,
    );

    expect(screen.getByText("Failed")).toBeInTheDocument();
    expect(screen.queryByText("Warning")).not.toBeInTheDocument();
    expect(screen.getByText("Ran")).toBeInTheDocument();
  });
});
