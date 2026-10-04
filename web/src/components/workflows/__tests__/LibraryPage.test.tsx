// Copyright (c) 2025 Reliant Labs

/**
 * The Library (WORKFLOW_UI.md §2.2), against the real data layer with only the
 * RPC client mocked: sections kept from the hub (Your workflows, Built-in,
 * Failed to load), each row's "last run" from LastRunPerWorkflow and its
 * automations count from ListTriggers, the name linking to DETAIL (not the
 * builder), and Run… opening RunWorkflowDialog.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { RunDisplayState } from "@/gen/reliant/v1/run_pb";
import { libraryResponse, protoRun, protoTrigger, renderWorkflowsPage } from "./workflowsTestUtils";

const mocks = vi.hoisted(() => ({
  listWorkflows: vi.fn(),
  getWorkflow: vi.fn(),
  lastRunPerWorkflow: vi.fn(),
  listTriggers: vi.fn(),
  listPresetsForWorkflow: vi.fn(),
  getDefaultPresetsBatch: vi.fn(),
  listWorktrees: vi.fn(),
  listDaemons: vi.fn(),
  listProjectDaemons: vi.fn(),
}));

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    workflow: () => ({ listWorkflows: mocks.listWorkflows, getWorkflow: mocks.getWorkflow }),
    run: () => ({ lastRunPerWorkflow: mocks.lastRunPerWorkflow }),
    trigger: () => ({ listTriggers: mocks.listTriggers }),
    preset: () => ({
      listPresetsForWorkflow: mocks.listPresetsForWorkflow,
      getDefaultPresetsBatch: mocks.getDefaultPresetsBatch,
    }),
    worktree: () => ({ listWorktrees: mocks.listWorktrees }),
    daemonRegistry: () => ({ listDaemons: mocks.listDaemons }),
    project: () => ({ listProjectDaemons: mocks.listProjectDaemons }),
  },
}));

vi.mock("@/store/projectStore", () => {
  const state = { currentProject: { id: "proj-1", name: "Reliant" }, projects: [], isLoading: false };
  const useProjectStore = Object.assign(
    (selector?: (s: typeof state) => unknown) => (selector ? selector(state) : state),
    { getState: () => state },
  );
  return { useProjectStore };
});

import { LibraryPage } from "../library/LibraryPage";

beforeEach(() => {
  for (const mock of Object.values(mocks)) mock.mockReset();
  mocks.listWorkflows.mockResolvedValue(libraryResponse());
  mocks.lastRunPerWorkflow.mockResolvedValue({
    runs: [protoRun("triage", "chat-tri", RunDisplayState.FAILED)],
  });
  mocks.listTriggers.mockResolvedValue({
    triggers: [protoTrigger("t1", "triage"), protoTrigger("t2", "triage"), protoTrigger("t3", "builtin://agent")],
  });
  mocks.getWorkflow.mockResolvedValue({ source: "project", workflow: { name: "triage", inputs: {} } });
  mocks.listPresetsForWorkflow.mockResolvedValue({ presets: [], invalidPresets: [] });
  mocks.getDefaultPresetsBatch.mockResolvedValue({ defaults: {} });
  mocks.listWorktrees.mockResolvedValue({ worktrees: [], total: 0 });
  mocks.listDaemons.mockResolvedValue({ daemons: [] });
  mocks.listProjectDaemons.mockResolvedValue({ projectDaemons: [] });
});

function renderLibrary() {
  return renderWorkflowsPage(<LibraryPage />, "/workflows/library", "/workflows/library");
}

describe("LibraryPage", () => {
  it("keeps the hub's sections: your workflows, built-in, and failed to load", async () => {
    renderLibrary();
    const mine = await screen.findByRole("list", { name: "Your workflows" });
    expect(within(mine).getByTestId("workflow-row-triage")).toBeInTheDocument();
    expect(within(mine).getByTestId("workflow-row-my-draft")).toBeInTheDocument();
    const builtin = screen.getByRole("list", { name: "Built-in" });
    expect(within(builtin).getByTestId("workflow-row-builtin://agent")).toBeInTheDocument();
    const broken = screen.getByRole("list", { name: "Failed to load" });
    expect(within(broken).getByText("yaml: line 3: bad indent")).toBeInTheDocument();
    // It asked for hidden workflows too: this is the management view.
    expect(mocks.listWorkflows).toHaveBeenCalledWith(expect.objectContaining({ projectId: "proj-1", includeHidden: true }));
  });

  it("shows each row's last run and automations count", async () => {
    renderLibrary();
    const triage = await screen.findByTestId("workflow-row-triage");
    await waitFor(() =>
      expect(within(triage).getByTestId("workflow-row-last-run")).toHaveTextContent(/Failed · 2 hours ago/),
    );
    expect(within(triage).getByRole("link", { name: /Failed · 2 hours ago/ })).toHaveAttribute(
      "href",
      "/workflows/runs/chat-tri",
    );
    expect(within(triage).getByTestId("workflow-row-automations")).toHaveTextContent("2 automations");

    // A builtin's automations match on its stored ref.
    const agent = screen.getByTestId("workflow-row-builtin://agent");
    await waitFor(() => expect(within(agent).getByTestId("workflow-row-automations")).toHaveTextContent("1 automation"));
    expect(within(agent).getByTestId("workflow-row-last-run")).toHaveTextContent("Not run yet");
    // One request for every row, never one per workflow.
    expect(mocks.lastRunPerWorkflow).toHaveBeenCalledTimes(1);
  });

  it("clicking a workflow goes to its detail page, not the builder", async () => {
    const { router } = renderLibrary();
    const triage = await screen.findByTestId("workflow-row-triage");
    const link = within(triage).getByRole("link", { name: "Triage" });
    expect(link).toHaveAttribute("href", "/workflows/library/triage");
    await userEvent.click(link);
    await waitFor(() => expect(router.state.location.pathname).toBe("/workflows/library/triage"));
  });

  it("Run… opens the run dialog for that workflow; a draft offers no Run…", async () => {
    renderLibrary();
    const triage = await screen.findByTestId("workflow-row-triage");
    await userEvent.click(within(triage).getByRole("button", { name: "Run Triage" }));
    expect(await screen.findByRole("dialog", { name: "Run Triage" })).toBeInTheDocument();

    const draft = screen.getByTestId("workflow-row-my-draft");
    expect(within(draft).queryByRole("button", { name: /^Run / })).toBeNull();
    expect(within(draft).getByTestId("workflow-draft-badge")).toBeInTheDocument();
  });

  it("the row menu's Edit opens the builder", async () => {
    const { router } = renderLibrary();
    const triage = await screen.findByTestId("workflow-row-triage");
    await userEvent.click(within(triage).getByRole("button", { name: "More actions for Triage" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Edit" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/workflow/triage"));
  });

  it("a user workflow's menu offers Export and Delete; a built-in's does not", async () => {
    renderLibrary();
    const draft = await screen.findByTestId("workflow-row-my-draft");
    await userEvent.click(within(draft).getByRole("button", { name: "More actions for My Draft" }));
    const menu = await screen.findByRole("menu");
    expect(within(menu).getByRole("menuitem", { name: "Delete" })).toBeInTheDocument();
    expect(within(menu).getByRole("menuitem", { name: "Export" })).toBeInTheDocument();
    // A draft cannot be the default.
    expect(within(menu).queryByRole("menuitem", { name: "Set as default" })).toBeNull();
    await userEvent.keyboard("{Escape}");

    const agent = screen.getByTestId("workflow-row-builtin://agent");
    await userEvent.click(within(agent).getByRole("button", { name: "More actions for Agent" }));
    const builtinMenu = await screen.findByRole("menu");
    expect(within(builtinMenu).queryByRole("menuitem", { name: "Delete" })).toBeNull();
    expect(within(builtinMenu).getByRole("menuitem", { name: "Duplicate" })).toBeInTheDocument();
  });
});
