/**
 * A workflow input's description and example are what the Run and Activate
 * forms show for it: the description under the field, the example in its
 * empty box. YAML could declare both, but the builder's params editor offered
 * neither, so an input built in the UI reached the Run form as a bare name.
 */
import { describe, it, expect, vi } from "vitest";
import React from "react";
import { fireEvent, render, screen } from "@testing-library/react";

import { createInput, getInputDescription, getInputExample, type InputDef } from "../../../lib/inputHelpers";

vi.mock("../../../api/preset-grpc", () => ({ presetGrpc: { listPresets: vi.fn(async () => []) } }));
vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (selector?: any) => {
    const state = { currentProject: null };
    return selector ? selector(state) : state;
  },
}));
vi.mock("../../../store/globalDataStore", () => ({ useModels: () => [] }));
vi.mock("../ToolsSelector", () => ({ ToolsSelector: () => null }));

import { WorkflowParamsEditorContent } from "../WorkflowParamsEditor";

function renderEditor(param: InputDef) {
  const onUpdate = vi.fn();
  render(React.createElement(WorkflowParamsEditorContent, { params: { channel: param as any }, onUpdate }));
  const latest = () => (onUpdate.mock.calls.at(-1)![0] as Record<string, InputDef>).channel;
  return { onUpdate, latest };
}

describe("WorkflowParamsEditor description and example", () => {
  it("shows what the input already declares", () => {
    renderEditor(createInput("string", { description: "Where to post", example: "C0123ABCDEF" }));
    expect(screen.getByLabelText("Description")).toHaveValue("Where to post");
    expect(screen.getByLabelText("Example")).toHaveValue("C0123ABCDEF");
  });

  it("writes the description and the example into the input", () => {
    const { latest } = renderEditor(createInput("string"));
    fireEvent.change(screen.getByLabelText("Description"), { target: { value: "Where to post" } });
    expect(getInputDescription(latest())).toBe("Where to post");
    fireEvent.change(screen.getByLabelText("Example"), { target: { value: "C0123ABCDEF" } });
    expect(getInputExample(latest())).toBe("C0123ABCDEF");
  });
});
