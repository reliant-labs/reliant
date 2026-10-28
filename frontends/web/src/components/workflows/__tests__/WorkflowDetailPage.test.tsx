// Copyright (c) 2025 Reliant Labs

/**
 * Workflow detail (WORKFLOW_UI.md §2.3), against the real data layer with only
 * the RPC client mocked: its recent runs (the run log, filtered to the
 * workflow), its Triggers card — every trigger that runs it, declared or
 * added by hand, per project, with switches, Settings, Fire trigger now and
 * history — its presets, its inputs, and the actions: Run… opens the run
 * dialog, Edit opens the builder, Add trigger… opens the automation dialog.
 */

import { beforeEach, describe, expect, it, vi, afterEach } from "vitest";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { serviceHandlers } from "@/test/serviceTransport";
import { queryClient as presetQueryClient } from "@/lib/query-client";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import { RunDisplayState } from "@/gen/reliant/v1/run_pb";
import { CelStringSchema, NodeSchema, SubWorkflowArgsSchema, WorkflowSchema } from "@/gen/reliant/v1/workflow_v2_pb";
import { GetWorkflowResponseSchema } from "@/gen/reliant/v1/workflow_pb";
import { DisableWorkflowResponseSchema, ScheduleSourceSchema, TriggerHealthSchema, TriggerHealthStatus, TriggerSchema, WorkflowTriggerSchema } from "@/gen/reliant/v1/trigger_pb";
import { getWorkflowByName, presetsResponse } from "../../workflow/run/__tests__/runFormFixtures";
import { libraryResponse, protoRun, protoTrigger, renderWorkflowsPage } from "./workflowsTestUtils";

const mocks = vi.hoisted(() => ({
  listWorkflows: vi.fn(),
  getWorkflow: vi.fn(),
  listRuns: vi.fn(),
  listTriggers: vi.fn(),
  getTrigger: vi.fn(),
  disableWorkflow: vi.fn(),
  setTriggerEnabled: vi.fn(),
  fireTrigger: vi.fn(),
  listPresetsForWorkflow: vi.fn(),
  getDefaultPresetsBatch: vi.fn(),
  getDefaultPresets: vi.fn(),
  listWorktrees: vi.fn(),
  listDaemons: vi.fn(),
  listProjectDaemons: vi.fn(),
}));

// PresetService and RunService are reached through connectClient (the
// generated hooks), so they are answered at the transport by the same fakes. Integration events are
// named from the catalog's trigger types; none here.
beforeEach(() => {
  setTransport(mockTransport({
      ...serviceHandlers("TriggerService", { listTriggers: mocks.listTriggers, getTrigger: mocks.getTrigger, disableWorkflow: mocks.disableWorkflow, setTriggerEnabled: mocks.setTriggerEnabled, fireTrigger: mocks.fireTrigger }),
      // The run form lists workspaces through the generated WorktreeService hooks.
      ...serviceHandlers("WorktreeService", { listWorktrees: mocks.listWorktrees }),
      ...serviceHandlers("WorkflowService", { listWorkflows: mocks.listWorkflows, getWorkflow: mocks.getWorkflow }),
      ...serviceHandlers("PresetService", {
      listPresetsForWorkflow: mocks.listPresetsForWorkflow,
      getDefaultPresetsBatch: mocks.getDefaultPresetsBatch,
      getDefaultPresets: mocks.getDefaultPresets,
    }),
      ...serviceHandlers("RunService", { listRuns: mocks.listRuns }),
      "CatalogService.SearchCatalog": { response: {} },
      ...serviceHandlers("ProjectService", { listProjectDaemons: mocks.listProjectDaemons }),
      ...serviceHandlers("DaemonRegistryService", { listDaemons: mocks.listDaemons }),
    }));
});
afterEach(() => {
  setTransport(null);
  // The preset reads are cached in the app's query client; no test may see
  // another's answers.
  presetQueryClient.clear();
});

vi.mock("@/store/projectStore", () => {
  const state = {
    currentProject: { id: "proj-1", name: "Reliant" },
    projects: [{ id: "proj-1", name: "Reliant" }],
    isLoading: false,
    loadProjects: async () => undefined,
  };
  const useProjectStore = Object.assign(
    (selector?: (s: typeof state) => unknown) => (selector ? selector(state) : state),
    { getState: () => state },
  );
  return { useProjectStore };
});

// The diagram is ReactFlow; what it draws is the viewer's own business.
vi.mock("../../workflow/WorkflowViewerPanel", () => ({
  WorkflowViewerPanel: ({ workflowName }: { workflowName: string }) => (
    <div data-testid="viewer-panel">{workflowName}</div>
  ),
}));

import { WorkflowDetailPage } from "../detail/WorkflowDetailPage";
import { AutomationDialogHost } from "../../Automations/dialog/AutomationDialogHost";
import { closeAutomationDialog } from "../../Automations/dialog/openAutomationDialog";

beforeEach(() => {
  for (const mock of Object.values(mocks)) mock.mockReset();
  mocks.listWorkflows.mockResolvedValue(libraryResponse());
  mocks.getWorkflow.mockImplementation(async (request: { name: string }) => getWorkflowByName(request));
  mocks.listRuns.mockResolvedValue({
    runs: [
      protoRun("triage", "run-a", RunDisplayState.COMPLETED),
      protoRun("triage", "run-b", RunDisplayState.FAILED),
    ],
    nextPageToken: "",
  });
  mocks.listTriggers.mockResolvedValue({
    triggers: [protoTrigger("t1", "triage", "Morning triage"), protoTrigger("t2", "builtin://agent", "Other")],
  });
  mocks.getTrigger.mockImplementation(async ({ id }: { id: string }) => ({ trigger: protoTrigger(id, "triage", "Morning triage") }));
  mocks.listPresetsForWorkflow.mockResolvedValue(presetsResponse());
  mocks.getDefaultPresetsBatch.mockResolvedValue({ defaults: {} });
  mocks.getDefaultPresets.mockResolvedValue({ defaults: {} });
  mocks.listWorktrees.mockResolvedValue({ worktrees: [], total: 0 });
  mocks.listDaemons.mockResolvedValue({ daemons: [] });
  mocks.listProjectDaemons.mockResolvedValue({ projectDaemons: [] });
  mocks.fireTrigger.mockResolvedValue({ fireWorkflowId: "fire-1" });
  closeAutomationDialog();
});

function renderDetail(ref = "triage", query = "") {
  return renderWorkflowsPage(
    <>
      <WorkflowDetailPage />
      <AutomationDialogHost />
    </>,
    `/workflows/library/${encodeURIComponent(ref)}${query}`,
    "/workflows/library/$workflowRef",
  );
}

/** A schedule declaration, as both GetWorkflow and ListWorkflows carry it. */
const declaration = (name: string, cron: string) =>
  create(WorkflowTriggerSchema, { name, source: { case: "schedule", value: create(ScheduleSourceSchema, { cron: [cron], timezone: "UTC" }) } });

/** The library, with triage declaring these triggers. */
function libraryDeclaring(...triggers: ReturnType<typeof declaration>[]) {
  const library = libraryResponse();
  library.workflows[1] = { ...library.workflows[1]!, triggers } as (typeof library.workflows)[number];
  return library;
}

describe("WorkflowDetailPage", () => {
  it("shows the workflow's recent runs, filtered to it", async () => {
    renderDetail();
    expect(await screen.findByRole("heading", { level: 1, name: "Triage" })).toBeInTheDocument();
    const runs = await screen.findByTestId("workflow-detail-runs");
    expect(within(runs).getAllByRole("link", { name: /Run of triage/ })).toHaveLength(2);
    // The run log: trigger events that started no run come with the runs.
    expect(mocks.listRuns).toHaveBeenCalledWith(
      expect.objectContaining({ projectId: "proj-1", workflow: ["triage"], includeUnlaunchedEvents: true }),
    );
    const viewAll = screen.getByRole("link", { name: "View all" });
    expect(viewAll.getAttribute("href")).toMatch(/^\/workflows\/runs\?/);
    expect(decodeURIComponent(viewAll.getAttribute("href") ?? "")).toContain('workflow=["triage"]');
  });

  it("lists the triggers that run this workflow — a hand-made one says so — and no other's", async () => {
    renderDetail();
    const card = await screen.findByTestId("triggers-card");
    const row = await within(card).findByTestId("trigger-row-t1");
    expect(row).toHaveAttribute("id", "trigger-t1");
    expect(row).toHaveTextContent("Morning triage");
    expect(within(row).getByText("Added here")).toBeInTheDocument();
    expect(within(card).queryByText("Other")).toBeNull();
  });

  it("says a workflow that declares a schedule is off, and offers Set up… instead of 'Nothing runs this workflow'", async () => {
    // ci-triage declares `triggers: - schedule: cron [0 6 * * 1-5]` and has
    // no activation, so nothing runs it YET.
    mocks.listTriggers.mockResolvedValue({ triggers: [], lastUserUpdateSequence: "0" });
    mocks.listWorkflows.mockResolvedValue(libraryDeclaring(declaration("weekday-morning", "0 6 * * 1-5")));
    mocks.getWorkflow.mockImplementation(async (request: { name: string }) =>
      request.name === "triage"
        ? create(GetWorkflowResponseSchema, {
            source: "project",
            workflow: create(WorkflowSchema, {
              name: "triage",
              inputs: {},
              triggers: [
                create(WorkflowTriggerSchema, {
                  name: "weekday-morning",
                  source: { case: "schedule", value: create(ScheduleSourceSchema, { cron: ["0 6 * * 1-5"], timezone: "UTC" }) },
                }),
              ],
            }),
          })
        : getWorkflowByName(request),
    );
    renderDetail();

    const off = await screen.findByTestId("triggers-not-set-up");
    expect(off).toHaveTextContent("Off in Reliant:");
    expect(off).toHaveTextContent("weekday-morning");
    expect(within(off).getByText("From the workflow file")).toBeInTheDocument();
    await userEvent.click(within(off).getByRole("button", { name: "Set up…" }));
    expect(await screen.findByRole("form", { name: "Turn on Triage" })).toBeInTheDocument();
  });

  it("a workflow whose Chat trigger is off can still be run by hand: Run… is how it is run", async () => {
    const library = libraryResponse();
    library.workflows[1] = { ...library.workflows[1]!, automationOnly: true } as (typeof library.workflows)[number];
    mocks.listWorkflows.mockResolvedValue(library);
    renderDetail();
    await screen.findByRole("heading", { level: 1, name: "Triage" });
    expect(screen.queryByText(/Its Chat trigger is off/)).toBeNull();
    expect(screen.getByRole("button", { name: "Run…" })).toBeEnabled();
  });

  it("shows the presets that fit it, and its inputs", async () => {
    renderDetail();
    const presets = await screen.findByRole("list", { name: "Presets for this workflow" });
    expect(within(presets).getByTestId("preset-row-careful")).toBeInTheDocument();
    expect(within(presets).getByTestId("preset-row-strict")).toBeInTheDocument();
    expect(mocks.listPresetsForWorkflow).toHaveBeenCalledWith(
      expect.objectContaining({ projectId: "proj-1", workflowName: "triage", includeHidden: true }),
    );

    const inputs = await screen.findByTestId("workflow-detail-inputs");
    expect(within(inputs).getByText("depth")).toBeInTheDocument();
    expect(within(inputs).getByText("label")).toBeInTheDocument();
    expect(within(inputs).getByText("review.strictness")).toBeInTheDocument();
  });

  it("Run… opens the run dialog for this workflow", async () => {
    renderDetail();
    await screen.findByRole("heading", { level: 1, name: "Triage" });
    await userEvent.click(screen.getByRole("button", { name: "Run…" }));
    expect(await screen.findByRole("dialog", { name: "Run Triage" })).toBeInTheDocument();
  });

  it("a trigger's Edit entry loads by id into the real unified form", async () => {
    renderDetail();
    const row = await screen.findByTestId("trigger-row-t1");
    await userEvent.click(within(row).getByRole("button", { expanded: false }));
    await userEvent.click(within(row).getByRole("button", { name: "Edit" }));
    const dialog = await screen.findByRole("dialog", { name: "Edit automation" });
    expect(within(dialog).getByLabelText("What should it do?")).toHaveValue("Go");
    expect(within(dialog).getByRole("heading", { name: "When" })).toBeInTheDocument();
    expect(mocks.getTrigger).toHaveBeenCalledWith(expect.objectContaining({ id: "t1" }));
    expect(within(dialog).getByRole("button", { name: "Save changes" })).toBeInTheDocument();
  });

  it("Edit opens the builder", async () => {
    const { router } = renderDetail();
    await screen.findByRole("heading", { level: 1, name: "Triage" });
    await userEvent.click(screen.getByRole("button", { name: "Edit" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/workflow/triage"));
  });

  it("Add trigger… opens the automation form with this workflow; the header has only Run… and Edit", async () => {
    renderDetail();
    await screen.findByRole("heading", { level: 1, name: "Triage" });
    expect(screen.queryByRole("button", { name: "New automation" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Enable" })).toBeNull();
    await userEvent.click(await screen.findByRole("button", { name: "Add trigger…" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("heading", { name: "When" })).toBeInTheDocument();
    expect(within(dialog).getByRole("heading", { name: "Do" })).toBeInTheDocument();
    expect(within(dialog).queryByLabelText("Name")).toBeNull();
    expect(await within(dialog).findByText("Triage", { selector: "span.font-medium" })).toBeInTheDocument();
    expect(within(dialog).queryByLabelText("Workflow")).toBeNull();
  });

  it("a draft cannot run, and says why in text", async () => {
    renderDetail("my-draft");
    await screen.findByRole("heading", { level: 1, name: "My Draft" });
    expect(screen.getByRole("button", { name: "Run…" })).toBeDisabled();
    expect(screen.getByText("Drafts can't run until they are published. Open it in the editor to publish it.")).toBeInTheDocument();
  });

  it("Used by lists the workflows that call this one through ref:, from the loaded library", async () => {
    const callsTriage = create(NodeSchema, {
      id: "triage-each",
      type: "workflow",
      args: {
        case: "workflow",
        value: create(SubWorkflowArgsSchema, {
          ref: create(CelStringSchema, { value: { case: "literal", value: "project://triage" } }),
        }),
      },
    });
    const base = libraryResponse();
    mocks.listWorkflows.mockResolvedValue({
      ...base,
      workflows: [
        ...base.workflows,
        { ...base.workflows[1]!, name: "nightly-sweep", filename: "nightly-sweep", description: "Sweeps", nodes: [callsTriage] },
      ],
    });
    renderDetail();
    const usedBy = await screen.findByTestId("workflow-detail-used-by");
    expect(within(usedBy).getByRole("link", { name: "Nightly Sweep" })).toHaveAttribute(
      "href",
      "/workflows/library/nightly-sweep",
    );
    expect(within(usedBy).getAllByRole("link")).toHaveLength(1);
    // A scan of the library list detail already loads, not a request of its
    // own. (useWorkflowDefinition makes its own include_hidden=false call.)
    const libraryCalls = mocks.listWorkflows.mock.calls.filter(([request]) => request.includeHidden);
    expect(libraryCalls).toHaveLength(1);
  });

  it("Used by says so when nothing calls it", async () => {
    renderDetail();
    const usedBy = await screen.findByRole("region", { name: "Used by" });
    expect(within(usedBy).getByText("No other workflow calls this one.")).toBeInTheDocument();
  });

  it("a workflow that no longer exists says so and links back", async () => {
    renderDetail("gone");
    expect(await screen.findByText("This workflow no longer exists.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to workflows" })).toHaveAttribute("href", "/workflows/library");
  });

  it("a workflow that no longer exists still lists the triggers that run it, so they can be removed", async () => {
    mocks.listTriggers.mockResolvedValue({ triggers: [protoTrigger("t9", "gone", "Orphaned schedule")] });
    renderDetail("gone");
    expect(await screen.findByText("This workflow no longer exists.")).toBeInTheDocument();
    expect(await screen.findByTestId("trigger-row-t9")).toHaveTextContent("Orphaned schedule");
    expect(screen.getByRole("button", { name: "Add trigger…" })).toBeDisabled();
  });

  describe("triggers", () => {
    const declaresTwo = (request: { name: string }) =>
      request.name === "triage"
        ? create(GetWorkflowResponseSchema, {
            source: "project",
            workflow: create(WorkflowSchema, {
              name: "triage",
              inputs: {},
              triggers: [declaration("morning", "0 6 * * *"), declaration("evening", "0 18 * * *")],
            }),
          })
        : getWorkflowByName(request);

    const activation = (id: string, declared: string, enabled: boolean, projectId = "proj-1") => {
      const t = protoTrigger(id, "triage", `Triage · ${declared}`);
      t.workflowTrigger = declared;
      t.enabled = enabled;
      t.projectId = projectId;
      t.projectName = projectId === "proj-1" ? "Reliant" : "Website";
      return t;
    };

    beforeEach(() => {
      mocks.getWorkflow.mockImplementation(async (request: { name: string }) => declaresTwo(request));
      mocks.listWorkflows.mockResolvedValue(libraryDeclaring(declaration("morning", "0 6 * * *"), declaration("evening", "0 18 * * *")));
    });

    it("groups them per project, with its state, Settings and a switch; each trigger has a row of its own", async () => {
      mocks.listTriggers.mockResolvedValue({ triggers: [activation("a1", "morning", true), activation("a2", "evening", true)] });
      renderDetail();

      const group = await screen.findByTestId("triggers-project-proj-1");
      expect(group).toHaveAttribute("id", "automation-proj-1");
      expect(within(group).getByText("Reliant")).toBeInTheDocument();
      expect(within(group).getByRole("switch", { name: "On in Reliant" })).toHaveAttribute("aria-checked", "true");
      expect(within(group).getByRole("button", { name: "Settings for Reliant" })).toBeInTheDocument();
      // One group for the two triggers; each is a row inside it, from the workflow file.
      expect(screen.getAllByTestId(/^triggers-project-/)).toHaveLength(1);
      const rows = within(group).getAllByTestId(/^trigger-row-/);
      expect(rows.map((row) => row.getAttribute("id"))).toEqual(["trigger-a2", "trigger-a1"]);
      expect(within(rows[0]!).getByText("From the workflow file")).toBeInTheDocument();
      // Every declared trigger is set up here: nothing is listed as off.
      expect(screen.queryByTestId("triggers-not-set-up")).toBeNull();
    });

    it("turning one declared trigger off leaves its enabled sibling alone", async () => {
      const first = activation("a1", "morning", true);
      const sibling = activation("a2", "evening", true);
      mocks.listTriggers.mockResolvedValue({ triggers: [first, sibling] });
      mocks.setTriggerEnabled.mockImplementation(async ({ id, enabled }: { id: string; enabled: boolean }) => {
        if (id === first.id) first.enabled = enabled;
        return { trigger: first };
      });
      renderDetail();
      await userEvent.click(await screen.findByRole("switch", { name: "morning on" }));
      await waitFor(() => expect(mocks.setTriggerEnabled).toHaveBeenCalledWith(expect.objectContaining({ id: "a1", enabled: false })));
      expect(mocks.setTriggerEnabled).toHaveBeenCalledTimes(1);
      expect(mocks.disableWorkflow).not.toHaveBeenCalled();
      expect(sibling.enabled).toBe(true);
      expect(screen.getByRole("switch", { name: "evening on" })).toHaveAttribute("aria-checked", "true");
    });

    it("turning a project off turns the workflow off there in one call", async () => {
      const user = userEvent.setup();
      mocks.listTriggers.mockResolvedValue({ triggers: [activation("a1", "morning", true), activation("a2", "evening", true)] });
      mocks.disableWorkflow.mockResolvedValue(create(DisableWorkflowResponseSchema, { paused: [create(TriggerSchema, { id: "a1" }), create(TriggerSchema, { id: "a2" })] }));
      renderDetail();

      await user.click(await screen.findByRole("switch", { name: "On in Reliant" }));
      await waitFor(() => expect(mocks.disableWorkflow).toHaveBeenCalledTimes(1));
      expect(mocks.disableWorkflow.mock.calls[0]![0]).toMatchObject({ workflow: "triage", projectId: "proj-1" });
    });

    it("a trigger row expands to its definition, Fire trigger now and its history in the run log", async () => {
      const user = userEvent.setup();
      mocks.listTriggers.mockResolvedValue({ triggers: [activation("a1", "morning", true), activation("a2", "evening", true)] });
      renderDetail();

      const row = await screen.findByTestId("trigger-row-a1");
      await user.click(within(row).getByRole("button", { expanded: false }));
      expect(within(row).getByTestId("trigger-definition")).toHaveTextContent("Runs on");
      const history = within(row).getByTestId("trigger-history");
      await waitFor(() => expect(mocks.listRuns).toHaveBeenCalledWith(expect.objectContaining({ triggerId: "a1", includeUnlaunchedEvents: true })));
      expect(decodeURIComponent(within(history).getByRole("link", { name: "View all in Runs" }).getAttribute("href") ?? "")).toContain("trigger=a1");

      // Fire trigger now: the trigger fires, unattended — not an attended Run….
      await user.click(within(row).getByRole("button", { name: "Fire trigger now" }));
      await waitFor(() => expect(mocks.fireTrigger).toHaveBeenCalledWith(expect.objectContaining({ id: "a1" })));
      // A declared trigger is configured with its project's: Settings, and no Delete while it works.
      expect(within(row).getByRole("button", { name: "Settings" })).toBeInTheDocument();
      expect(within(row).queryByRole("button", { name: "Remove trigger" })).toBeNull();
    });

    it("keeps configuration changes separate from firings, with agent and user attribution and snapshots", async () => {
      const trigger = activation("a1", "morning", true);
      mocks.listTriggers.mockResolvedValue({ triggers: [trigger] });
      mocks.getTrigger.mockResolvedValue({ trigger: {
        ...trigger,
        changes: [
          { id: "change-1", operation: "update", userId: "owner", chatId: "caller-chat", runId: "caller-run", toolCallId: "call-1", beforeState: '{"DaemonID":"old"}', afterState: '{"DaemonID":"cicd","Params":"[redacted]"}', createdAt: "2026-10-27T04:00:00Z" },
          { id: "change-2", operation: "create", userId: "owner", beforeState: "", afterState: '{"enabled":true}', createdAt: "2026-10-26T04:00:00Z" },
        ],
      } });
      renderDetail();
      const row = await screen.findByTestId("trigger-row-a1");
      expect(mocks.getTrigger).not.toHaveBeenCalled();
      await userEvent.click(within(row).getByRole("button", { expanded: false }));
      const history = await within(row).findByRole("region", { name: "Configuration history" });
      expect(await within(history).findByText(/Agent in chat caller-chat/)).toBeInTheDocument();
      expect(mocks.getTrigger).toHaveBeenCalledWith(expect.objectContaining({ id: "a1" }));
      await userEvent.click(within(history).getByText(/Agent in chat caller-chat/));
      expect(within(history).getByText(/Thread caller-run · Call call-1/)).toBeVisible();
      expect(within(history).getByText(/Before:.*old/)).toHaveTextContent('After: {"DaemonID":"cicd","Params":"[redacted]"}');
      expect(within(history).getByText(/User owner/)).toBeInTheDocument();
      expect(within(history).getByText(/Before: \(new activation\)/)).toBeInTheDocument();
      const firings = within(row).getByTestId("trigger-history");
      expect(within(firings).queryByText(/caller-chat/)).toBeNull();
      expect(within(firings).getByRole("link", { name: "View all in Runs" })).toBeInTheDocument();
    });

    it("shows the configuration history empty state only after an authorized detail read", async () => {
      mocks.listTriggers.mockResolvedValue({ triggers: [activation("a1", "morning", true)] });
      renderDetail();
      const row = await screen.findByTestId("trigger-row-a1");
      await userEvent.click(within(row).getByRole("button", { expanded: false }));
      expect(await within(row).findByText("No configuration changes recorded yet.")).toBeInTheDocument();
      expect(mocks.getTrigger).toHaveBeenCalledWith(expect.objectContaining({ id: "a1" }));
    });

    it("does not use list data as configuration history when GetTrigger refuses access", async () => {
      mocks.listTriggers.mockResolvedValue({ triggers: [activation("a1", "morning", true)] });
      mocks.getTrigger.mockRejectedValue(new ConnectError("not yours", Code.PermissionDenied));
      renderDetail();
      const row = await screen.findByTestId("trigger-row-a1");
      await userEvent.click(within(row).getByRole("button", { expanded: false }));
      expect(await within(row).findByText("Configuration history could not be loaded.")).toHaveAttribute("role", "alert");
      expect(within(row).queryByText("No configuration changes recorded yet.")).toBeNull();
      expect(within(row).getByTestId("trigger-history")).toBeInTheDocument();
    });

    it("calls out a trigger that needs you under the header, with its one call to action", async () => {
      const broken = activation("a1", "morning", true);
      broken.health = create(TriggerHealthSchema, {
        status: TriggerHealthStatus.BROKEN,
        lastFailureDetail: "the workflow no longer declares a trigger named morning",
      });
      mocks.listTriggers.mockResolvedValue({ triggers: [broken] });
      renderDetail();

      const notice = await screen.findByTestId("automation-state-notice");
      expect(notice).toHaveAttribute("data-state", "broken");
      expect(notice).toHaveTextContent("Triage in Reliant can't run: its workflow no longer declares this trigger.");
      expect(within(notice).getByRole("button", { name: "Open workflow" })).toBeInTheDocument();
      // The project's group carries the same action instead of Settings.
      const group = screen.getByTestId("triggers-project-proj-1");
      expect(within(group).getByRole("button", { name: "Open workflow for Reliant" })).toBeInTheDocument();
    });

    it("opens and scrolls to the trigger the address names", async () => {
      window.location.hash = "#trigger-a1";
      mocks.listTriggers.mockResolvedValue({ triggers: [activation("a1", "morning", true)] });
      renderDetail();

      const row = await screen.findByTestId("trigger-row-a1");
      await waitFor(() => expect(within(row).getByTestId("trigger-definition")).toBeInTheDocument());
      window.location.hash = "";
    });

    it("lists the triggers in scope; another project's are a link away", async () => {
      const user = userEvent.setup();
      mocks.listTriggers.mockResolvedValue({ triggers: [activation("a1", "morning", true), activation("w1", "morning", true, "proj-2")] });
      const { router } = renderDetail();

      await screen.findByTestId("triggers-project-proj-1");
      expect(screen.queryByTestId("triggers-project-proj-2")).toBeNull();
      await user.click(await screen.findByRole("button", { name: "Show all projects" }));
      await waitFor(() => expect(router.state.location.search).toMatchObject({ allProjects: true }));
      expect(await screen.findByTestId("triggers-project-proj-2")).toHaveTextContent("Website");
    });

    it("says a workflow that declares nothing and that nothing runs is off, with Add trigger… in the header", async () => {
      mocks.listTriggers.mockResolvedValue({ triggers: [] });
      mocks.listWorkflows.mockResolvedValue(libraryResponse());
      mocks.getWorkflow.mockImplementation(async (request: { name: string }) => getWorkflowByName(request));
      renderDetail();
      await screen.findByRole("heading", { level: 1, name: "Triage" });
      expect(await screen.findByTestId("triggers-empty")).toHaveTextContent("Off. Triage runs only when someone starts it.");
      expect(screen.queryByRole("button", { name: /^Set up/ })).toBeNull();
      expect(screen.getByRole("button", { name: "Add trigger…" })).toBeEnabled();
    });
  });
});
