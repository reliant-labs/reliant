/**
 * Live validation (research/WORKFLOW_EDITOR_UX_REVIEW.md issue 4): with
 * unsaved edits, the canvas is validated once typing pauses, so problems
 * appear before a save — and an old response never overwrites a newer one.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";

const validateWorkflow = vi.hoisted(() => vi.fn());
vi.mock("../../../api/workflow-grpc", () => ({ workflowGrpc: { validateWorkflow } }));

import type { Workflow } from "../../../types/workflow";
import { LIVE_VALIDATION_DEBOUNCE_MS, useLiveValidation } from "./useLiveValidation";

const wf = (name: string, extra: Partial<Workflow> = {}) => ({ name, ...extra }) as Workflow;
const problem = (message: string) => ({ type: "structure", message, nodeId: "x" });

function setup(initial: { workflow: Workflow; enabled: boolean }) {
  const setStatus = vi.fn();
  const setFindings = vi.fn();
  const hook = renderHook(
    (props: { workflow: Workflow; enabled: boolean }) =>
      useLiveValidation({ projectId: "proj-1", setStatus, setFindings, ...props }),
    { initialProps: initial },
  );
  return { ...hook, setStatus, setFindings };
}

beforeEach(() => {
  vi.useFakeTimers();
  validateWorkflow.mockReset();
});
afterEach(() => vi.useRealTimers());

describe("useLiveValidation", () => {
  it("validates the unsaved canvas once edits pause, and reports its problems", async () => {
    validateWorkflow.mockResolvedValue({ valid: false, errors: [problem("needs a model")] });
    const { rerender, setStatus, setFindings } = setup({ workflow: wf("a"), enabled: true });
    rerender({ workflow: wf("a", { description: "1" }), enabled: true });
    rerender({ workflow: wf("a", { description: "12" }), enabled: true });
    expect(validateWorkflow).not.toHaveBeenCalled();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(LIVE_VALIDATION_DEBOUNCE_MS);
    });
    expect(validateWorkflow).toHaveBeenCalledTimes(1);
    expect(validateWorkflow).toHaveBeenCalledWith("proj-1", wf("a", { description: "12" }));
    expect(setStatus).toHaveBeenCalledWith("validating");
    expect(setFindings).toHaveBeenCalledWith([problem("needs a model")]);
    expect(setStatus).toHaveBeenLastCalledWith("invalid");
  });

  it("does nothing without unsaved edits (the load and the save report findings)", async () => {
    setup({ workflow: wf("a"), enabled: false });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LIVE_VALIDATION_DEBOUNCE_MS * 2);
    });
    expect(validateWorkflow).not.toHaveBeenCalled();
  });

  it("does not revalidate when only the layout moved", async () => {
    validateWorkflow.mockResolvedValue({ valid: true, errors: [] });
    const { rerender } = setup({ workflow: wf("a", { ui: { positions: { x: { x: 1, y: 1 } } } } as Partial<Workflow>), enabled: true });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LIVE_VALIDATION_DEBOUNCE_MS);
    });
    rerender({ workflow: wf("a", { ui: { positions: { x: { x: 9, y: 9 } } } } as Partial<Workflow>), enabled: true });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LIVE_VALIDATION_DEBOUNCE_MS);
    });
    expect(validateWorkflow).toHaveBeenCalledTimes(1);
  });

  it("drops a slow response for an older canvas", async () => {
    let resolveFirst: (value: unknown) => void = () => {};
    validateWorkflow
      .mockReturnValueOnce(new Promise((resolve) => (resolveFirst = resolve)))
      .mockResolvedValueOnce({ valid: true, errors: [] });
    const { rerender, setFindings, setStatus } = setup({ workflow: wf("old"), enabled: true });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LIVE_VALIDATION_DEBOUNCE_MS);
    });
    rerender({ workflow: wf("new"), enabled: true });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LIVE_VALIDATION_DEBOUNCE_MS);
    });
    await act(async () => {
      resolveFirst({ valid: false, errors: [problem("stale")] });
    });
    expect(setFindings).not.toHaveBeenCalledWith([problem("stale")]);
    expect(setStatus).toHaveBeenLastCalledWith("valid");
  });
});
