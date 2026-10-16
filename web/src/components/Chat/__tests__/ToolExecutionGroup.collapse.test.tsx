import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";

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

import { SurfaceProvider } from "../../../lib/surfaceContext";
import { ToolExecutionGroup } from "../ToolExecutionGroup";

const mk = (name: string, id: string) => ({
  call: { id, name, input: {} },
  result: { content: "ok", is_error: false } as never,
  status: "completed" as const,
});

describe("ToolExecutionGroup default expansion", () => {
  it("starts collapsed when every tool collapses by default", () => {
    render(<ToolExecutionGroup executions={[mk("shell", "1"), mk("shell", "2")]} />);
    expect(screen.queryAllByTestId("tool-row")).toHaveLength(0);
  });

  it("starts expanded when a tool wants to be open (file edit)", () => {
    render(<ToolExecutionGroup executions={[mk("shell", "1"), mk("edit", "2")]} />);
    expect(screen.queryAllByTestId("tool-row")).toHaveLength(2);
  });
});

 it("keeps mobile runs collapsed by default and preserves expansion as tools arrive", () => {
   const wrap = (count: number) => <SurfaceProvider surface="mobile"><ToolExecutionGroup executions={Array.from({length:count}, (_, i) => mk("edit", String(i)))} /></SurfaceProvider>;
   const {rerender} = render(wrap(2));
   expect(screen.queryAllByTestId("tool-row")).toHaveLength(0);
   fireEvent.click(screen.getByRole("button", {name:/Edited 2 files/}));
   rerender(wrap(3));
   expect(screen.getAllByTestId("tool-row")).toHaveLength(3);
   expect(screen.getByRole("button", {name:/Edited 3 files/})).toHaveAttribute("aria-expanded", "true");
 });
