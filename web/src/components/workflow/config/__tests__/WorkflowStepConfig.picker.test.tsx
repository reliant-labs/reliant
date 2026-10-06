/**
 * A workflow step's reference was a native <select> of bare names
 * (`structured-agent`, `triage`) with a "Custom path..." escape. The author
 * could not search it or see what a workflow does before picking it. It is now
 * the shared picker: grouped, searchable, each workflow with its title and
 * description, and manual entry for a project:// path or an expression.
 */
import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

const listWorkflows = vi.fn();
vi.mock("../../../../api/client", () => ({ api: { workflows: { list: (projectId: string) => listWorkflows(projectId) } } }));
vi.mock("../../../../store/projectStore", () => ({
  useProjectStore: (selector: (state: unknown) => unknown) => selector({ currentProject: { id: "project-1" } }),
}));
vi.mock("../../useWorkflowInputs", () => ({
  useWorkflowInputs: () => ({
    loadingDef: false,
    inputGroups: [],
    presets: [],
    presetsLoading: false,
    selectedPresets: {},
    getPresetsForGroup: () => [],
    handlePresetSelect: () => undefined,
    handleInputChange: () => undefined,
  }),
}));

import { WorkflowStepConfig } from "../WorkflowStepConfig";
import { getStepInputs, getStepPresets, getStepRef, type WorkflowStep } from "../../../../types/workflow";
import { celString } from "../../../../lib/celAdapter";

listWorkflows.mockResolvedValue({
  workflows: [
    { name: "builtin://agent", title: "Agent", description: "A coding agent with tools.", source: "builtin" },
    { name: "builtin://structured-agent", title: "Structured agent", description: "Answers through a response tool.", source: "builtin" },
    { name: "workflow://triage", title: "", description: "Label new issues.", source: "user" },
    { name: "workflow://editing", title: "Editing", description: "", source: "user" },
  ],
});

function step(ref: string): WorkflowStep {
  return {
    id: "child",
    type: "workflow",
    args: { case: "workflow", value: { ref: celString(ref), args: { topic: "old" }, presets: { "": "fast" } } },
  } as unknown as WorkflowStep;
}

function renderStep(ref = "") {
  const onUpdate = vi.fn();
  render(<WorkflowStepConfig step={step(ref)} onUpdate={onUpdate} currentWorkflowName="workflow://editing" />);
  return { onUpdate, latest: () => onUpdate.mock.calls.at(-1)![0] as WorkflowStep };
}

describe("WorkflowStepConfig reference picker", () => {
  it("lists built-in and your workflows, each with what it does, and not the workflow being edited", async () => {
    const user = userEvent.setup();
    renderStep();
    const reference = await screen.findByLabelText("Reference", { selector: "button" });
    await waitFor(() => expect(reference).toBeEnabled());
    await user.click(reference);

    const builtins = screen.getByRole("group", { name: "Built-in workflows" });
    expect(within(builtins).getAllByRole("option").map((o) => o.textContent)).toEqual([
      "Agentbuiltin://agentA coding agent with tools.",
      "Structured agentbuiltin://structured-agentAnswers through a response tool.",
    ]);
    const yours = screen.getByRole("group", { name: "Your workflows" });
    // No title: named by its ref, without the scheme. The workflow being edited is left out.
    expect(within(yours).getAllByRole("option").map((o) => o.textContent)).toEqual(["triageworkflow://triageLabel new issues."]);
  });

  it("finds a workflow by what it does, and clears the old args and presets on pick", async () => {
    const user = userEvent.setup();
    const { latest } = renderStep("builtin://agent");
    const reference = await screen.findByLabelText("Reference", { selector: "button" });
    await waitFor(() => expect(reference).toHaveTextContent("Agent"));
    await user.click(reference);
    await user.type(screen.getByRole("combobox", { name: "Search workflows" }), "label");
    await user.keyboard("{Enter}");

    expect(getStepRef(latest())).toBe("workflow://triage");
    expect(getStepInputs(latest())).toEqual({});
    expect(getStepPresets(latest())).toEqual({});
  });

  it("opens a ref the list does not know as text, and keeps the args while it is typed", async () => {
    const user = userEvent.setup();
    const { latest } = renderStep("project://flows/review");
    const text = await screen.findByLabelText("Reference", { selector: "input" });
    expect(text).toHaveValue("project://flows/review");
    await user.type(text, "s");
    expect(getStepRef(latest())).toBe("project://flows/reviews");
    expect(getStepInputs(latest())).toEqual({ topic: "old" });
    expect(getStepPresets(latest())).toEqual({ "": "fast" });
  });
});
