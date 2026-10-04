// Copyright (c) 2025 Reliant Labs

/**
 * Workflow detail (WORKFLOW_UI.md §2.3), against the real data layer with only
 * the RPC client mocked: its recent runs (ListRuns filtered to the workflow),
 * the automations that run it (ListTriggers), its presets
 * (ListPresetsForWorkflow), its inputs, and the actions — Run… opens
 * RunWorkflowDialog, Edit opens the builder, New automation opens the
 * automation form prefilled with this workflow.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { RunDisplayState } from "@/gen/reliant/v1/run_pb";
import { getWorkflowByName, presetsResponse } from "../../workflow/run/__tests__/runFormFixtures";
import { libraryResponse, protoRun, protoTrigger, renderWorkflowsPage } from "./workflowsTestUtils";

const mocks = vi.hoisted(() => ({
  listWorkflows: vi.fn(),
  getWorkflow: vi.fn(),
  listRuns: vi.fn(),
  listTriggers: vi.fn(),
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
    trigger: () => ({ listTriggers: mocks.listTriggers }),
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

  it("Run… opens RunWorkflowDialog for this workflow", async () => {
    renderDetail();
    await screen.findByRole("heading", { level: 1, name: "Triage" });
    await userEvent.click(screen.getByRole("button", { name: "Run…" }));
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
    expect(screen.getByRole("button", { name: "Run…" })).toBeDisabled();
    expect(screen.getByText("Drafts cannot run until they are marked complete.")).toBeInTheDocument();
  });

  it("a workflow that no longer exists says so and links back", async () => {
    renderDetail("gone");
    expect(await screen.findByText("This workflow no longer exists.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to library" })).toHaveAttribute("href", "/workflows/library");
  });
});
