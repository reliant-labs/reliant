/**
 * The builder's assistant panel opens by itself only for a new, empty draft.
 */
import { describe, expect, it, vi } from "vitest";
import { renderHook } from "@testing-library/react";

import type { Workflow } from "../../../types/workflow";
import { useAssistantPanelDefault, shouldOpenAssistantPanel } from "../hooks/useAssistantPanelDefault";
import { workflowsJourney } from "../../Onboarding/journeys/workflows";

const empty = { name: "new-one" } as Workflow;
const emptyList = { name: "new-one", nodes: [] } as unknown as Workflow;
const withSteps = { name: "fix-my-pr", nodes: [{ id: "review" }] } as unknown as Workflow;

describe("shouldOpenAssistantPanel", () => {
  it("opens for a new draft with no steps", () => {
    expect(shouldOpenAssistantPanel({ workflow: empty, isBuiltin: false, tourParam: null })).toBe(true);
    expect(shouldOpenAssistantPanel({ workflow: emptyList, isBuiltin: false, tourParam: null })).toBe(true);
  });

  it("stays closed for a workflow that has steps, a built-in, or nothing loaded", () => {
    expect(shouldOpenAssistantPanel({ workflow: withSteps, isBuiltin: false, tourParam: null })).toBe(false);
    expect(shouldOpenAssistantPanel({ workflow: emptyList, isBuiltin: true, tourParam: null })).toBe(false);
    expect(shouldOpenAssistantPanel({ workflow: undefined, isBuiltin: false, tourParam: null })).toBe(false);
  });

  it("stays closed on every workflows-tour step, even for an empty draft", () => {
    const ids = workflowsJourney.steps.map((s) => s.id);
    expect(ids.length).toBeGreaterThan(0);
    for (const id of ids) {
      expect(shouldOpenAssistantPanel({ workflow: emptyList, isBuiltin: false, tourParam: `workflows:${id}` }), id).toBe(false);
      expect(shouldOpenAssistantPanel({ workflow: emptyList, isBuiltin: false, tourParam: id }), id).toBe(false);
    }
  });

  it("is not suppressed by the other journey's tour param", () => {
    expect(shouldOpenAssistantPanel({ workflow: emptyList, isBuiltin: false, tourParam: "forge:f-1" })).toBe(true);
  });
});

describe("useAssistantPanelDefault", () => {
  const run = (workflow: Workflow | undefined, key = "d1", isBuiltin = false) => {
    const openPanel = vi.fn();
    const hook = renderHook((p: { workflow: Workflow | undefined; key: string }) =>
      useAssistantPanelDefault({ workflow: p.workflow, workflowKey: p.key, isBuiltin, openPanel }),
      { initialProps: { workflow, key } },
    );
    return { openPanel, ...hook };
  };

  it("opens once for a new, empty draft", () => {
    const { openPanel, rerender } = run(emptyList);
    expect(openPanel).toHaveBeenCalledTimes(1);
    rerender({ workflow: { ...emptyList }, key: "d1" });
    expect(openPanel).toHaveBeenCalledTimes(1);
  });

  it("does not reopen after the user adds a step and closes it", () => {
    const { openPanel, rerender } = run(emptyList);
    rerender({ workflow: withSteps, key: "d1" });
    expect(openPanel).toHaveBeenCalledTimes(1);
  });

  it("never opens for an existing workflow", () => {
    const { openPanel } = run(withSteps);
    expect(openPanel).not.toHaveBeenCalled();
  });

  it("waits for the workflow to load, then decides", () => {
    const { openPanel, rerender } = run(undefined);
    expect(openPanel).not.toHaveBeenCalled();
    rerender({ workflow: withSteps, key: "d1" });
    expect(openPanel).not.toHaveBeenCalled();
  });

  it("decides again when a different workflow is opened", () => {
    const { openPanel, rerender } = run(withSteps, "d1");
    rerender({ workflow: emptyList, key: "d2" });
    expect(openPanel).toHaveBeenCalledTimes(1);
  });
});
