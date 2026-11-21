/**
 * A run's read-only calls as one quiet "Explored …" row: what it says, when
 * it opens by itself, and the rows it opens onto.
 */
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { SurfaceProvider } from "../../../lib/surfaceContext";

vi.mock("../ToolExecution", () => ({
  ToolExecution: ({ toolCall }: { toolCall: { name: string } }) => <div data-testid="tool-row">{toolCall.name}</div>,
}));

import { ToolExecutionCollapsibleGroup } from "../ToolExecutionCollapsibleGroup";

const done = (id: string, name: string, input: Record<string, unknown> = {}, isError = false) => ({
  call: { id, name, input },
  result: { content: "ok", is_error: isError } as never,
  status: (isError ? "failed" : "completed") as "failed" | "completed",
});

const reads = () => [
  done("1", "view", { path: "a.ts" }),
  done("2", "view", { path: "b.ts" }),
  done("3", "grep", { pattern: "x" }),
  done("4", "glob", { pattern: "*.ts" }),
];

const header = () => screen.getByRole("button");

describe("ToolExecutionCollapsibleGroup", () => {
  it("says what the reads explored, closed once they are done", () => {
    render(<ToolExecutionCollapsibleGroup executions={reads()} />);
    expect(header().textContent).toContain("Explored 2 files 1 search 1 listing");
    expect(header().getAttribute("aria-label")).toBe("Explored 2 files 1 search 1 listing");
    expect(header().getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryAllByTestId("tool-row")).toHaveLength(0);
  });

  it("opens onto one row per call, in order", () => {
    render(<ToolExecutionCollapsibleGroup executions={reads()} />);
    fireEvent.click(header());
    expect(header().getAttribute("aria-expanded")).toBe("true");
    expect(screen.getAllByTestId("tool-row").map((row) => row.textContent)).toEqual(["view", "view", "grep", "glob"]);
    fireEvent.click(header());
    expect(screen.queryAllByTestId("tool-row")).toHaveLength(0);
  });

  it("stays open while a read is still running", () => {
    const executions = [...reads().slice(0, 2), { call: { id: "5", name: "view", input: { path: "c.ts" } }, status: "executing" as const }];
    render(<ToolExecutionCollapsibleGroup executions={executions} />);
    expect(header().getAttribute("aria-expanded")).toBe("true");
    expect(screen.getAllByTestId("tool-row")).toHaveLength(3);
  });

  it("names a failed read in its row and its accessible name, and opens on it", () => {
    render(<ToolExecutionCollapsibleGroup executions={[done("1", "view"), done("2", "grep", {}, true)]} />);
    expect(header().textContent).toContain("1 failed");
    expect(header().getAttribute("aria-label")).toBe("Explored 1 file 1 search, 1 failed");
    expect(header().getAttribute("aria-expanded")).toBe("true");
  });

  it("stays closed on the phone, even while a read runs", () => {
    const executions = [done("1", "view"), { call: { id: "2", name: "view", input: {} }, status: "executing" as const }];
    render(
      <SurfaceProvider surface="mobile">
        <ToolExecutionCollapsibleGroup executions={executions} />
      </SurfaceProvider>,
    );
    expect(header().getAttribute("aria-expanded")).toBe("false");
  });
});
