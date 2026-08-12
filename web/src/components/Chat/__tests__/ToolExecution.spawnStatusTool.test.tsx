import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ToolExecution } from "../ToolExecution";
import { ToolCallStatus, ChatWorkflowStatus } from "../../../gen/reliant/v1/chat_pb";

// Regression guard for the spawn_status / spawn_send substring bug: these are
// ordinary tools on an EXISTING spawn, not spawns themselves. Their status
// must come from their own tool call, not from a (nonexistent) child workflow.

const mockAllWorkflows: Array<{ id: string; status: ChatWorkflowStatus; children: unknown[] }> = [
  { id: "wf-child-1", status: ChatWorkflowStatus.RUNNING, children: [] },
];

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

describe("ToolExecution status for spawn_status / spawn_send", () => {
  it("shows completed for a completed spawn_status call, not a spawn-derived 'Executing...'", () => {
    render(
      <ToolExecution
        toolCall={{
          id: "toolu_spawn_status_1",
          name: "spawn_status",
          input: { agent_id: "wf-child-1" },
          finished: true,
          durableStatus: ToolCallStatus.COMPLETED,
        }}
        toolResult={{ name: "spawn_status", content: "status: running", is_error: false }}
        chatId="chat-1"
      />,
    );

    expect(screen.getByText("Completed")).toBeInTheDocument();
    expect(screen.queryByText("Executing...")).not.toBeInTheDocument();
  });

  it("shows executing for an in-flight spawn_status call, driven by its own tool status", () => {
    render(
      <ToolExecution
        toolCall={{
          id: "toolu_spawn_status_2",
          name: "spawn_status",
          input: { agent_id: "wf-child-1" },
          finished: false,
          durableStatus: ToolCallStatus.EXECUTING,
        }}
        chatId="chat-1"
      />,
    );

    expect(screen.getByText("Executing...")).toBeInTheDocument();
  });

  it("shows completed for a completed spawn_send call", () => {
    render(
      <ToolExecution
        toolCall={{
          id: "toolu_spawn_send_1",
          name: "spawn_send",
          input: { agent_id: "wf-child-1", message: "keep going" },
          finished: true,
          durableStatus: ToolCallStatus.COMPLETED,
        }}
        toolResult={{ name: "spawn_send", content: "queued", is_error: false }}
        chatId="chat-1"
      />,
    );

    expect(screen.getByText("Completed")).toBeInTheDocument();
    expect(screen.queryByText("Executing...")).not.toBeInTheDocument();
  });
});
