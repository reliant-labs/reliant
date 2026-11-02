/**
 * A tool GROUP's open state follows its work, not the per-kind collapse
 * preference: open while a call runs, closed once all finished. The
 * preference ("Execution tools start expanded") decides how each ROW inside
 * an open group starts (ToolExecution); a finished group stays closed when it
 * changes, and a running one stays open.
 */
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, act } from "@testing-library/react";

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
import {
  DEFAULT_TOOL_COLLAPSE_SETTINGS,
  TOOL_COLLAPSE_SETTINGS_EVENT,
  type ToolCollapseDefaults,
} from "../../Settings/ToolCallSettings";
import { SETTINGS_KEYS } from "../../../services/settingsSync";

const mk = (name: string, id: string) => ({
  call: { id, name, input: {} },
  result: { content: "ok", is_error: false } as never,
  status: "completed" as const,
});

function writePrefs(overrides: Partial<ToolCollapseDefaults>) {
  localStorage.setItem(
    SETTINGS_KEYS.TOOL_COLLAPSE_DEFAULTS,
    JSON.stringify({ ...DEFAULT_TOOL_COLLAPSE_SETTINGS, ...overrides })
  );
}

describe("tool groups follow their work, not the per-kind preference", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("keeps a finished group closed when execution tools switch to expanded", () => {
    writePrefs({ execution: true });
    render(<ToolExecutionGroup executions={[mk("shell", "1"), mk("shell", "2")]} />);
    expect(screen.queryAllByTestId("tool-row")).toHaveLength(0);

    writePrefs({ execution: false });
    act(() => {
      window.dispatchEvent(new CustomEvent(TOOL_COLLAPSE_SETTINGS_EVENT));
    });

    expect(screen.queryAllByTestId("tool-row")).toHaveLength(0);
  });

  it("keeps a running group open when execution tools switch to collapsed", () => {
    writePrefs({ execution: false });
    const live = (id: string) => ({ call: { id, name: "shell", input: {} }, status: "executing" as const });
    render(<ToolExecutionGroup executions={[live("1"), live("2")]} />);
    expect(screen.queryAllByTestId("tool-row")).toHaveLength(2);

    writePrefs({ execution: true });
    act(() => {
      window.dispatchEvent(new CustomEvent(TOOL_COLLAPSE_SETTINGS_EVENT));
    });

    expect(screen.queryAllByTestId("tool-row")).toHaveLength(2);
  });
});
