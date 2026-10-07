/**
 * Changing an input's Type in the builder keeps the name it was given.
 *
 * The editor tracks each input's name and a stable React key on the input
 * itself (`_name`, `_id`). A type change rebuilds the input for the new config
 * case, and it used to rebuild it without those: the Name field went blank,
 * the input was saved under the key "undefined", and two such inputs shared
 * the key `undefined` in the list.
 */
import { describe, it, expect, vi, afterEach } from "vitest";
import React from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { getInputDefault, getInputUI, type InputDef } from "../../../lib/inputHelpers";
import type { Param } from "../../../types/workflow";

vi.mock("../../../api/preset-grpc", () => ({ presetGrpc: { listPresets: vi.fn(async () => []) } }));
vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (selector?: any) => {
    const state = { currentProject: null };
    return selector ? selector(state) : state;
  },
}));
vi.mock("../../../store/globalDataStore", () => ({ useModels: () => ({ models: [], loading: false }) }));
vi.mock("../ToolsSelector", () => ({ ToolsSelector: () => null }));

import { WorkflowParamsEditorContent } from "../WorkflowParamsEditor";

function renderEmptyEditor() {
  const onUpdate = vi.fn();
  render(React.createElement(WorkflowParamsEditorContent, { params: {}, onUpdate }));
  return onUpdate;
}

function lastParams(onUpdate: ReturnType<typeof vi.fn>): Record<string, Param> {
  return onUpdate.mock.calls.at(-1)![0] as Record<string, Param>;
}

function nameFields(): HTMLInputElement[] {
  return screen.getAllByPlaceholderText("input_name") as HTMLInputElement[];
}

/** Each input's Type select, in order: the selects that offer "Integer". */
function typeSelects(): HTMLSelectElement[] {
  return screen.getAllByRole("option", { name: "Integer" }).map((o) => o.closest("select") as HTMLSelectElement);
}

describe("WorkflowParamsEditor type change", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("keeps the name typed before the type was changed", async () => {
    const onUpdate = renderEmptyEditor();
    await userEvent.click(screen.getByRole("button", { name: /add input/i }));
    fireEvent.change(nameFields()[0], { target: { value: "max_attempts" } });

    await userEvent.selectOptions(typeSelects()[0], "integer");

    expect(nameFields()[0]).toHaveValue("max_attempts");
    const params = lastParams(onUpdate);
    expect(Object.keys(params)).toEqual(["max_attempts"]);
    expect(params.max_attempts.type).toBe("integer");
  });

  it("keeps every input's name and key distinct across type changes", async () => {
    const consoleError = vi.spyOn(console, "error");
    const onUpdate = renderEmptyEditor();
    const addInput = screen.getByRole("button", { name: /add input/i });
    await userEvent.click(addInput);
    await userEvent.click(addInput);

    await userEvent.selectOptions(typeSelects()[0], "boolean");
    await userEvent.selectOptions(typeSelects()[1], "enum");

    expect(nameFields().map((f) => f.value)).toEqual(["input1", "input2"]);
    const params = lastParams(onUpdate);
    expect(Object.keys(params)).toEqual(["input1", "input2"]);
    expect(params.input1.type).toBe("boolean");
    expect(params.input2.type).toBe("enum");
    // React reports colliding list keys and uncontrolled→controlled inputs here.
    const reactWarnings = consoleError.mock.calls
      .map((args) => String(args[0]))
      .filter((msg) => /same key|uncontrolled|controlled/i.test(msg));
    expect(reactWarnings).toEqual([]);
  });

  it("the knobs video flow: name, then Integer, then a default and Toolbar", async () => {
    const onUpdate = renderEmptyEditor();
    await userEvent.click(screen.getByRole("button", { name: /add input/i }));
    fireEvent.change(nameFields()[0], { target: { value: "max_attempts" } });
    await userEvent.selectOptions(typeSelects()[0], "integer");
    fireEvent.change(screen.getByPlaceholderText("0"), { target: { value: "3" } });
    await userEvent.selectOptions(screen.getByLabelText(/visibility in chat/i), "toolbar");

    const params = lastParams(onUpdate);
    expect(Object.keys(params)).toEqual(["max_attempts"]);
    const input = params.max_attempts as InputDef;
    expect(input.config?.case).toBe("integerInput");
    expect(getInputUI(input)).toBe("toolbar");
    expect(getInputDefault(input)).toBe(3);
  });
});
