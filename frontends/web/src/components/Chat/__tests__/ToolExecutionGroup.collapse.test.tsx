import { describe, it, expect, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

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
  workflowLifecycleToDisplayStatus: () => undefined,
}));

import { ToolExecutionGroup } from "../ToolExecutionGroup";

const done = (name: string, id: string, is_error = false) => ({
  call: { id, name, input: {} },
  result: { content: "ok", is_error } as never,
  status: is_error ? ("failed" as const) : ("completed" as const),
});
const running = (name: string, id: string) => ({
  call: { id, name, input: {} },
  status: "executing" as const,
});

/**
 * Live work is open and finished work is closed — the reverse of the old
 * default, which hid two running tools behind one row while finished edits
 * stayed expanded.
 */
describe("ToolExecutionGroup expansion", () => {
  it("is open while a call in it is running", () => {
    render(<ToolExecutionGroup executions={[running("spawn", "1"), running("shell", "2")]} />);
    expect(screen.queryAllByTestId("tool-row")).toHaveLength(2);
  });

  it("is closed once every call finished, even a file edit", () => {
    render(<ToolExecutionGroup executions={[done("shell", "1"), done("edit", "2")]} />);
    expect(screen.queryAllByTestId("tool-row")).toHaveLength(0);
  });

  it("closes by itself when its last call finishes", () => {
    const { rerender } = render(<ToolExecutionGroup executions={[done("shell", "1"), running("shell", "2")]} />);
    expect(screen.queryAllByTestId("tool-row")).toHaveLength(2);
    rerender(<ToolExecutionGroup executions={[done("shell", "1"), done("shell", "2")]} />);
    expect(screen.queryAllByTestId("tool-row")).toHaveLength(0);
  });

  it("stays open when a call failed, so the failure is in view", () => {
    render(<ToolExecutionGroup executions={[done("shell", "1"), done("shell", "2", true)]} />);
    expect(screen.queryAllByTestId("tool-row")).toHaveLength(2);
  });

  it("has a header that is a real button with aria-expanded", () => {
    render(<ToolExecutionGroup executions={[done("shell", "1"), done("shell", "2")]} />);
    const header = screen.getByRole("button", { expanded: false });
    expect(header.tagName).toBe("BUTTON");
    fireEvent.click(header);
    expect(header).toHaveAttribute("aria-expanded", "true");
    expect(screen.queryAllByTestId("tool-row")).toHaveLength(2);
    expect(document.getElementById(header.getAttribute("aria-controls")!)).not.toBeNull();
  });
});
