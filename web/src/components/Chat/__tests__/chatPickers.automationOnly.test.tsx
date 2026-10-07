/**
 * A workflow whose Chat trigger is off (`automation_only` in its YAML) cannot
 * be started from a chat, so no chat picker offers it. Both composer pickers
 * read the same fact from the server's list item (isChatLaunchable), so they
 * cannot disagree about which workflows a chat can start.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("../../../store/globalDataStore", () => ({
  useWorkflows: () => ({
    workflows: [
      { name: "builtin://agent", filename: "agent", description: "Basic agentic chat", step_count: 2, source: "builtin", status: "complete" },
      { name: "chatty-helper", filename: "chatty-helper", description: "Answers questions", step_count: 1, source: "user", status: "complete" },
      {
        name: "nightly-digest",
        filename: "nightly-digest",
        description: "Runs on a schedule only",
        step_count: 1,
        source: "user",
        status: "complete",
        automation_only: true,
      },
    ],
    loading: false,
  }),
}));

vi.mock("../../../store/preferencesStore", () => {
  const state = {
    preferences: { defaultWorkflow: "builtin://agent" },
    isLoading: false,
    loadPreferences: vi.fn(),
    isWorkflowHidden: () => false,
    updatePreferences: vi.fn(),
  };
  const usePreferencesStore = (selector?: (s: typeof state) => unknown) => (selector ? selector(state) : state);
  return { DEFAULT_WORKFLOW: "builtin://agent", usePreferencesStore };
});

const { WorkflowSelector } = await import("../WorkflowSelector");
const { AgentSelector } = await import("../AgentSelector");

describe("chat pickers leave out workflows whose Chat trigger is off", () => {
  it("WorkflowSelector (composer dropdown and the Change Workflow slash command)", async () => {
    render(<WorkflowSelector />);
    await userEvent.click(screen.getByRole("button", { name: /agent/i }));
    expect(await screen.findByText("Chatty Helper")).toBeInTheDocument();
    expect(screen.queryByText("Nightly Digest")).not.toBeInTheDocument();
  });

  it("AgentSelector", async () => {
    render(<AgentSelector />);
    await userEvent.click(screen.getByRole("button", { name: /general/i }));
    expect(await screen.findByText("chatty-helper")).toBeInTheDocument();
    expect(screen.queryByText("nightly-digest")).not.toBeInTheDocument();
  });
});
