/**
 * A workflow whose Chat trigger is off (`automation_only` in its YAML) cannot
 * be started from a chat, so no chat picker offers it. Both composer pickers
 * read the same fact from the server's list item (isChatLaunchable), so they
 * cannot disagree about which workflows a chat can start.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("../../../hooks/workflow-queries", async (importActual) => ({
  ...(await importActual<typeof import("../../../hooks/workflow-queries")>()),
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
      // The Autopilot pack: its workflows only start from their triggers, and
      // its helpers are building blocks (hidden) as well.
      {
        name: "builtin://autopilot-pr-review",
        filename: "autopilot-pr-review",
        title: "Autopilot · PR review",
        description: "Reviews a pull request",
        step_count: 4,
        source: "builtin",
        status: "complete",
        automation_only: true,
      },
      {
        name: "builtin://autopilot-lease-acquire",
        filename: "autopilot-lease-acquire",
        title: "Autopilot · take a work-item lease",
        description: "Takes a lease",
        step_count: 1,
        source: "builtin",
        status: "complete",
        is_hidden: true,
        automation_only: true,
      },
      // A user's saved building block: `hidden: true` in its YAML.
      {
        name: "shared-step",
        filename: "shared-step",
        description: "Ref'd by other workflows",
        step_count: 1,
        source: "user",
        status: "complete",
        is_hidden: true,
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

describe("chat pickers leave out workflows whose Chat trigger is off, and building blocks", () => {
  it("WorkflowSelector (composer dropdown and the Change Workflow slash command)", async () => {
    render(<WorkflowSelector />);
    await userEvent.click(screen.getByRole("button", { name: /agent/i }));
    expect(await screen.findByText("Chatty Helper")).toBeInTheDocument();
    expect(screen.queryByText("Nightly Digest")).not.toBeInTheDocument();
    expect(screen.queryByText(/Autopilot/)).not.toBeInTheDocument();
    expect(screen.queryByText("Shared Step")).not.toBeInTheDocument();
  });

  it("AgentSelector", async () => {
    render(<AgentSelector />);
    await userEvent.click(screen.getByRole("button", { name: /general/i }));
    expect(await screen.findByText("chatty-helper")).toBeInTheDocument();
    expect(screen.queryByText("nightly-digest")).not.toBeInTheDocument();
    expect(screen.queryByText(/autopilot/i)).not.toBeInTheDocument();
    expect(screen.queryByText("shared-step")).not.toBeInTheDocument();
  });
});
