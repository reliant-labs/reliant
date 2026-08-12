/**
 * Changing the tool-collapse preference must affect tool calls that are ALREADY
 * on screen, not just ones rendered afterwards.
 *
 * The components read the preference once in a useState initializer, so without
 * a broadcast from the settings page a change only appeared after a reload —
 * which reads to the user as "the setting does nothing".
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

describe("tool collapse preference changes apply live", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("expands an on-screen group when execution tools switch to expanded", () => {
    writePrefs({ execution: true });
    render(<ToolExecutionGroup executions={[mk("bash", "1"), mk("bash", "2")]} />);
    expect(screen.queryAllByTestId("tool-row")).toHaveLength(0);

    // User flips "Execution Tools" to expanded in Settings.
    writePrefs({ execution: false });
    act(() => {
      window.dispatchEvent(new CustomEvent(TOOL_COLLAPSE_SETTINGS_EVENT));
    });

    expect(screen.queryAllByTestId("tool-row")).toHaveLength(2);
  });

  it("collapses an on-screen group when execution tools switch to collapsed", () => {
    writePrefs({ execution: false });
    render(<ToolExecutionGroup executions={[mk("bash", "1"), mk("bash", "2")]} />);
    expect(screen.queryAllByTestId("tool-row")).toHaveLength(2);

    writePrefs({ execution: true });
    act(() => {
      window.dispatchEvent(new CustomEvent(TOOL_COLLAPSE_SETTINGS_EVENT));
    });

    expect(screen.queryAllByTestId("tool-row")).toHaveLength(0);
  });
});
