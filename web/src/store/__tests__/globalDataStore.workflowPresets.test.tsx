// Copyright (c) 2025 Reliant Labs

/**
 * Which project a workflow's presets are read from.
 *
 * Project presets differ per project, so the project is load-bearing:
 *
 *   - useWorkflowPresets(projectId, …) reads the project it is GIVEN. The Run…
 *     form and the automation dialog use it, and they routinely show a
 *     project other than the open one.
 *   - usePresetsForWorkflow(…) — the chat composer's — still follows the
 *     CURRENT project, unchanged by the extraction.
 *   - useWorkflowInputs, shared by the builder step config, follows its
 *     `projectId` argument for presets as it already did for the definition.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";

const listPresetsForWorkflow = vi.fn();

vi.mock("@/api/preset-grpc", () => ({
  presetGrpc: {
    listPresetsForWorkflow: (projectId: string, workflowName: string) =>
      listPresetsForWorkflow(projectId, workflowName),
    getDefaultPresets: vi.fn(async () => ({})),
  },
}));

vi.mock("@/api/workflow-grpc", () => ({
  workflowGrpc: {
    listWorkflows: vi.fn(async () => []),
    getWorkflow: vi.fn(async () => ({ workflow: null })),
  },
}));

vi.mock("@/lib/configReady", () => ({ waitForConfig: async () => undefined }));

vi.mock("@/store/projectStore", () => {
  const snapshot = () => ({ currentProject: { id: "proj-current" } });
  const useProjectStore = Object.assign(
    (selector: (s: ReturnType<typeof snapshot>) => unknown) => selector(snapshot()),
    { getState: snapshot, subscribe: () => () => undefined },
  );
  return { useProjectStore };
});

import { usePresetsForWorkflow, useWorkflowPresets } from "../globalDataStore";
import { useWorkflowInputs } from "@/components/workflow/useWorkflowInputs";

describe("workflow presets follow the right project", () => {
  beforeEach(() => {
    listPresetsForWorkflow.mockReset();
    listPresetsForWorkflow.mockImplementation(async (projectId: string) => [
      { name: `preset-of-${projectId}`, description: "", params: {}, source: "project", tag: "agent" },
    ]);
  });

  it("useWorkflowPresets reads the project it is given, not the current one", async () => {
    const { result } = renderHook(() => useWorkflowPresets("proj-other", "workflow://triage"));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(listPresetsForWorkflow).toHaveBeenCalledWith("proj-other", "workflow://triage");
    expect(listPresetsForWorkflow).not.toHaveBeenCalledWith("proj-current", expect.anything());
    expect(result.current.presets.map((p) => p.name)).toEqual(["preset-of-proj-other"]);
  });

  it("usePresetsForWorkflow (the composer's) still reads the current project", async () => {
    const { result } = renderHook(() => usePresetsForWorkflow("builtin://agent"));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(listPresetsForWorkflow).toHaveBeenCalledWith("proj-current", "builtin://agent");
    expect(result.current.presets.map((p) => p.name)).toEqual(["preset-of-proj-current"]);
  });

  it("useWorkflowInputs lists presets from its projectId argument", async () => {
    const { result } = renderHook(() =>
      useWorkflowInputs({
        projectId: "proj-other",
        workflowRef: "workflow://triage",
        values: {},
        onValuesChange: () => undefined,
      }),
    );
    await waitFor(() => expect(result.current.presetsLoading).toBe(false));
    expect(listPresetsForWorkflow).toHaveBeenCalledWith("proj-other", "workflow://triage");
    expect(listPresetsForWorkflow).not.toHaveBeenCalledWith("proj-current", expect.anything());
  });
});
