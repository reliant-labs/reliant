import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SubAgentGroup } from "../SubAgentGroup";
import { WorkflowState, WorkflowStopReason } from "../../../gen/reliant/v1/chat_pb";

// Sub-agents a turn started together are one group: a header counting them by
// state, open while any runs, closed once all finished — and a failed one
// stays on screen even when the group is closed.

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

afterEach(() => {
  cleanup();
  mockAllWorkflows = [];
});

const DONE = { state: WorkflowState.STOPPED, stopReason: WorkflowStopReason.COMPLETED };
const FAILED = { state: WorkflowState.STOPPED, stopReason: WorkflowStopReason.FAILED };
const RUNNING = { state: WorkflowState.ACTIVE, stopReason: WorkflowStopReason.UNSPECIFIED };

function agents(states: Array<typeof DONE>) {
  mockAllWorkflows = states.map((s, i) => ({ id: `wf-${i}`, ...s, children: [] }));
  return states.map((_, i) => ({
    call: { id: `call-${i}`, name: "spawn", input: { title: `Agent ${i + 1}` }, finished: true, childWorkflowId: `wf-${i}` },
    result: { id: `call-${i}`, name: "spawn", content: "started", is_error: false },
    onCancel: vi.fn(),
  }));
}

describe("SubAgentGroup", () => {
  it("counts twelve agents by state in one header, open while any runs", () => {
    const executions = agents([...Array(8).fill(DONE), FAILED, RUNNING, RUNNING, RUNNING]);
    render(<SubAgentGroup executions={executions} chatId="chat-1" />);

    const group = screen.getByTestId("sub-agent-group");
    expect(within(group).getByText("12 agents · 8 done · 3 running · 1 failed")).toBeInTheDocument();
    expect(group).toHaveAttribute("data-expanded", "true");
    expect(within(group).getAllByTestId("sub-agent-row")).toHaveLength(12);
    // Each running agent keeps a keyboard-reachable Cancel.
    const cancels = within(group).getAllByRole("button", { name: "Cancel tool execution" });
    expect(cancels).toHaveLength(3);
    cancels[0].focus();
    expect(cancels[0]).toHaveFocus();
  });

  it("is closed once every agent finished, but keeps a failed one on screen", () => {
    const executions = agents([DONE, DONE, FAILED, DONE]);
    render(<SubAgentGroup executions={executions} chatId="chat-1" />);

    const group = screen.getByTestId("sub-agent-group");
    expect(group).toHaveAttribute("data-expanded", "false");
    const rows = within(group).getAllByTestId("sub-agent-row");
    expect(rows).toHaveLength(1);
    expect(rows[0]).toHaveAttribute("data-state", "failed");
    expect(within(rows[0]).getByText("Agent 3")).toBeInTheDocument();
  });

  it("opens on the user's toggle", () => {
    const executions = agents([DONE, DONE, DONE]);
    render(<SubAgentGroup executions={executions} chatId="chat-1" />);

    const toggle = screen.getByRole("button", { name: "3 agents · 3 done" });
    expect(screen.queryAllByTestId("sub-agent-row")).toHaveLength(0);
    fireEvent.click(toggle);
    expect(screen.getAllByTestId("sub-agent-row")).toHaveLength(3);
  });
});
