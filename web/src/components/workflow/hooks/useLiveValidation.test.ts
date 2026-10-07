/**
 * Live validation (research/WORKFLOW_EDITOR_UX_REVIEW.md issue 4): with
 * unsaved edits, the canvas is validated once typing pauses, so problems
 * appear before a save — and an old response never overwrites a newer one.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";

const validateWorkflow = vi.hoisted(() => vi.fn());
vi.mock("../../../api/workflow-grpc", () => ({ workflowGrpc: { validateWorkflow } }));

import { fromJson } from "@bufbuild/protobuf";

import { WorkflowSchema } from "../../../gen/reliant/v1/workflow_v2_pb";
import { createInput, setInputDefault } from "../../../lib/inputHelpers";
import type { Param, Workflow } from "../../../types/workflow";
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

/**
 * int64 fields are bigints in protobuf-es — an integer input's default, min
 * and max, and a CelInt literal such as call_llm's max_tokens — and
 * JSON.stringify throws on a bigint. The canvas key used to be a
 * JSON.stringify of the workflow, so the debounced check threw "Do not know
 * how to serialize a BigInt" inside its timer, nothing caught it, and the
 * problem count and step markers stayed stale until Save.
 */
describe("useLiveValidation with int64 fields", () => {
  /** As GetWorkflow hands it to the builder: int64 decoded to bigint. */
  const loaded = (inputs: Record<string, unknown>, nodes: unknown[] = []) =>
    fromJson(WorkflowSchema, { name: "retry", inputs, nodes } as never) as Workflow;
  const maxAttempts = (fields: Record<string, string>) => ({ max_attempts: { type: "integer", integerInput: fields } });

  async function validateOnce(workflow: Workflow) {
    validateWorkflow.mockResolvedValue({ valid: false, errors: [problem("max_attempts is unused")] });
    const run = setup({ workflow: wf("seed"), enabled: true });
    run.rerender({ workflow, enabled: true });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LIVE_VALIDATION_DEBOUNCE_MS);
    });
    return run;
  }

  it.each([
    ["an integer input with a default", maxAttempts({ default: "3" })],
    ["an integer input with a min and a max", maxAttempts({ min: "1", max: "10" })],
    ["an integer input with a default, min and max", maxAttempts({ default: "3", min: "1", max: "10" })],
  ])("validates a loaded workflow with %s and reports its problems", async (_label, inputs) => {
    const workflow = loaded(inputs);
    // The fixture is what the builder really holds: the int64s are bigints.
    const config = workflow.inputs!.max_attempts.config.value as Record<string, unknown>;
    expect(Object.values(config).some((value) => typeof value === "bigint")).toBe(true);

    const { setStatus, setFindings } = await validateOnce(workflow);
    expect(validateWorkflow).toHaveBeenCalledWith("proj-1", workflow);
    expect(setFindings).toHaveBeenCalledWith([problem("max_attempts is unused")]);
    expect(setStatus).toHaveBeenLastCalledWith("invalid");
  });

  it("validates a default typed into the builder's Inputs editor", async () => {
    const input = setInputDefault(createInput("integer", { ui: "toolbar" }), "3");
    const workflow = wf("retry", { inputs: { max_attempts: input as Param } });
    const { setStatus, setFindings } = await validateOnce(workflow);
    expect(validateWorkflow).toHaveBeenCalledWith("proj-1", workflow);
    expect(setFindings).toHaveBeenCalledWith([problem("max_attempts is unused")]);
    expect(setStatus).toHaveBeenLastCalledWith("invalid");
  });

  it("validates a step with a CelInt literal (call_llm max_tokens)", async () => {
    const workflow = loaded({}, [{ id: "ask", type: "call_llm", callLlm: { maxTokens: { literal: "4096" } } }]);
    const { setStatus } = await validateOnce(workflow);
    expect(validateWorkflow).toHaveBeenCalledWith("proj-1", workflow);
    expect(setStatus).toHaveBeenLastCalledWith("invalid");
  });

  it("revalidates when only an int64 value changed", async () => {
    validateWorkflow.mockResolvedValue({ valid: true, errors: [] });
    const { rerender } = setup({ workflow: loaded(maxAttempts({ default: "3" })), enabled: true });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LIVE_VALIDATION_DEBOUNCE_MS);
    });
    rerender({ workflow: loaded(maxAttempts({ default: "4" })), enabled: true });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LIVE_VALIDATION_DEBOUNCE_MS);
    });
    expect(validateWorkflow).toHaveBeenCalledTimes(2);
  });

  it("reports a canvas it cannot encode as unknown and retries it on the next edit", async () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    // A string field holding an object: what a save would also fail to encode.
    const broken = wf("a", { description: {} as unknown as string });
    const { rerender, setStatus } = setup({ workflow: broken, enabled: true });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LIVE_VALIDATION_DEBOUNCE_MS);
    });
    expect(validateWorkflow).not.toHaveBeenCalled();
    expect(setStatus).toHaveBeenLastCalledWith("unknown");
    expect(consoleError).toHaveBeenCalledWith("Live validation failed:", expect.any(Error));

    validateWorkflow.mockResolvedValue({ valid: true, errors: [] });
    rerender({ workflow: wf("a", { description: "fixed" }), enabled: true });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LIVE_VALIDATION_DEBOUNCE_MS);
    });
    expect(validateWorkflow).toHaveBeenCalledTimes(1);
    expect(setStatus).toHaveBeenLastCalledWith("valid");
    consoleError.mockRestore();
  });
});
