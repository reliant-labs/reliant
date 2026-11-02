/**
 * The collapsed group header says what the lead call did rather than counting
 * tool calls: "Edited totals.ts", a shell call's own description ("Run the
 * tests"), "Spawned researcher" — and name(preview) for a tool with no
 * plain-words verb.
 */
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";

vi.mock("../../../store/activityStore", () => ({
  useActivityStore: () => undefined,
  ChatActivity: { IDLE: "idle" },
}));
vi.mock("../../../hooks/useWorkflowExecutions", () => ({
  useWorkflowExecutions: () => ({ allWorkflows: [] }),
}));
vi.mock("../ToolExecution", () => ({
  ToolExecution: () => <div data-testid="tool-row" />,
  durableStatusToDisplayStatus: () => undefined,
  workflowStatusToDisplayStatus: () => undefined,
}));

import { ToolExecutionGroup } from "../ToolExecutionGroup";
import {
  DEFAULT_TOOL_COLLAPSE_SETTINGS,
  type ToolCollapseDefaults,
} from "../../Settings/ToolCallSettings";
import { SETTINGS_KEYS } from "../../../services/settingsSync";

const mk = (
  name: string,
  id: string,
  input: Record<string, unknown> = {}
) => ({
  call: { id, name, input },
  result: { content: "ok", is_error: false } as never,
  status: "completed" as const,
});

function writePrefs(overrides: Partial<ToolCollapseDefaults>) {
  localStorage.setItem(
    SETTINGS_KEYS.TOOL_COLLAPSE_DEFAULTS,
    JSON.stringify({ ...DEFAULT_TOOL_COLLAPSE_SETTINGS, ...overrides })
  );
}

describe("ToolExecutionGroup header label", () => {
  beforeEach(() => {
    localStorage.clear();
    writePrefs({ execution: true, agent: true });
  });

  const shell = (id: string, description: string) =>
    mk("shell", id, { command: `rg -n thing-${id} .`, description });

  it("previews the lead call's argument and counts the rest", () => {
    render(
      <ToolExecutionGroup
        executions={[shell("1", "Run the tests"), shell("2", "Lint"), shell("3", "Build")]}
      />
    );
    expect(screen.getByText("Run the tests and 2 other tools")).toBeInTheDocument();
  });

  it("uses the singular for exactly one other call", () => {
    render(<ToolExecutionGroup executions={[shell("1", "Run the tests"), mk("grep", "2")]} />);
    expect(screen.getByText("Run the tests and 1 other tool")).toBeInTheDocument();
  });

  it("leads with spawn wherever it appears in the group", () => {
    render(
      <ToolExecutionGroup
        executions={[
          shell("1", "Run the tests"),
          mk("spawn", "2", { preset: "researcher" }),
          mk("grep", "3"),
        ]}
      />
    );
    expect(screen.getByText("Spawned researcher and 2 other tools")).toBeInTheDocument();
  });

  it("strips the mcp__ prefix from the lead name", () => {
    render(
      <ToolExecutionGroup
        executions={[shell("1", "Run the tests"), mk("mcp__reliant__spawn", "2", {})]}
      />
    );
    expect(screen.getByText("Spawned and 1 other tool")).toBeInTheDocument();
  });

  it("shows just the lead preview when the group holds a single call", () => {
    render(<ToolExecutionGroup executions={[shell("1", "Run the tests")]} />);
    expect(screen.getByText("Run the tests")).toBeInTheDocument();
  });

  it("names an edit by its file, as the action", () => {
    render(<ToolExecutionGroup executions={[mk("edit", "1", { file_path: "src/cart/totals.ts" }), mk("grep", "2")]} />);
    expect(screen.getByText(/^Edited .*totals\.ts and 1 other tool$/)).toBeInTheDocument();
  });

  it("keeps name(preview) for a tool with no plain-words verb", () => {
    render(<ToolExecutionGroup executions={[mk("custom_thing", "1", { query: "x" }), mk("grep", "2")]} />);
    expect(screen.getByText(/^custom_thing.* and 1 other tool$/)).toBeInTheDocument();
  });

  it("truncates a long preview so the trailing count stays visible", () => {
    render(
      <ToolExecutionGroup
        executions={[shell("1", "A".repeat(200)), shell("2", "Lint")]}
      />
    );
    const label = screen.getByText(/and 1 other tool$/);
    expect(label.textContent!.length).toBeLessThan(80);
    expect(label.textContent).toContain("…");
  });
});
