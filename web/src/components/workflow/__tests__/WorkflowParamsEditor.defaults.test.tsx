/**
 * A default set in the builder's Inputs editor survives save and reload, for
 * every type the editor offers a default for.
 *
 * Each input config types `default` differently on the wire: string, double,
 * int64 (a bigint in protobuf-es), bool, ModelSelector, or
 * google.protobuf.Value for the free-form types. The editor used to wrap every
 * default in a Value, which only the Value-typed fields accept, so Save threw
 * before reaching the server — "cannot encode field
 * reliant.v1.IntegerInputConfig.default to JSON: expected bigint (int64), got
 * object" — and a toolbar number could never get a default: its pill always
 * read "required".
 *
 * This drives the real path: the editor's control → onUpdate → workflowGrpc.
 * saveWorkflow (toWorkflowInit + create), encoded to JSON the way the Connect
 * transport encodes it → decoded back, as GetWorkflow returns it → the editor
 * again, and the chat toolbar pill.
 */
import { describe, it, expect, vi, beforeEach } from "vitest";
import React from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { fromJson, toJson, type JsonObject } from "@bufbuild/protobuf";

import { SaveWorkflowRequestSchema, type SaveWorkflowRequest } from "../../../gen/reliant/v1/workflow_pb";
import { WorkflowSchema } from "../../../gen/reliant/v1/workflow_v2_pb";
import { createInput, getInputDefault, type InputDef } from "../../../lib/inputHelpers";
import type { Param, Workflow } from "../../../types/workflow";

const rpc = vi.hoisted(() => ({ saveWorkflow: vi.fn() }));

vi.mock("../../../api/grpc-client", () => ({
  grpcClient: { workflow: () => ({ saveWorkflow: rpc.saveWorkflow }) },
}));
vi.mock("../../../api/preset-grpc", () => ({
  presetGrpc: {
    listPresets: vi.fn(async () => [{ name: "careful", description: "", params: {}, source: "project" }]),
  },
}));
vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (selector?: any) => {
    const state = { currentProject: { id: "p1" } };
    return selector ? selector(state) : state;
  },
}));
vi.mock("../../../store/globalDataStore", () => ({
  useModels: () => ({
    models: [{ id: "claude-sonnet-5", name: "Sonnet 5", provider: "anthropic" }],
    loading: false,
  }),
}));
// The real selector fetches the tool catalog; a button that picks one tool
// is all the editor needs from it.
vi.mock("../ToolsSelector", async () => {
  const { createElement } = await import("react");
  return {
    ToolsSelector: ({ value, onChange }: { value: string[]; onChange: (tools: string[]) => void }) =>
      createElement("button", { type: "button", onClick: () => onChange([...value, "bash"]) }, "Pick bash"),
  };
});

import { WorkflowParamsEditorContent } from "../WorkflowParamsEditor";
import { InlineParamInput } from "../InlineParamInput";
import { workflowGrpc } from "../../../api/workflow-grpc";

const NAME = "knob";

/** The editor's `<select>` that sets a default: the one offering "No default". */
function defaultSelect(): HTMLSelectElement {
  return screen.getByRole("option", { name: /no default/i }).closest("select") as HTMLSelectElement;
}

function renderEditor(params: Record<string, Param>) {
  const onUpdate = vi.fn();
  const view = render(React.createElement(WorkflowParamsEditorContent, { params, onUpdate }));
  return { onUpdate, view };
}

/** The params the editor last reported — what the builder holds and saves. */
function lastParams(onUpdate: ReturnType<typeof vi.fn>): Record<string, Param> {
  expect(onUpdate).toHaveBeenCalled();
  return onUpdate.mock.calls.at(-1)![0] as Record<string, Param>;
}

/**
 * Save through workflowGrpc, encoding the request to JSON exactly as the
 * Connect transport does (useBinaryFormat: false), then decode the stored
 * workflow back the way GetWorkflow hands it to the builder and the composer.
 */
async function saveAndReload(inputs: Record<string, Param>): Promise<{ workflow: Workflow; wire: JsonObject }> {
  let wire: JsonObject | undefined;
  rpc.saveWorkflow.mockImplementation(async (request: SaveWorkflowRequest) => {
    wire = toJson(SaveWorkflowRequestSchema, request) as JsonObject;
    return { success: true, message: "", validationErrors: [], version: 1n };
  });
  await workflowGrpc.saveWorkflow("p1", { name: "wf", inputs, nodes: [], edges: [] } as Workflow);
  const workflow = fromJson(WorkflowSchema, wire!.workflow as JsonObject) as Workflow;
  return { workflow, wire: wire! };
}

interface DefaultCase {
  type: string;
  /** Set the default through the editor's own control. */
  set: () => Promise<void> | void;
  /** getInputDefault after reload. */
  expected: unknown;
  /** Show the default again, as the reloaded editor does. */
  shownInEditor: () => void;
  /** What the chat toolbar pill shows when the person has not set a value. */
  toolbar?: string;
  init?: Record<string, unknown>;
}

const cases: DefaultCase[] = [
  {
    type: "string",
    set: () => fireEvent.change(screen.getByPlaceholderText("default value"), { target: { value: "main" } }),
    expected: "main",
    shownInEditor: () => expect(screen.getByPlaceholderText("default value")).toHaveValue("main"),
    toolbar: "main",
  },
  {
    type: "integer",
    set: () => fireEvent.change(screen.getByPlaceholderText("0"), { target: { value: "5" } }),
    expected: 5,
    shownInEditor: () => expect(screen.getByPlaceholderText("0")).toHaveValue(5),
    toolbar: "5",
  },
  {
    type: "number",
    set: () => fireEvent.change(screen.getByPlaceholderText("0.0"), { target: { value: "0.75" } }),
    expected: 0.75,
    shownInEditor: () => expect(screen.getByPlaceholderText("0.0")).toHaveValue(0.75),
    toolbar: "0.75",
  },
  {
    type: "boolean",
    set: () => userEvent.selectOptions(defaultSelect(), "true"),
    expected: true,
    shownInEditor: () => expect(defaultSelect()).toHaveValue("true"),
  },
  {
    type: "enum",
    init: { enumValues: ["a", "b"] },
    // Not the first option: the toolbar falls back to that one on its own.
    set: () => userEvent.selectOptions(defaultSelect(), "b"),
    expected: "b",
    shownInEditor: () => expect(defaultSelect()).toHaveValue("b"),
    toolbar: "b",
  },
  {
    type: "model",
    set: () => userEvent.selectOptions(defaultSelect(), "claude-sonnet-5"),
    expected: expect.objectContaining({ id: "claude-sonnet-5" }),
    shownInEditor: () => expect(defaultSelect()).toHaveValue("claude-sonnet-5"),
  },
  {
    type: "object",
    set: () =>
      fireEvent.change(document.querySelector("textarea")!, { target: { value: '{"retries": 2}' } }),
    expected: { retries: 2 },
    shownInEditor: () => expect(JSON.parse((document.querySelector("textarea") as HTMLTextAreaElement).value)).toEqual({ retries: 2 }),
  },
  {
    type: "any",
    set: () => fireEvent.change(screen.getByPlaceholderText("default value"), { target: { value: "anything" } }),
    expected: "anything",
    shownInEditor: () => expect(screen.getByPlaceholderText("default value")).toHaveValue("anything"),
  },
  {
    type: "tools",
    set: () => userEvent.click(screen.getByRole("button", { name: "Pick bash" })),
    expected: ["bash"],
    shownInEditor: () => undefined, // the selector is stubbed; the value assertion covers it
  },
  {
    type: "preset",
    set: async () => userEvent.click(await screen.findByRole("checkbox", { name: /careful/ })),
    expected: ["careful"],
    shownInEditor: () => undefined, // asserted once the preset list loads, below
  },
];

describe("WorkflowParamsEditor defaults survive save and reload", () => {
  beforeEach(() => {
    rpc.saveWorkflow.mockReset();
  });

  it.each(cases)("$type", async ({ type, init, set, expected, shownInEditor, toolbar }) => {
    const { onUpdate } = renderEditor({ [NAME]: createInput(type, init) as Param });
    await set();

    // Save: the request must encode. This is where the old editor threw.
    const { workflow } = await saveAndReload(lastParams(onUpdate));
    const reloaded = workflow.inputs![NAME] as InputDef;
    expect(reloaded.type).toBe(type);
    expect(getInputDefault(reloaded)).toEqual(expected);

    // Reload into the builder: the editor shows the saved default.
    cleanup();
    renderEditor(workflow.inputs as Record<string, Param>);
    shownInEditor();
    if (type === "preset") {
      expect(await screen.findByRole("checkbox", { name: /careful/ })).toBeChecked();
    }

    // The composer's toolbar pill starts from the default, not "required".
    if (toolbar !== undefined) {
      cleanup();
      render(React.createElement(InlineParamInput, { name: NAME, schema: reloaded, value: undefined, onChange: vi.fn() }));
      // The enum pill title-cases its option ("b" → "B"), hence /i.
      expect(screen.getByRole("button")).toHaveTextContent(new RegExp(`^Knob:\\s*${toolbar}$`, "i"));
      expect(screen.queryByText("required")).not.toBeInTheDocument();
    }
  });

  it("boolean: the toolbar toggle starts from the default", async () => {
    const { onUpdate } = renderEditor({ [NAME]: createInput("boolean") as Param });
    await userEvent.selectOptions(defaultSelect(), "true");
    const { workflow } = await saveAndReload(lastParams(onUpdate));

    cleanup();
    const onChange = vi.fn();
    render(React.createElement(InlineParamInput, { name: NAME, schema: workflow.inputs![NAME] as InputDef, value: undefined, onChange }));
    await userEvent.click(screen.getByRole("button"));
    // Toggling flips the default (true), so it reports false.
    expect(onChange).toHaveBeenCalledWith(false);
  });

  it("integer: an int64 past 2^53 is saved and shown exactly", async () => {
    const big = "9007199254740993"; // 2^53 + 1: a JS number rounds it to ...992
    const { onUpdate } = renderEditor({ [NAME]: createInput("integer") as Param });
    fireEvent.change(screen.getByPlaceholderText("0"), { target: { value: big } });
    expect(screen.getByPlaceholderText("0")).toHaveDisplayValue(big);

    const { workflow, wire } = await saveAndReload(lastParams(onUpdate));
    // int64 is a string in proto JSON; it must carry every digit.
    expect((wire.workflow as any).inputs[NAME].integerInput.default).toBe(big);

    cleanup();
    renderEditor(workflow.inputs as Record<string, Param>);
    expect(screen.getByPlaceholderText("0")).toHaveDisplayValue(big);
  });

  it("clearing a default removes it rather than saving an empty value", async () => {
    const { onUpdate } = renderEditor({ [NAME]: createInput("integer") as Param });
    const field = screen.getByPlaceholderText("0");
    fireEvent.change(field, { target: { value: "3" } });
    fireEvent.change(field, { target: { value: "" } });

    const { workflow, wire } = await saveAndReload(lastParams(onUpdate));
    expect((wire.workflow as any).inputs[NAME].integerInput.default).toBeUndefined();
    expect(getInputDefault(workflow.inputs![NAME] as InputDef)).toBeUndefined();
  });
});
