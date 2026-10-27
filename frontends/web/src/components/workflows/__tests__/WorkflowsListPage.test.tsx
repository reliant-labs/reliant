// Copyright (c) 2025 Reliant Labs

/**
 * The Workflows tab (WORKFLOW_UI.md §2.2), against the real data layer with
 * only the RPC client mocked: one table of every workflow with, inline, what
 * runs it by itself (its triggers, health, next run and On switch) — there is
 * no Automations list any more — sections kept from the hub (Your workflows,
 * Built-in, Failed to load), each row's "last run" from LastRunPerWorkflow,
 * the filter chips, the All projects scope, the name linking to the
 * workflow's page, and Run… opening the run dialog.
 */

import { useAutomationDialogStore } from "../../Automations/dialog/openAutomationDialog";
import { beforeEach, describe, expect, it, vi, afterEach } from "vitest";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { serviceHandlers } from "@/test/serviceTransport";
import { queryClient as presetQueryClient } from "@/lib/query-client";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { create } from "@bufbuild/protobuf";

import { RunDisplayState } from "@/gen/reliant/v1/run_pb";
import { WorkflowDraftStatus } from "@/gen/reliant/v1/workflow_pb";
import { ScheduleSourceSchema, TriggerHealthSchema, TriggerHealthStatus, TriggerSchema, WorkflowTriggerSchema } from "@/gen/reliant/v1/trigger_pb";
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
  setTriggerEnabled: vi.fn(),
  disableWorkflow: vi.fn(),
  listProjects: vi.fn(),
}));

// PresetService and RunService are reached through connectClient (the
// generated hooks), so they are answered at the transport by the same fakes. Integration events are
// named from the catalog's trigger types; none here.
beforeEach(() => {
  setTransport(mockTransport({
      ...serviceHandlers("TriggerService", {
        listTriggers: mocks.listTriggers,
        setTriggerEnabled: mocks.setTriggerEnabled,
        disableWorkflow: mocks.disableWorkflow,
      }),
      // The run form lists workspaces through the generated WorktreeService hooks.
      ...serviceHandlers("WorktreeService", { listWorktrees: mocks.listWorktrees }),
      ...serviceHandlers("WorkflowService", { listWorkflows: mocks.listWorkflows, getWorkflow: mocks.getWorkflow, deleteWorkflow: mocks.deleteWorkflow }),
      ...serviceHandlers("PresetService", {
      listPresetsForWorkflow: mocks.listPresetsForWorkflow,
      getDefaultPresetsBatch: mocks.getDefaultPresetsBatch,
    }),
      ...serviceHandlers("RunService", { lastRunPerWorkflow: mocks.lastRunPerWorkflow }),
      "CatalogService.SearchCatalog": { response: {} },
      ...serviceHandlers("ProjectService", { listProjectDaemons: mocks.listProjectDaemons, listProjects: mocks.listProjects }),
      ...serviceHandlers("DaemonRegistryService", { listDaemons: mocks.listDaemons }),
    }));
});
afterEach(() => {
  setTransport(null);
  // The preset reads are cached in the app's query client; no test may see
  // another's answers.
  presetQueryClient.clear();
});

// The project list needs a session; here it is answered directly.
const projectList = vi.hoisted(() => ({ projects: [{ id: "proj-1", name: "Reliant" }] as Array<{ id: string; name: string }> }));
vi.mock("@/hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/project-queries")>()),
  useProjects: () => ({ projects: projectList.projects, isLoading: false, error: null, refetch: () => undefined }),
}));

vi.mock("@/store/projectStore", () => {
  const state = { currentProject: { id: "proj-1", name: "Reliant" }, projects: [], isLoading: false };
  const useProjectStore = Object.assign(
    (selector?: (s: typeof state) => unknown) => (selector ? selector(state) : state),
    { getState: () => state },
  );
  return { useProjectStore };
});

import { WorkflowsListPage } from "../list/WorkflowsListPage";
import { AutomationDialogHost } from "../../Automations/dialog/AutomationDialogHost";
import { closeAutomationDialog } from "../../Automations/dialog/openAutomationDialog";

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
  mocks.listProjects.mockResolvedValue({ projects: [{ id: "proj-1", name: "Reliant" }] });
  projectList.projects = [{ id: "proj-1", name: "Reliant" }];
  mocks.setTriggerEnabled.mockImplementation(async (req: { id: string; enabled: boolean }) => ({
    trigger: create(TriggerSchema, { ...protoTrigger(req.id, "triage"), enabled: req.enabled }),
  }));
  mocks.disableWorkflow.mockResolvedValue({ disabled: [], failed: [] });
  closeAutomationDialog();
});

/** The page and the dialog host its shell mounts, so Set up… and Add trigger… open something. */
function Page() {
  return (
    <>
      <WorkflowsListPage />
      <AutomationDialogHost />
    </>
  );
}

function renderLibrary(path = "/workflows/library") {
  return renderWorkflowsPage(<Page />, path, "/workflows/library");
}

describe("WorkflowsListPage", () => {
  it("is one table with labelled columns: yours first, then built-in; broken files below", async () => {
    renderLibrary();
    const table = await screen.findByRole("table", { name: "Workflows" });
    expect(within(table).getAllByRole("columnheader").map((th) => th.textContent)).toEqual([
      "Name",
      "Runs by itself",
      "Health",
      "Last run",
      "Next run",
      "On",
      "Actions",
    ]);
    const order = within(table)
      .getAllByRole("row")
      .slice(1)
      .map((row) => row.getAttribute("data-testid"));
    expect(order).toEqual(["workflow-row-my-draft", "workflow-row-triage", "workflow-row-builtin://agent"]);
    // The section survives as each row's source badge.
    expect(within(screen.getByTestId("workflow-row-builtin://agent")).getByText("Built-in")).toBeInTheDocument();
    expect(within(screen.getByTestId("workflow-row-triage")).getByText("Project")).toBeInTheDocument();
    const broken = screen.getByRole("list", { name: "Failed to load" });
    expect(within(broken).getByText("yaml: line 3: bad indent")).toBeInTheDocument();
    // It asked for hidden workflows too: this is the management view.
    expect(mocks.listWorkflows).toHaveBeenCalledWith(expect.objectContaining({ projectId: "proj-1", includeHidden: true }));
  });

  it("a workflow that nothing runs and declares no triggers says it runs only when started, and has no switch", async () => {
    mocks.listTriggers.mockResolvedValue({ triggers: [] });
    renderLibrary();
    const triage = await screen.findByTestId("workflow-row-triage");
    await waitFor(() => expect(within(triage).getByTestId("workflow-row-starts-on")).toHaveTextContent("Only when started"));
    expect(within(triage).queryByRole("switch")).toBeNull();
    // Its scheduling affordance is the row menu's Add trigger….
    await userEvent.click(within(triage).getByRole("button", { name: "More actions for Triage (project, triage)" }));
    expect(await screen.findByRole("menuitem", { name: "Add trigger…" })).toBeInTheDocument();
  });

  it("shows inline what runs it: its triggers, health, next run and its switch", async () => {
    renderLibrary();
    const triage = await screen.findByTestId("workflow-row-triage");
    await waitFor(() => expect(within(triage).getByTestId("workflow-row-starts-on")).toHaveTextContent(/2 triggers/));
    expect(within(triage).getByTestId("workflow-row-health")).toHaveTextContent(/Healthy|On/);
    expect(within(triage).getByRole("switch", { name: "Triage on (project, triage)" })).toBeChecked();
  });

  it("the switch turns every trigger of the row off, and back on", async () => {
    mocks.listTriggers.mockResolvedValue({
      triggers: [protoTrigger("t1", "triage"), create(TriggerSchema, { ...protoTrigger("t2", "triage"), enabled: false })],
    });
    renderLibrary();
    const triage = await screen.findByTestId("workflow-row-triage");
    const toggle = await within(triage).findByRole("switch", { name: "Triage on (project, triage)" });
    // Partly on reads as on; turning it off turns off what is on.
    expect(toggle).toBeChecked();
    await userEvent.click(toggle);
    await waitFor(() => expect(mocks.setTriggerEnabled).toHaveBeenCalledWith(expect.objectContaining({ id: "t1", enabled: false })));
    expect(mocks.setTriggerEnabled).not.toHaveBeenCalledWith(expect.objectContaining({ id: "t2" }));
  });

  it("shows each row's last run", async () => {
    renderLibrary();
    const triage = await screen.findByTestId("workflow-row-triage");
    await waitFor(() =>
      expect(within(triage).getByTestId("workflow-row-last-run")).toHaveTextContent(/Failed · 2 hours ago/),
    );
    expect(within(triage).getByRole("link", { name: /Failed · 2 hours ago/ })).toHaveAttribute(
      "href",
      "/workflows/runs/chat-tri",
    );

    // A builtin's triggers match on its stored ref.
    const agent = screen.getByTestId("workflow-row-builtin://agent");
    await waitFor(() => expect(within(agent).getByRole("switch")).toBeChecked());
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
    await userEvent.click(within(triage).getByRole("button", { name: "Run Triage (project, triage)" }));
    expect(await screen.findByRole("dialog", { name: "Run Triage" })).toBeInTheDocument();

    const draft = screen.getByTestId("workflow-row-my-draft");
    expect(within(draft).queryByRole("button", { name: /^Run / })).toBeNull();
    expect(within(draft).getByTestId("workflow-draft-badge")).toBeInTheDocument();
  });

  it("rows that share a title still have actions with unique names; the description is a description, not the name", async () => {
    // Two copies of the built-in Agent keep its title: three rows named "Agent".
    const response = libraryResponse();
    const agentCopy = (name: string, status: WorkflowDraftStatus) => ({
      ...response.workflows[0]!,
      name,
      filename: name,
      title: "Agent",
      source: "user",
      status,
      validationErrors: [],
    });
    response.workflows.push(agentCopy("high-maple", WorkflowDraftStatus.DRAFT), agentCopy("smart-seal", WorkflowDraftStatus.COMPLETE));
    mocks.listWorkflows.mockResolvedValue(response);
    renderLibrary();
    await screen.findByTestId("workflow-row-high-maple");

    const menus = screen.getAllByRole("button", { name: /^More actions for Agent/ }).map((b) => b.getAttribute("aria-label"));
    expect(menus.sort()).toEqual([
      "More actions for Agent (built-in, agent)",
      "More actions for Agent (draft, high-maple)",
      "More actions for Agent (mine, smart-seal)",
    ]);
    expect(new Set(screen.getAllByRole("button", { name: /^Run / }).map((b) => b.getAttribute("aria-label"))).size).toBe(
      screen.getAllByRole("button", { name: /^Run / }).length,
    );

    // The name is the title alone; the clipped description is read on request.
    const link = within(screen.getByTestId("workflow-row-triage")).getByRole("link", { name: "Triage" });
    expect(link).toHaveAttribute("aria-description", "Triage new issues");
    expect(within(screen.getByTestId("workflow-row-triage")).getByText("Triage new issues")).toHaveAttribute("aria-hidden", "true");
  });

  it("the row menu's Edit opens the builder", async () => {
    const { router } = renderLibrary();
    const triage = await screen.findByTestId("workflow-row-triage");
    await userEvent.click(within(triage).getByRole("button", { name: "More actions for Triage (project, triage)" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Edit" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/workflow/triage"));
  });

  it("search filters the rows and lands in the URL; Clear search brings them back", async () => {
    const { router } = renderLibrary();
    await screen.findByTestId("workflow-row-triage");
    // Each input change navigates the router. This verifies search/URL behavior,
    // not keyboard timing, so enter each complete value in one input event.
    fireEvent.change(screen.getByRole("searchbox", { name: "Search workflows" }), { target: { value: "coding agent" } });
    await waitFor(() => expect(router.state.location.search).toMatchObject({ q: "coding agent" }));
    expect(screen.queryByTestId("workflow-row-triage")).toBeNull();
    expect(screen.getByTestId("workflow-row-builtin://agent")).toBeInTheDocument();

    fireEvent.change(screen.getByRole("searchbox", { name: "Search workflows" }), { target: { value: "nothing like this" } });
    expect(await screen.findByText("No workflows match “nothing like this”.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Clear search" }));
    expect(await screen.findByTestId("workflow-row-triage")).toBeInTheDocument();
    expect(router.state.location.search).not.toHaveProperty("q");
  });

  it("reads the filter and sort from the URL: Built-in only, recently run first", async () => {
    mocks.lastRunPerWorkflow.mockResolvedValue({ runs: [protoRun("builtin://agent", "chat-a")] });
    renderLibrary("/workflows/library?show=%22builtin%22&sort=%22recent%22");
    expect(await screen.findByTestId("workflow-row-builtin://agent")).toBeInTheDocument();
    expect(screen.queryByTestId("workflow-row-triage")).toBeNull();
    expect(screen.queryByRole("list", { name: "Failed to load" })).toBeNull();
    expect(screen.getByRole("button", { name: "Built-in" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "Sort: Recently run" })).toBeInTheDocument();
  });

  it("the filter chips write the URL", async () => {
    const { router } = renderLibrary();
    await screen.findByTestId("workflow-row-triage");
    const chips = screen.getByRole("group", { name: "Show" });
    expect(within(chips).getAllByRole("button").map((b) => b.textContent)).toEqual([
      "All",
      "On",
      "Needs you",
      "Yours",
      "Built-in",
      "Failed to load",
    ]);
    await userEvent.click(within(chips).getByRole("button", { name: "Failed to load" }));
    await waitFor(() => expect(router.state.location.search).toMatchObject({ show: "failed" }));
    expect(screen.queryByTestId("workflow-row-triage")).toBeNull();
    expect(screen.getByRole("list", { name: "Failed to load" })).toBeInTheDocument();
  });

  it("On lists only what runs by itself — what the Automations tab used to", async () => {
    mocks.listTriggers.mockResolvedValue({ triggers: [protoTrigger("t1", "triage")] });
    renderLibrary("/workflows/library?show=%22on%22");
    expect(await screen.findByTestId("workflow-row-triage")).toBeInTheDocument();
    expect(screen.queryByTestId("workflow-row-builtin://agent")).toBeNull();
    expect(screen.queryByTestId("workflow-row-my-draft")).toBeNull();
  });

  it("sums up what runs by itself above the table", async () => {
    renderLibrary();
    const counts = await screen.findByTestId("automation-counts");
    // Three hand-made triggers (two for triage, one for agent), each its own automation; all healthy.
    await waitFor(() => expect(counts).toHaveTextContent(/3\s*on and healthy/));
  });

  it("across every project, a project's own workflow is a row per project, named with it", async () => {
    projectList.projects = [{ id: "proj-1", name: "Reliant" }, { id: "proj-2", name: "Website" }];
    mocks.listWorkflows.mockImplementation(async (req: { projectId: string }) => {
      const response = libraryResponse();
      return req.projectId === "proj-2" ? { ...response, invalidWorkflows: [] } : response;
    });
    mocks.listTriggers.mockResolvedValue({
      triggers: [protoTrigger("t1", "triage"), create(TriggerSchema, { ...protoTrigger("t9", "triage"), projectId: "proj-2", projectName: "Website" })],
    });
    renderLibrary("/workflows/library?allProjects=true");
    const reliant = await screen.findByTestId("workflow-row-triage-proj-1");
    const website = await screen.findByTestId("workflow-row-triage-proj-2");
    expect(reliant).toHaveTextContent("Reliant");
    expect(website).toHaveTextContent("Website");
    // A built-in is one row whatever the project count.
    expect(await screen.findAllByTestId("workflow-row-builtin://agent")).toHaveLength(1);
    expect(screen.queryByTestId("workflow-missing-badge")).toBeNull();
    // Every project's list was read; triggers from every project.
    await waitFor(() => expect(mocks.listWorkflows).toHaveBeenCalledWith(expect.objectContaining({ projectId: "proj-2" })));
    expect(mocks.listTriggers).toHaveBeenCalledWith(expect.not.objectContaining({ projectId: expect.anything() }));
  });

  it("a trigger whose workflow is listed nowhere still gets a row, so it can be turned off", async () => {
    mocks.listTriggers.mockResolvedValue({ triggers: [protoTrigger("t1", "gone-flow")] });
    renderLibrary();
    const orphan = await screen.findByTestId("workflow-row-gone-flow-proj-1");
    expect(within(orphan).getByTestId("workflow-missing-badge")).toHaveTextContent("Workflow not found");
    expect(within(orphan).getByRole("switch")).toBeChecked();
  });

  it("pins a workflow whose trigger is failing to the top, flagged", async () => {
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
    expect(within(rows[0]!).getByTestId("workflow-row-health")).toHaveTextContent("3 failed in a row");
    expect(within(rows[0]!).getByLabelText("Needs you")).toBeInTheDocument();
    // The state's one call to action sits on the row.
    expect(within(rows[0]!).getByRole("button", { name: "Open last run for Triage" })).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent(/1 workflow needs you/);
  });

  it("offers Set up… on a workflow that could run by itself and is not on here, and says Off", async () => {
    const library = libraryResponse();
    library.workflows[1] = {
      ...library.workflows[1]!,
      triggers: [create(WorkflowTriggerSchema, { name: "nightly", source: { case: "schedule", value: create(ScheduleSourceSchema, { cron: ["0 6 * * *"], timezone: "UTC" }) } })],
    } as (typeof library.workflows)[number];
    mocks.listWorkflows.mockResolvedValue(library);
    mocks.listTriggers.mockResolvedValue({ triggers: [] });
    renderLibrary();
    const triage = await screen.findByTestId("workflow-row-triage");
    await waitFor(() => expect(within(triage).getByTestId("workflow-row-starts-on")).toHaveTextContent(/^Off · /));
    await userEvent.click(within(triage).getByRole("button", { name: "Set up Triage (project, triage)" }));
    // The one automation dialog, for this workflow's declared triggers.
    expect(useAutomationDialogStore.getState().request).toMatchObject({ workflowRef: "triage", mode: "setup", showOpenWorkflow: true });
    // A workflow that declares nothing has no Set up.
    expect(within(screen.getByTestId("workflow-row-builtin://agent")).queryByRole("button", { name: /^Set up/ })).toBeNull();
  });

  it("a user workflow's menu offers Export and Delete; a built-in's does not", async () => {
    renderLibrary();
    const draft = await screen.findByTestId("workflow-row-my-draft");
    await userEvent.click(within(draft).getByRole("button", { name: "More actions for My Draft (draft, my-draft)" }));
    const menu = await screen.findByRole("menu");
    expect(within(menu).getByRole("menuitem", { name: "Delete" })).toBeInTheDocument();
    expect(within(menu).getByRole("menuitem", { name: "Export" })).toBeInTheDocument();
    // A draft cannot be the default.
    expect(within(menu).queryByRole("menuitem", { name: "Set as default" })).toBeNull();
    await userEvent.keyboard("{Escape}");

    const agent = screen.getByTestId("workflow-row-builtin://agent");
    await userEvent.click(within(agent).getByRole("button", { name: "More actions for Agent (built-in, agent)" }));
    const builtinMenu = await screen.findByRole("menu");
    expect(within(builtinMenu).queryByRole("menuitem", { name: "Delete" })).toBeNull();
    expect(within(builtinMenu).getByRole("menuitem", { name: "Duplicate" })).toBeInTheDocument();
  });

  it("Delete lists the triggers it takes with the workflow, in any project", async () => {
    mocks.deleteWorkflow.mockResolvedValue({});
    mocks.listTriggers.mockResolvedValue({
      triggers: [
        protoTrigger("t1", "my-draft", "Autopilot · PR review · pr-opened"),
        protoTrigger("t2", "project://My Draft", "Autopilot · PR review · push"),
        protoTrigger("t3", "triage"),
        protoTrigger("t4", "builtin://my-draft", "Built-in one"),
      ],
    });
    renderLibrary();
    const draft = await screen.findByTestId("workflow-row-my-draft");
    await userEvent.click(within(draft).getByRole("button", { name: "More actions for My Draft (draft, my-draft)" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Delete" }));

    const dialog = await screen.findByRole("dialog", { name: "Delete “My Draft”?" });
    await waitFor(() => expect(dialog).toHaveTextContent("Also deletes its 2 triggers"));
    expect(dialog).toHaveTextContent("Autopilot · PR review · pr-opened");
    expect(dialog).toHaveTextContent("Autopilot · PR review · push");
    expect(dialog).not.toHaveTextContent("Automation t3");
    expect(dialog).not.toHaveTextContent("Built-in one");
  });

  it("Delete says nothing about triggers when none run the workflow", async () => {
    renderLibrary();
    const draft = await screen.findByTestId("workflow-row-my-draft");
    await userEvent.click(within(draft).getByRole("button", { name: "More actions for My Draft (draft, my-draft)" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Delete" }));

    const dialog = await screen.findByRole("dialog", { name: "Delete “My Draft”?" });
    expect(dialog).not.toHaveTextContent("trigger");
  });

  it("Delete asks in the app, naming the title, slug and source — never a native confirm", async () => {
    const nativeConfirm = vi.spyOn(window, "confirm");
    mocks.deleteWorkflow.mockResolvedValue({});
    renderLibrary();
    const draft = await screen.findByTestId("workflow-row-my-draft");
    await userEvent.click(within(draft).getByRole("button", { name: "More actions for My Draft (draft, my-draft)" }));
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

    await userEvent.click(within(draft).getByRole("button", { name: "More actions for My Draft (draft, my-draft)" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Delete" }));
    await userEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Delete workflow" }));
    await waitFor(() =>
      expect(mocks.deleteWorkflow).toHaveBeenCalledWith(expect.objectContaining({ projectId: "proj-1", name: "my-draft" })),
    );
    nativeConfirm.mockRestore();
  });
});
