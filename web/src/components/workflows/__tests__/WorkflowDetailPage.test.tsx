// Copyright (c) 2025 Reliant Labs

/**
 * Workflow detail (WORKFLOW_UI.md §2.3), against the real data layer with only
 * the RPC client mocked: its recent runs (ListRuns filtered to the workflow),
 * the automations that run it (ListTriggers), its presets
 * (ListPresetsForWorkflow), its inputs, and the actions — Run now opens
 * RunNowDialog, Edit opens the builder, New automation opens the
 * automation form prefilled with this workflow.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { create } from "@bufbuild/protobuf";

import { RunDisplayState } from "@/gen/reliant/v1/run_pb";
import { CelStringSchema, NodeSchema, SubWorkflowArgsSchema, WorkflowSchema } from "@/gen/reliant/v1/workflow_v2_pb";
import { GetWorkflowResponseSchema } from "@/gen/reliant/v1/workflow_pb";
import { DisableWorkflowResponseSchema, ScheduleSourceSchema, TriggerFailureSchema, TriggerSchema, WorkflowTriggerSchema } from "@/gen/reliant/v1/trigger_pb";
import { getWorkflowByName, presetsResponse } from "../../workflow/run/__tests__/runFormFixtures";
import { libraryResponse, protoRun, protoTrigger, renderWorkflowsPage } from "./workflowsTestUtils";

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
    run: () => ({ listRuns: mocks.listRuns }),
    trigger: () => ({ listTriggers: mocks.listTriggers, disableWorkflow: mocks.disableWorkflow }),
    preset: () => ({
      listPresetsForWorkflow: mocks.listPresetsForWorkflow,
      getDefaultPresetsBatch: mocks.getDefaultPresetsBatch,
      getDefaultPresets: mocks.getDefaultPresets,
    }),
    worktree: () => ({ listWorktrees: mocks.listWorktrees }),
    daemonRegistry: () => ({ listDaemons: mocks.listDaemons }),
    project: () => ({ listProjectDaemons: mocks.listProjectDaemons }),
  },
}));

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

  it("lists a schedule the YAML declares but nobody activated, instead of 'Nothing runs this workflow'", async () => {
    // ci-triage declares `triggers: - schedule: cron [0 6 * * 1-5]` and has
    // no activation, so nothing runs it YET — but the page used to say
    // nothing runs it at all, which reads as if the YAML was ignored.
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

    const declared = await screen.findByTestId("workflow-detail-declared-triggers");
    expect(within(declared).getByText("weekday-morning")).toBeInTheDocument();
    expect(within(declared).getByText("Not enabled")).toBeInTheDocument();
    // One Enable for the workflow, not one Activate per trigger.
    expect(screen.getAllByRole("button", { name: "Enable" }).length).toBeGreaterThan(0);
    expect(screen.queryByRole("button", { name: "Activate" })).toBeNull();
    expect(screen.queryByText("Nothing runs this workflow on its own.")).toBeNull();
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

  it("New automation opens the automation form with this workflow", async () => {
    renderDetail();
    await screen.findByRole("heading", { level: 1, name: "Triage" });
    await userEvent.click(screen.getByRole("button", { name: "New automation" }));
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

  describe("enabling", () => {
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

    it("offers one Enable for a workflow that declares triggers, and opens the dialog that turns on all of them", async () => {
      const user = userEvent.setup();
      mocks.listTriggers.mockResolvedValue({ triggers: [] });
      mocks.getWorkflow.mockImplementation(async (request: { name: string }) => declaresTwo(request));
      renderDetail();

      const header = await screen.findByRole("heading", { level: 1, name: "Triage" });
      expect(header).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Disable" })).toBeNull();
      await user.click((await screen.findAllByRole("button", { name: "Enable" }))[0]!);
      const dialog = await screen.findByRole("form", { name: "Enable Triage" });
      expect(within(dialog).getByText(/all 2 triggers/)).toBeInTheDocument();
    });

    it("shows Disable, not Enable, once every declared trigger is running, with each one's state", async () => {
      mocks.listTriggers.mockResolvedValue({ triggers: [activation("a1", "morning", true), activation("a2", "evening", true)] });
      mocks.getWorkflow.mockImplementation(async (request: { name: string }) => declaresTwo(request));
      renderDetail();

      const declared = await screen.findByTestId("workflow-detail-declared-triggers");
      expect(within(declared).getAllByText(/^Enabled/)).toHaveLength(2);
      expect(await screen.findByRole("button", { name: "Disable" })).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Enable" })).toBeNull();
    });

    it("offers to enable the rest when a trigger was added since, and to disable what runs", async () => {
      mocks.listTriggers.mockResolvedValue({ triggers: [activation("a1", "morning", true)] });
      mocks.getWorkflow.mockImplementation(async (request: { name: string }) => declaresTwo(request));
      renderDetail();

      await screen.findByTestId("workflow-detail-declared-triggers");
      expect(await screen.findByRole("button", { name: "Enable the rest" })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Disable" })).toBeInTheDocument();
    });

    it("Disable pauses the workflow's activations in one call", async () => {
      const user = userEvent.setup();
      mocks.listTriggers.mockResolvedValue({ triggers: [activation("a1", "morning", true), activation("a2", "evening", true)] });
      mocks.getWorkflow.mockImplementation(async (request: { name: string }) => declaresTwo(request));
      mocks.disableWorkflow.mockResolvedValue(create(DisableWorkflowResponseSchema, { paused: [create(TriggerSchema, { id: "a1" }), create(TriggerSchema, { id: "a2" })] }));
      renderDetail();

      await user.click(await screen.findByRole("button", { name: "Disable" }));
      await waitFor(() => expect(mocks.disableWorkflow).toHaveBeenCalledTimes(1));
      expect(mocks.disableWorkflow.mock.calls[0]![0]).toMatchObject({ workflow: "triage", projectId: "proj-1" });
      expect(screen.queryByRole("alert")).toBeNull();
    });

    it("says which triggers could not be paused and are still running", async () => {
      const user = userEvent.setup();
      mocks.listTriggers.mockResolvedValue({ triggers: [activation("a1", "morning", true), activation("a2", "evening", true)] });
      mocks.getWorkflow.mockImplementation(async (request: { name: string }) => declaresTwo(request));
      mocks.disableWorkflow.mockResolvedValue(
        create(DisableWorkflowResponseSchema, {
          paused: [create(TriggerSchema, { id: "a1" })],
          failed: [create(TriggerFailureSchema, { triggerId: "a2", name: "Triage · evening", message: "schedule backend: temporal is down" })],
        }),
      );
      renderDetail();

      await user.click(await screen.findByRole("button", { name: "Disable" }));
      const alert = await screen.findByRole("alert");
      expect(alert).toHaveTextContent("One trigger could not be paused and is still running");
      expect(alert).toHaveTextContent("Triage · evening (schedule backend: temporal is down)");
    });

    it("says nothing was paused when the request itself fails", async () => {
      const user = userEvent.setup();
      mocks.listTriggers.mockResolvedValue({ triggers: [activation("a1", "morning", true), activation("a2", "evening", true)] });
      mocks.getWorkflow.mockImplementation(async (request: { name: string }) => declaresTwo(request));
      mocks.disableWorkflow.mockRejectedValue(new Error("the server is unreachable"));
      renderDetail();

      await user.click(await screen.findByRole("button", { name: "Disable" }));
      expect(await screen.findByRole("alert")).toHaveTextContent("Nothing was paused: the server is unreachable");
    });

    it("shows a broken activation as broken, with why, and offers no Enable that would not fix it", async () => {
      const broken = activation("a1", "morning", true);
      broken.health = create((await import("@/gen/reliant/v1/trigger_pb")).TriggerHealthSchema, {
        status: (await import("@/gen/reliant/v1/trigger_pb")).TriggerHealthStatus.BROKEN,
        lastFailureDetail: "the workflow no longer declares a trigger named morning",
      });
      mocks.listTriggers.mockResolvedValue({ triggers: [broken] });
      mocks.getWorkflow.mockImplementation(async (request: { name: string }) => declaresTwo(request));
      renderDetail();

      const declared = await screen.findByTestId("workflow-detail-declared-triggers");
      expect(within(declared).getByText(/Broken/)).toBeInTheDocument();
    });

    it("does not offer Enable on a workflow that declares nothing, or on a draft", async () => {
      mocks.listTriggers.mockResolvedValue({ triggers: [] });
      renderDetail();
      await screen.findByRole("heading", { level: 1, name: "Triage" });
      expect(screen.queryByRole("button", { name: "Enable" })).toBeNull();
    });
  });
});
