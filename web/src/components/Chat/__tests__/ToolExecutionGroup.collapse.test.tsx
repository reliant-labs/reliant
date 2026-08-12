import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";

vi.mock("../../../store/activityStore", () => ({
  useActivityStore: () => undefined,
  ChatActivity: { IDLE: "idle" },
}));
vi.mock("../../../hooks/useWorkflowExecutions", () => ({
  useWorkflowExecutions: () => ({ allWorkflows: [] }),
}));
vi.mock("../ToolExecution", () => ({
  ToolExecution: ({ toolCall }: { toolCall: { name: string } }) => (
    <div data-testid="tool-row">{toolCall.name}</div>
  ),
  durableStatusToDisplayStatus: () => undefined,
  workflowStatusToDisplayStatus: () => undefined,
}));

import { ToolExecutionGroup } from "../ToolExecutionGroup";

const mk = (name: string, id: string) => ({
  call: { id, name, input: {} },
  result: { content: "ok", is_error: false } as never,
  status: "completed" as const,
});

describe("ToolExecutionGroup default expansion", () => {
  it("starts collapsed when every tool collapses by default", () => {
    render(<ToolExecutionGroup executions={[mk("bash", "1"), mk("bash", "2")]} />);
    expect(screen.queryAllByTestId("tool-row")).toHaveLength(0);
  });

  it("starts expanded when a tool wants to be open (file edit)", () => {
    render(<ToolExecutionGroup executions={[mk("bash", "1"), mk("edit", "2")]} />);
    expect(screen.queryAllByTestId("tool-row")).toHaveLength(2);
  });
});
