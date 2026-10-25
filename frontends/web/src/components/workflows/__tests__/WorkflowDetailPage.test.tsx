// Copyright (c) 2025 Reliant Labs

/**
 * Workflow detail (WORKFLOW_UI.md §2.3), against the real data layer with only
 * the RPC client mocked: its recent runs (ListRuns filtered to the workflow),
 * the automations that run it (ListTriggers), its presets
 * (ListPresetsForWorkflow), its inputs, and the actions — Run now opens
 * RunNowDialog, Edit opens the builder, New automation opens the
 * automation form prefilled with this workflow.
 */

import { beforeEach, describe, expect, it, vi, afterEach } from "vitest";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { serviceHandlers } from "@/test/serviceTransport";
import { queryClient as presetQueryClient } from "@/lib/query-client";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { create } from "@bufbuild/protobuf";

import { RunDisplayState } from "@/gen/reliant/v1/run_pb";
import { CelStringSchema, NodeSchema, SubWorkflowArgsSchema, WorkflowSchema } from "@/gen/reliant/v1/workflow_v2_pb";
import { GetWorkflowResponseSchema } from "@/gen/reliant/v1/workflow_pb";
import { DisableWorkflowResponseSchema, ScheduleSourceSchema, TriggerHealthSchema, TriggerHealthStatus, TriggerSchema, WorkflowTriggerSchema } from "@/gen/reliant/v1/trigger_pb";
import { getWorkflowByName, presetsResponse } from "../../workflow/run/__tests__/runFormFixtures";
import { libraryResponse, protoRun, protoTrigger, renderWorkflowsPage } from "./workflowsTestUtils";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";

const mocks = vi.hoisted(() => ({
  listWorkflows: vi.fn(),
  getWorkflow: vi.fn(),
  listRuns: vi.fn(),
  listTriggers: vi.fn(),
  disableWorkflow: vi.fn(),
  listPresetsForWorkflow: vi.fn(),
  getDefaultPresetsBatch: vi.fn(),
  getDefaultPresets: vi.fn(),
  listWorktrees: vi.fn(),
  listDaemons: vi.fn(),
  listProjectDaemons: vi.fn(),
}));

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    workflow: () => ({ listWorkflows: mocks.listWorkflows, getWorkflow: mocks.getWorkflow }),
    trigger: () => ({ listTriggers: mocks.listTriggers, disableWorkflow: mocks.disableWorkflow }),
    worktree: () => ({ listWorktrees: mocks.listWorktrees }),
    daemonRegistry: () => ({ listDaemons: mocks.listDaemons }),
    project: () => ({ listProjectDaemons: mocks.listProjectDaemons }),
  },
}));

// PresetService and RunService are reached through connectClient (the
// generated hooks), so they are answered at the transport by the same fakes. Integration events are
// named from the catalog's trigger types; none here.
beforeEach(() => {
  setTransport(mockTransport({
      ...serviceHandlers("PresetService", {
      listPresetsForWorkflow: mocks.listPresetsForWorkflow,
      getDefaultPresetsBatch: mocks.getDefaultPresetsBatch,
      getDefaultPresets: mocks.getDefaultPresets,
    }),
      ...serviceHandlers("RunService", { listRuns: mocks.listRuns }),
      "CatalogService.SearchCatalog": { response: {} },
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
  mocks.listPresetsForWorkflow.mockResolvedValue(presetsResponse());
  mocks.getDefaultPresetsBatch.mockResolvedValue({ defaults: {} });
  mocks.getDefaultPresets.mockResolvedValue({ defaults: {} });
  mocks.listWorktrees.mockResolvedValue({ worktrees: [], total: 0 });
  mocks.listDaemons.mockResolvedValue({ daemons: [] });
  mocks.listProjectDaemons.mockResolvedValue({ projectDaemons: [] });
});

function renderDetail(ref = "triage") {
  return renderWorkflowsPage(
    <WorkflowDetailPage />,
    `/workflows/library/${encodeURIComponent(ref)}`,
    "/workflows/library/$workflowRef",
  );
}

describe("WorkflowDetailPage", () => {
  it("shows the workflow's recent runs, filtered to it", async () => {
    renderDetail();
    expect(await screen.findByRole("heading", { level: 1, name: "Triage" })).toBeInTheDocument();
    const runs = await screen.findByTestId("workflow-detail-runs");
    expect(within(runs).getAllByRole("link", { name: /Run of triage/ })).toHaveLength(2);
    expect(mocks.listRuns).toHaveBeenCalledWith(
      expect.objectContaining({ projectId: "proj-1", workflow: ["triage"] }),
    );
    const viewAll = screen.getByRole("link", { name: "View all" });
    expect(viewAll.getAttribute("href")).toMatch(/^\/workflows\/runs\?/);
    expect(decodeURIComponent(viewAll.getAttribute("href") ?? "")).toContain('workflow=["triage"]');
  });

  it("lists only the automations that run this workflow", async () => {
    renderDetail();
    const automations = await screen.findByTestId("workflow-detail-automations");
    expect(within(automations).getByRole("link", { name: "Morning triage" })).toHaveAttribute(
      "href",
      "/workflows/automations/t1",
    );
    expect(within(automations).queryByText("Other")).toBeNull();
  });

  it("says a workflow that declares a schedule is not turned on, and offers Set up instead of 'Nothing runs this workflow'", async () => {
    // ci-triage declares `triggers: - schedule: cron [0 6 * * 1-5]` and has
    // no activation, so nothing runs it YET.
    mocks.listTriggers.mockResolvedValue({ triggers: [], lastUserUpdateSequence: "0" });
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

    const empty = await screen.findByTestId("turned-on-empty");
    expect(empty).toHaveTextContent("Not turned on. Set up Triage to run it by itself:");
    await userEvent.click(within(empty).getByRole("button", { name: "Set up Triage" }));
    expect(await screen.findByRole("form", { name: "Set up Triage" })).toBeInTheDocument();
  });

  it("a workflow whose Chat trigger is off can still be run by hand: Run now is how it is run", async () => {
    const library = libraryResponse();
    library.workflows[1] = { ...library.workflows[1]!, automationOnly: true } as (typeof library.workflows)[number];
    mocks.listWorkflows.mockResolvedValue(library);
    renderDetail();
    await screen.findByRole("heading", { level: 1, name: "Triage" });
    expect(screen.queryByText(/Its Chat trigger is off/)).toBeNull();
    expect(screen.getByRole("button", { name: "Run now" })).toBeEnabled();
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

  it("Run now opens the run dialog for this workflow", async () => {
    renderDetail();
    await screen.findByRole("heading", { level: 1, name: "Triage" });
    await userEvent.click(screen.getByRole("button", { name: "Run now" }));
    expect(await screen.findByRole("dialog", { name: "Run Triage" })).toBeInTheDocument();
  });

  it("Edit opens the builder", async () => {
    const { router } = renderDetail();
    await screen.findByRole("heading", { level: 1, name: "Triage" });
    await userEvent.click(screen.getByRole("button", { name: "Edit" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/workflow/triage"));
  });

  it("Add a custom automation opens the automation form with this workflow; the header has only Run now and Edit", async () => {
    renderDetail();
    await screen.findByRole("heading", { level: 1, name: "Triage" });
    expect(screen.queryByRole("button", { name: "New automation" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Enable" })).toBeNull();
    await userEvent.click(await screen.findByRole("button", { name: "Add a custom automation" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByLabelText("Name")).toHaveValue("Triage");
    await waitFor(() => expect(within(dialog).getByLabelText("Workflow")).toHaveValue("triage"));
  });

  it("a draft cannot run, and says why in text", async () => {
    renderDetail("my-draft");
    await screen.findByRole("heading", { level: 1, name: "My Draft" });
    expect(screen.getByRole("button", { name: "Run now" })).toBeDisabled();
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
    expect(screen.getByRole("link", { name: "Back to library" })).toHaveAttribute("href", "/workflows/library");
  });

  describe("turned on", () => {
    const declaresTwo = (request: { name: string }) =>
      request.name === "triage"
        ? create(GetWorkflowResponseSchema, {
            source: "project",
            workflow: create(WorkflowSchema, {
              name: "triage",
              inputs: {},
              triggers: [
                create(WorkflowTriggerSchema, { name: "morning", source: { case: "schedule", value: create(ScheduleSourceSchema, { cron: ["0 6 * * *"], timezone: "UTC" }) } }),
                create(WorkflowTriggerSchema, { name: "evening", source: { case: "schedule", value: create(ScheduleSourceSchema, { cron: ["0 18 * * *"], timezone: "UTC" }) } }),
              ],
            }),
          })
        : getWorkflowByName(request);

    const activation = (id: string, declared: string, enabled: boolean) => {
      const t = protoTrigger(id, "triage", `Triage · ${declared}`);
      t.workflowTrigger = declared;
      t.enabled = enabled;
      return t;
    };

    it("lists one row per project it is on in, with its state, a settings summary and Settings", async () => {
      const user = userEvent.setup();
      mocks.listTriggers.mockResolvedValue({ triggers: [activation("a1", "morning", true), activation("a2", "evening", true)] });
      mocks.getWorkflow.mockImplementation(async (request: { name: string }) => declaresTwo(request));
      renderDetail();

      const row = await screen.findByTestId("turned-on-row-proj-1");
      expect(row).toHaveAttribute("id", "automation-proj-1");
      expect(within(row).getByText("Reliant")).toBeInTheDocument();
      expect(within(row).getByRole("switch", { name: "On in Reliant" })).toHaveAttribute("aria-checked", "true");
      expect(within(row).getByRole("button", { name: "Settings for Reliant" })).toBeInTheDocument();
      // One row for the two triggers; they are inside it.
      expect(screen.getAllByTestId(/^turned-on-row-/)).toHaveLength(1);
      await user.click(within(row).getByRole("button", { expanded: false }));
      const inner = within(row).getByRole("list", { name: "Triggers in Reliant" });
      expect(within(inner).getAllByRole("link", { name: /^History of / }).map((a) => a.getAttribute("href"))).toEqual([
        "/workflows/automations/a2",
        "/workflows/automations/a1",
      ]);
      expect(screen.getByRole("button", { name: "Turn on in another project" })).toBeInTheDocument();
    });

    it("pausing a row pauses the workflow in that project in one call", async () => {
      const user = userEvent.setup();
      mocks.listTriggers.mockResolvedValue({ triggers: [activation("a1", "morning", true), activation("a2", "evening", true)] });
      mocks.getWorkflow.mockImplementation(async (request: { name: string }) => declaresTwo(request));
      mocks.disableWorkflow.mockResolvedValue(create(DisableWorkflowResponseSchema, { paused: [create(TriggerSchema, { id: "a1" }), create(TriggerSchema, { id: "a2" })] }));
      renderDetail();

      await user.click(await screen.findByRole("switch", { name: "On in Reliant" }));
      await waitFor(() => expect(mocks.disableWorkflow).toHaveBeenCalledTimes(1));
      expect(mocks.disableWorkflow.mock.calls[0]![0]).toMatchObject({ workflow: "triage", projectId: "proj-1" });
    });

    it("calls out an automation that needs you under the header, with its one call to action", async () => {
      const broken = activation("a1", "morning", true);
      broken.health = create(TriggerHealthSchema, {
        status: TriggerHealthStatus.BROKEN,
        lastFailureDetail: "the workflow no longer declares a trigger named morning",
      });
      mocks.listTriggers.mockResolvedValue({ triggers: [broken] });
      mocks.getWorkflow.mockImplementation(async (request: { name: string }) => declaresTwo(request));
      renderDetail();

      const notice = await screen.findByTestId("automation-state-notice");
      expect(notice).toHaveAttribute("data-state", "broken");
      expect(notice).toHaveTextContent("Triage in Reliant can't run: its workflow no longer declares this trigger.");
      expect(within(notice).getByRole("button", { name: "Open workflow" })).toBeInTheDocument();
      // The row carries the same action instead of Settings.
      const row = screen.getByTestId("turned-on-row-proj-1");
      expect(within(row).getByRole("button", { name: "Open workflow for Reliant" })).toBeInTheDocument();
    });

    it("opens and scrolls to the automation the address names", async () => {
      window.location.hash = "#automation-proj-1";
      mocks.listTriggers.mockResolvedValue({ triggers: [activation("a1", "morning", true)] });
      mocks.getWorkflow.mockImplementation(async (request: { name: string }) => declaresTwo(request));
      renderDetail();

      const row = await screen.findByTestId("turned-on-row-proj-1");
      await waitFor(() => expect(within(row).getByRole("list", { name: "Triggers in Reliant" })).toBeInTheDocument());
      window.location.hash = "";
    });

    it("offers no Set up on a workflow that declares nothing", async () => {
      mocks.listTriggers.mockResolvedValue({ triggers: [] });
      renderDetail();
      await screen.findByRole("heading", { level: 1, name: "Triage" });
      expect(await screen.findByText("This workflow declares no triggers, so it runs only when someone starts it.")).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: /^Set up/ })).toBeNull();
    });
  });
});
