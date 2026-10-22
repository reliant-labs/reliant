/**
 * The Run form (and a workflow step's inputs) render each declared input
 * through WorkflowInputGroup. An unset input must say what it will be: its
 * description under it, its example in the empty box, and its default —
 * "Default: flagship" for a model, and a tools default shown as the selection
 * it is. The tools default used to render as nothing selected, because the
 * form hands it over as the list it is declared as and the widget only read a
 * comma-separated string.
 */
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

vi.mock("../../../store/globalDataStore", () => ({
  useModels: () => ({ models: [{ id: "claude-opus-5-5", name: "Claude Opus 5.5", provider: "Anthropic" }], loading: false }),
  useGlobalDataStore: (selector: (state: unknown) => unknown) =>
    selector({ isInitialized: true, isPrefetching: false, refetchWorkflowPresets: async () => undefined }),
}));
vi.mock("../../../api/preset-grpc", () => ({ presetGrpc: {} }));
vi.mock("../../../api/client", () => ({
  api: { tools: { list: async () => ({ tools: [{ name: "view", description: "Read a file", category: "File" }] }) } },
}));

import { WorkflowInputGroup } from "../WorkflowInputGroup";
import { createInput } from "../../../lib/inputHelpers";

function renderGroup() {
  render(
    <WorkflowInputGroup
      isTopLevel
      group={{
        name: "",
        label: "",
        inputs: [
          { name: "topic", schema: createInput("string", { description: "What to research", example: "the history of CEL" }) },
          { name: "model", schema: createInput("model", { default: { tags: ["flagship"] } }) },
          { name: "tools", schema: createInput("tools", { default: ["tag:coding:default"] }) },
        ],
      }}
      values={{}}
      onChange={() => undefined}
      presets={[]}
      selectedPreset={null}
      onPresetSelect={() => undefined}
    />,
  );
}

describe("WorkflowInputGroup defaults", () => {
  it("shows an input's description under it and its example in the empty box", () => {
    renderGroup();
    expect(screen.getByText("What to research")).toBeInTheDocument();
    expect(screen.getByLabelText("Topic")).toHaveAttribute("placeholder", "the history of CEL");
  });

  it("says which model an unset model input will use", () => {
    renderGroup();
    expect(screen.getByRole("button", { name: /Default: flagship/ })).toBeInTheDocument();
  });

  // The hint under a default-only input printed the selector's JSON:
  // Default: {"tags":["flagship"]}, Default: ["tag:coding:default"].
  it("words the default hint, never as JSON", () => {
    renderGroup();
    expect(screen.getByText("Default: flagship (any provider)")).toBeInTheDocument();
    expect(screen.getByText("Default: tag:coding:default")).toBeInTheDocument();
    expect(screen.queryByText(/Default: [[{]/)).not.toBeInTheDocument();
  });

  it("shows a tools default as the tools it selects", async () => {
    renderGroup();
    expect(await screen.findByTitle("Expression")).toHaveTextContent("tag:coding:default");
    expect(screen.getByRole("button", { name: "Remove tag:coding:default" })).toBeInTheDocument();
  });
});
