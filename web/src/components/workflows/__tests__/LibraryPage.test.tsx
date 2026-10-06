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

import { create } from "@bufbuild/protobuf";

import { RunDisplayState } from "@/gen/reliant/v1/run_pb";
import { TriggerHealthSchema, TriggerHealthStatus, TriggerSchema } from "@/gen/reliant/v1/trigger_pb";
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
  deleteWorkflow: vi.fn(),
}));

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    workflow: () => ({ listWorkflows: mocks.listWorkflows, getWorkflow: mocks.getWorkflow, deleteWorkflow: mocks.deleteWorkflow }),
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
  it("is one table with labelled columns: yours first, then built-in; broken files below", async () => {
    renderLibrary();
    const table = await screen.findByRole("table", { name: "Workflows" });
    expect(within(table).getAllByRole("columnheader").map((th) => th.textContent)).toEqual([
      "Name",
      "Source",
      "Automations",
      "Last run",
      "Actions",
    ]);
    const order = within(table)
      .getAllByRole("row")
      .slice(1)
      .map((row) => row.getAttribute("data-testid"));
    expect(order).toEqual(["workflow-row-my-draft", "workflow-row-triage", "workflow-row-builtin://agent"]);
    // The section survives as the Source column.
    expect(within(screen.getByTestId("workflow-row-builtin://agent")).getByText("Built-in")).toBeInTheDocument();
    expect(within(screen.getByTestId("workflow-row-triage")).getByText("Project")).toBeInTheDocument();
    const broken = screen.getByRole("list", { name: "Failed to load" });
    expect(within(broken).getByText("yaml: line 3: bad indent")).toBeInTheDocument();
    // It asked for hidden workflows too: this is the management view.
    expect(mocks.listWorkflows).toHaveBeenCalledWith(expect.objectContaining({ projectId: "proj-1", includeHidden: true }));
  });

  it("a workflow with no automation shows a dash under the Automations column, named for screen readers", async () => {
    mocks.listTriggers.mockResolvedValue({ triggers: [] });
    renderLibrary();
    const triage = await screen.findByTestId("workflow-row-triage");
    const cell = within(triage).getByTestId("workflow-row-automations");
    await waitFor(() => expect(cell).toHaveTextContent("—"));
    expect(within(cell).getByText("None")).toHaveClass("sr-only");
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

  it("search filters the rows and lands in the URL; Clear search brings them back", async () => {
    const { router } = renderLibrary();
    await screen.findByTestId("workflow-row-triage");
    await userEvent.type(screen.getByRole("searchbox", { name: "Search workflows" }), "coding agent");
    await waitFor(() => expect(router.state.location.search).toMatchObject({ q: "coding agent" }));
    expect(screen.queryByTestId("workflow-row-triage")).toBeNull();
    expect(screen.getByTestId("workflow-row-builtin://agent")).toBeInTheDocument();

    await userEvent.clear(screen.getByRole("searchbox", { name: "Search workflows" }));
    await userEvent.type(screen.getByRole("searchbox", { name: "Search workflows" }), "nothing like this");
    expect(await screen.findByText("No workflows match “nothing like this”.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Clear search" }));
    expect(await screen.findByTestId("workflow-row-triage")).toBeInTheDocument();
    expect(router.state.location.search).not.toHaveProperty("q");
  });

  it("reads source and sort from the URL: Built-in only, recently run first", async () => {
    mocks.lastRunPerWorkflow.mockResolvedValue({ runs: [protoRun("builtin://agent", "chat-a")] });
    renderWorkflowsPage(<LibraryPage />, "/workflows/library?source=%22builtin%22&sort=%22recent%22", "/workflows/library");
    expect(await screen.findByTestId("workflow-row-builtin://agent")).toBeInTheDocument();
    expect(screen.queryByTestId("workflow-row-triage")).toBeNull();
    expect(screen.queryByRole("list", { name: "Failed to load" })).toBeNull();
    expect(screen.getByRole("button", { name: "Source: Built-in" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sort: Recently run" })).toBeInTheDocument();
  });

  it("the Source menu writes the URL", async () => {
    const { router } = renderLibrary();
    await screen.findByTestId("workflow-row-triage");
    await userEvent.click(screen.getByRole("button", { name: "Source: All" }));
    const menu = await screen.findByRole("menu", { name: "Source" });
    await userEvent.click(within(menu).getByRole("menuitemradio", { name: "Failed to load" }));
    await waitFor(() => expect(router.state.location.search).toMatchObject({ source: "failed" }));
    expect(screen.queryByTestId("workflow-row-triage")).toBeNull();
    expect(screen.getByRole("list", { name: "Failed to load" })).toBeInTheDocument();
  });

  it("pins a workflow whose automation is failing to the top, flagged", async () => {
    mocks.listTriggers.mockResolvedValue({
      triggers: [
        protoTrigger("t1", "triage"),
        create(TriggerSchema, {
          ...protoTrigger("t2", "triage"),
          health: create(TriggerHealthSchema, { status: TriggerHealthStatus.FAILING, consecutiveFailures: 3 }),
        }),
      ],
    });
    renderLibrary();
    const table = await screen.findByRole("table", { name: "Workflows" });
    await waitFor(() => expect(screen.getByTestId("workflow-row-triage")).toHaveAttribute("data-attention", "true"));
    const rows = within(table).getAllByRole("row").slice(1);
    // First, and listed once.
    expect(rows[0]).toHaveAttribute("data-testid", "workflow-row-triage");
    expect(rows.filter((row) => row.getAttribute("data-testid") === "workflow-row-triage")).toHaveLength(1);
    expect(within(rows[0]!).getByTestId("workflow-row-failing")).toHaveTextContent("1 failing");
    expect(within(rows[0]!).getByLabelText("Needs attention")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent(/1 workflow needs attention/);
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

  it("Delete asks in the app, naming the title, slug and source — never a native confirm", async () => {
    const nativeConfirm = vi.spyOn(window, "confirm");
    mocks.deleteWorkflow.mockResolvedValue({});
    renderLibrary();
    const draft = await screen.findByTestId("workflow-row-my-draft");
    await userEvent.click(within(draft).getByRole("button", { name: "More actions for My Draft" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Delete" }));

    const dialog = await screen.findByRole("dialog", { name: "Delete “My Draft”?" });
    expect(dialog).toHaveTextContent("my-draft");
    expect(dialog).toHaveTextContent("Mine");
    expect(dialog).toHaveTextContent("draft");
    expect(nativeConfirm).not.toHaveBeenCalled();
    expect(mocks.deleteWorkflow).not.toHaveBeenCalled();

    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog", { name: "Delete “My Draft”?" })).toBeNull();
    expect(mocks.deleteWorkflow).not.toHaveBeenCalled();

    await userEvent.click(within(draft).getByRole("button", { name: "More actions for My Draft" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Delete" }));
    await userEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Delete workflow" }));
    await waitFor(() =>
      expect(mocks.deleteWorkflow).toHaveBeenCalledWith(expect.objectContaining({ projectId: "proj-1", name: "my-draft" })),
    );
    nativeConfirm.mockRestore();
  });
});
