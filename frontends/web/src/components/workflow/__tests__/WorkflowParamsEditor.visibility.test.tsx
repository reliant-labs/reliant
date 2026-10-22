/**
 * The builder's Visibility control must speak the composer's language.
 *
 * ChatInput reads a param's YAML `ui` value: "toolbar" renders it under the
 * chat input, "hidden" never shows it, anything else puts it behind ⚙. The
 * control used to offer only "Visible in UI" (writing "config") and "Hidden",
 * so a toolbar param could not be made from the builder — and editing one
 * displayed it as "Visible in UI", one click from silently demoting it.
 */
import { describe, it, expect, vi } from "vitest";
import React from "react";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { createInput, getInputUI, type InputDef } from "../../../lib/inputHelpers";

// The editor's type-specific inputs reach for presets, models and the project;
// none of that is under test here.
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
  render(
    React.createElement(WorkflowParamsEditorContent, {
      params: { mode: param as any },
      onUpdate,
    }),
  );
  const select = screen.getByLabelText(/visibility in chat/i) as HTMLSelectElement;
  return { select, onUpdate };
}

/** The `ui` value of the `mode` param in the editor's latest onUpdate call. */
function lastUi(onUpdate: ReturnType<typeof vi.fn>): string | undefined {
  const params = onUpdate.mock.calls.at(-1)![0] as Record<string, InputDef>;
  return getInputUI(params.mode);
}

describe("WorkflowParamsEditor visibility", () => {
  it("shows a toolbar param as Toolbar, not as a generic 'visible' option", () => {
    const { select } = renderEditor(createInput("enum", { ui: "toolbar", enumValues: ["a", "b"] }));
    expect(select.value).toBe("toolbar");
    expect(within(select).getByRole("option", { name: /toolbar/i })).toBeInTheDocument();
  });

  it("offers exactly the three placements the composer understands", () => {
    const { select } = renderEditor(createInput("boolean"));
    const values = within(select).getAllByRole("option").map((o) => (o as HTMLOptionElement).value);
    expect(values).toEqual(["toolbar", "settings", "hidden"]);
    // No value means "behind ⚙" — the composer's default.
    expect(select.value).toBe("settings");
  });

  it("writes ui: toolbar when promoted to the toolbar", async () => {
    const { select, onUpdate } = renderEditor(createInput("boolean"));
    await userEvent.selectOptions(select, "toolbar");
    expect(lastUi(onUpdate)).toBe("toolbar");
  });

  it("clears ui when moved back to Settings, rather than writing a value", async () => {
    const { select, onUpdate } = renderEditor(createInput("enum", { ui: "toolbar", enumValues: ["a"] }));
    await userEvent.selectOptions(select, "settings");
    expect(lastUi(onUpdate) ?? "").toBe("");
  });

  it("writes ui: hidden", async () => {
    const { select, onUpdate } = renderEditor(createInput("string"));
    await userEvent.selectOptions(select, "hidden");
    expect(lastUi(onUpdate)).toBe("hidden");
  });

  it("does not offer Toolbar for a type the toolbar cannot render", () => {
    const { select } = renderEditor(createInput("tools"));
    const values = within(select).getAllByRole("option").map((o) => (o as HTMLOptionElement).value);
    expect(values).toEqual(["settings", "hidden"]);
  });

  it("leaves a renderer hint like textarea alone until the placement changes", () => {
    const { select, onUpdate } = renderEditor(createInput("string", { ui: "textarea" }));
    expect(select.value).toBe("settings");
    expect(onUpdate).not.toHaveBeenCalled();
  });
});
