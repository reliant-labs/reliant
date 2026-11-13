/**
 * Adopting an automation whose workflow resolves in the adopter's project:
 * its inputs are the workflow's own typed fields (RunWorkflowForm, as
 * creating an automation renders them), so a number is sent as a number, a
 * yes/no as a bool and a group as a structured value, each encoded exactly
 * as a create encodes it. A required input left unset holds Adopt back. When
 * the workflow does not resolve there, the dialog falls back to typing each
 * input by hand.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create, toJson } from "@bufbuild/protobuf";
import { ValueSchema, type Value } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { BooleanInputConfigSchema, InputBaseSchema, InputSchema } from "@/gen/reliant/v1/workflow_v2_pb";
import type { Trigger } from "@/gen/reliant/v1/trigger_pb";

const ADA_PROJECT = { id: "proj-ada", name: "Ada's app", org_id: "org-a" };
const mocks = vi.hoisted(() => ({ projects: [] as { id: string; name: string; org_id: string }[] }));

vi.mock("@/hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/project-queries")>()),
  useProjects: () => ({ projects: mocks.projects, isLoading: false, error: null, refetch: () => undefined }),
  useProjectDaemonInstalls: () => ({ data: [], isLoading: false }),
}));
vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({ daemons: [{ daemonId: "d-ada", name: "ada-box", status: DaemonStatus.ACTIVE }], loading: false, answered: true, refresh: vi.fn() }),
}));
vi.mock("@/store/projectStore", () => {
  const state = { currentProject: { id: "proj-ada", name: "Ada's app" }, projects: [], isLoading: false };
  const useProjectStore = Object.assign((selector?: (s: typeof state) => unknown) => (selector ? selector(state) : state), { getState: () => state });
  return { useProjectStore };
});

import { setTransport } from "@/lib/connect";
import { queryClient as presetQueryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { serviceHandlers } from "@/test/serviceTransport";
import { renderWithQuery } from "@/test/renderWithQuery";

import { WORKFLOW_LIST, presetsResponse, triageWorkflowResponse, worktreesResponse } from "../../../../workflow/run/__tests__/runFormFixtures";
import { AdoptAutomationDialog } from "../AdoptAutomationDialog";

/** triage, plus a yes/no input. */
function typedWorkflow() {
  const resp = triageWorkflowResponse();
  resp.workflow!.inputs["dry_run"] = create(InputSchema, {
    type: "boolean",
    config: { case: "booleanInput", value: create(BooleanInputConfigSchema, { base: create(InputBaseSchema, { description: "Only report" }), default: false }) },
  });
  return resp;
}

const getWorkflow = vi.fn();
const getDefaultPresetsBatch = vi.fn();
const adopt = vi.fn((_req: unknown) => ({ trigger: { id: "t-1", name: "Triage" }, notices: [] }));

const trigger = { id: "t-1", name: "Triage", workflow: "triage", source: { case: "schedule", value: {} } } as unknown as Trigger;

beforeEach(() => {
  adopt.mockClear();
  getWorkflow.mockReset();
  mocks.projects = [ADA_PROJECT];
  getDefaultPresetsBatch.mockReset();
  getDefaultPresetsBatch.mockResolvedValue({ presetsByWorkflow: {} });
  setTransport(
    mockTransport({
      "AutomationTakeoverService.AdoptTrigger": adopt,
      ...serviceHandlers("WorkflowService", { getWorkflow, listWorkflows: async () => WORKFLOW_LIST }),
      ...serviceHandlers("PresetService", {
        listPresetsForWorkflow: async () => presetsResponse(),
        getDefaultPresetsBatch,
      }),
      ...serviceHandlers("WorktreeService", { listWorktrees: async (r: { projectId: string }) => worktreesResponse(r.projectId) }),
    }),
  );
});
afterEach(() => {
  setTransport(null);
  presetQueryClient.clear();
});

const sent = () => (adopt.mock.calls[0][0] as { rebinding: { params: Record<string, Value> } }).rebinding.params;
const asJson = (params: Record<string, Value>) => Object.fromEntries(Object.entries(params).map(([k, v]) => [k, toJson(ValueSchema, v)]));

describe("AdoptAutomationDialog typed inputs", () => {
  it("renders the workflow's own fields and sends each value as its declared type", async () => {
    getWorkflow.mockResolvedValue(typedWorkflow());
    renderWithQuery(<AdoptAutomationDialog orgId="org-a" target={{ kind: "adopt", trigger, paramNames: ["label"] }} onClose={vi.fn()} />);

    expect(await screen.findByTestId("adopt-typed-inputs")).toBeInTheDocument();
    expect(screen.queryByTestId("adopt-params")).toBeNull();
    expect(getWorkflow).toHaveBeenCalledWith(expect.objectContaining({ projectId: "proj-ada", name: "triage" }));

    // The required Label is unset: Adopt waits for it.
    await waitFor(() => expect(screen.getByTestId("adopt-submit")).toBeDisabled());

    fireEvent.change(await screen.findByLabelText("Label"), { target: { value: "bugs" } });
    fireEvent.change(screen.getByLabelText("Depth"), { target: { value: "5" } });
    await userEvent.selectOptions(screen.getByLabelText("Strictness"), "high");
    await userEvent.click(screen.getByRole("switch", { name: "Dry run" }));

    await waitFor(() => expect(screen.getByTestId("adopt-submit")).toBeEnabled());
    await userEvent.click(screen.getByTestId("adopt-submit"));
    await waitFor(() => expect(adopt).toHaveBeenCalledTimes(1));
    const params = sent();
    expect(params.depth.kind.case).toBe("numberValue");
    expect(params.dry_run.kind.case).toBe("boolValue");
    expect(params.review.kind.case).toBe("structValue");
    expect(asJson(params)).toEqual(expect.objectContaining({ label: "bugs", depth: 5, dry_run: true, review: { strictness: "high" } }));
  });

  it("starts the inputs again when the adopter switches project: nothing chosen against the first is sent to the second", async () => {
    mocks.projects = [ADA_PROJECT, { id: "proj-bob", name: "Bob's app", org_id: "org-a" }];
    getWorkflow.mockImplementation(async () => typedWorkflow());
    // Ada's app defaults triage to its "careful" preset; Bob's app has none.
    getDefaultPresetsBatch.mockImplementation(async (r: { projectId: string }) =>
      r.projectId === "proj-ada" ? { presetsByWorkflow: { triage: { presets: { "": "careful" } } } } : { presetsByWorkflow: {} },
    );
    renderWithQuery(<AdoptAutomationDialog orgId="org-a" target={{ kind: "adopt", trigger, paramNames: ["label"] }} onClose={vi.fn()} />);

    const label = await screen.findByLabelText("Label");
    await waitFor(() => expect(getDefaultPresetsBatch).toHaveBeenCalledWith(expect.objectContaining({ projectId: "proj-ada" })));
    fireEvent.change(label, { target: { value: "from-ada" } });
    fireEvent.change(screen.getByLabelText("Depth"), { target: { value: "7" } });

    await userEvent.selectOptions(screen.getByTestId("adopt-project"), "proj-bob");
    await waitFor(() => expect(getWorkflow).toHaveBeenCalledWith(expect.objectContaining({ projectId: "proj-bob", name: "triage" })));
    await waitFor(() => expect(screen.getByLabelText("Label")).toHaveValue(""));
    // Bob's required Label is unset again: Adopt waits for it.
    await waitFor(() => expect(screen.getByTestId("adopt-submit")).toBeDisabled());

    fireEvent.change(screen.getByLabelText("Label"), { target: { value: "from-bob" } });
    await waitFor(() => expect(screen.getByTestId("adopt-submit")).toBeEnabled());
    await userEvent.click(screen.getByTestId("adopt-submit"));
    await waitFor(() => expect(adopt).toHaveBeenCalledTimes(1));

    const rebinding = (adopt.mock.calls[0][0] as { rebinding: { projectId: string; params: Record<string, Value>; presets: Record<string, string> } }).rebinding;
    expect(rebinding.projectId).toBe("proj-bob");
    expect(rebinding.presets).toEqual({});
    const params = asJson(rebinding.params);
    expect(params.label).toBe("from-bob");
    expect(params).not.toHaveProperty("depth");
    expect(JSON.stringify(params)).not.toContain("from-ada");
  });

  it("falls back to typing each input when the workflow does not resolve in the adopter's project", async () => {
    getWorkflow.mockRejectedValue(new ConnectError("not found", Code.NotFound));
    renderWithQuery(<AdoptAutomationDialog orgId="org-a" target={{ kind: "adopt", trigger, paramNames: ["label"] }} onClose={vi.fn()} />);

    expect(await screen.findByTestId("adopt-params")).toHaveTextContent("could not be read in this project");
    expect(screen.queryByTestId("adopt-typed-inputs")).toBeNull();
    await userEvent.type(screen.getByTestId("adopt-param-label"), "bugs");
    await userEvent.click(screen.getByTestId("adopt-submit"));
    await waitFor(() => expect(adopt).toHaveBeenCalledTimes(1));
    expect(asJson(sent())).toEqual({ label: "bugs" });
  });
});
