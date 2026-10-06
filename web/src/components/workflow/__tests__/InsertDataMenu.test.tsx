/**
 * "Insert data" (UX review §1 issue 5): every expression-capable field can
 * list what it can read — inputs, the trigger, and the outputs of the steps
 * that run before this one — and insert a path without the author knowing
 * the `{{ }}` syntax, the node id or the output name.
 */
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("../../../lib/monacoManager", () => ({ useMonaco: () => null }));
vi.mock("../../../lib/cel-completion-service", async () => ({
  ...(await vi.importActual<typeof import("../../../lib/cel-completion-service")>("../../../lib/cel-completion-service")),
  ensureCELCompletionsCached: async () => {},
}));
// A step's outputs come from the node catalog, as its Outputs tab's do:
// call_llm's fields with their sub-fields, and debug plumbing marked advanced.
vi.mock("../../../lib/node-metadata", async () => {
  const { create } = await import("@bufbuild/protobuf");
  const { NodeInfoSchema, NodeInputFieldSchema } = await import("../../../gen/reliant/v1/catalog_pb");
  const field = (name: string, type: string, extra: Record<string, unknown> = {}) => create(NodeInputFieldSchema, { name, type, description: "", ...extra });
  const nodes = [
    create(NodeInfoSchema, {
      id: "call_llm",
      outputFields: [
        field("response_text", "string", { description: "The reply text" }),
        field("tool_calls", "array", { children: [field("name", "string", { description: "The tool's name" })] }),
        field("message", "object", { children: [field("text", "string")] }),
        field("upstream_proxyman_id", "string", { visibilityContexts: ["advanced"] }),
      ],
    }),
  ];
  return {
    ...(await vi.importActual<typeof import("../../../lib/node-metadata")>("../../../lib/node-metadata")),
    ensureNodesCached: async () => nodes,
    getCachedNodes: () => nodes,
  };
});

import { ProtoFieldRenderer } from "../ProtoFieldRenderer";
import { hasOpenEscapeLayer } from "../../../hooks/useEscapeLayer";
import { CELCompletionProvider, CELCurrentNodeProvider, type CELCompletionContextValue } from "../CELCompletionContext";
import type { ProtoFieldSchema } from "../../../types/workflowFieldSchema";

const context: CELCompletionContextValue = {
  nodeIds: ["plan", "tools", "later"],
  nodeTypeMap: { plan: "call_llm", tools: "execute_tools", later: "call_llm" },
  inputParams: { topic: { type: "string", description: "What to research" } },
  edges: [
    { source: "plan", target: "tools" },
    { source: "tools", target: "later" },
  ],
};

const toolCallsField: ProtoFieldSchema = {
  key: "tool_calls",
  label: "Tool calls",
  widget: "text",
  valueKind: "string",
  celCapable: true,
};

function Field({ schema = toolCallsField, initial = "", onValue }: { schema?: ProtoFieldSchema; initial?: string; onValue?: (v: unknown) => void }) {
  const [value, setValue] = useState<unknown>(initial);
  return (
    <ProtoFieldRenderer
      schema={schema}
      value={value}
      onChange={(next) => {
        setValue(next);
        onValue?.(next);
      }}
    />
  );
}

function renderField(props: Parameters<typeof Field>[0] = {}, currentNode: string | null = "tools") {
  const onKeyDown = vi.fn();
  render(
    // The builder's own Escape handler sits above every panel.
    <div onKeyDown={onKeyDown}>
      <CELCompletionProvider value={context}>
        <CELCurrentNodeProvider value={currentNode}>
          <Field {...props} />
        </CELCurrentNodeProvider>
      </CELCompletionProvider>
    </div>,
  );
  return { onKeyDown };
}

describe("Insert data", () => {
  it("lists inputs, the trigger and the outputs of the steps before this one", async () => {
    const user = userEvent.setup();
    renderField();
    await user.click(screen.getByRole("button", { name: "Insert data" }));

    const menu = screen.getByRole("dialog", { name: "Insert data into Tool calls" });
    expect(within(menu).getByRole("group", { name: "Inputs" })).toHaveTextContent("inputs.topic");
    expect(within(menu).getByRole("group", { name: "Trigger" })).toHaveTextContent("trigger.kind");
    const plan = within(menu).getByRole("group", { name: "plan" });
    expect(plan).toHaveTextContent("nodes.plan.tool_calls");
    expect(plan).toHaveTextContent("The reply text");
    // `later` runs after this step, so it has nothing to read yet.
    expect(within(menu).queryByRole("group", { name: "later" })).toBeNull();
  });

  it("offers what the step's Outputs tab lists: sub-fields in, debug fields out", async () => {
    const user = userEvent.setup();
    renderField();
    await user.click(screen.getByRole("button", { name: "Insert data" }));
    const plan = within(screen.getByRole("dialog")).getByRole("group", { name: "plan" });
    expect(within(plan).getAllByRole("option").map((o) => o.querySelector("code")!.textContent)).toEqual([
      "nodes.plan.response_text",
      "nodes.plan.tool_calls",
      "nodes.plan.tool_calls[0].name",
      "nodes.plan.message",
      "nodes.plan.message.text",
    ]);
  });

  it("inserts the path as an expression and switches the field to Expression", async () => {
    const user = userEvent.setup();
    const onValue = vi.fn();
    renderField({ onValue });
    await user.click(screen.getByRole("button", { name: "Insert data" }));
    await user.click(screen.getByRole("option", { name: /^nodes\.plan\.tool_calls(?!\[)/ }));

    expect(onValue).toHaveBeenLastCalledWith("{{ nodes.plan.tool_calls }}");
    expect(screen.getByRole("button", { name: "Expression" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("inserts at the cursor of an expression already being written, without doubling the braces", async () => {
    const user = userEvent.setup();
    const onValue = vi.fn();
    renderField({ initial: "Topic: {{ }}", onValue });
    const input = screen.getByDisplayValue("Topic: {{ }}") as HTMLInputElement;
    input.setSelectionRange(10, 10); // inside the open template
    await user.click(screen.getByRole("button", { name: "Insert data" }));
    await user.click(screen.getByRole("option", { name: /inputs\.topic/ }));
    expect(onValue).toHaveBeenLastCalledWith("Topic: {{ inputs.topic}}");
  });

  it("searches, picks with the keyboard, and keeps focus in the search box", async () => {
    const user = userEvent.setup();
    const onValue = vi.fn();
    renderField({ onValue });
    await user.click(screen.getByRole("button", { name: "Insert data" }));
    const search = screen.getByRole("combobox", { name: "Search data" });
    expect(search).toHaveFocus();
    await user.type(search, "reply");
    const menu = screen.getByRole("dialog");
    expect(within(menu).getAllByRole("option").map((o) => o.textContent)).toEqual([expect.stringContaining("nodes.plan.response_text")]);
    expect(search).toHaveAttribute("aria-activedescendant", within(menu).getByRole("option").id);

    await user.keyboard("{Enter}");
    expect(onValue).toHaveBeenLastCalledWith("{{ nodes.plan.response_text }}");
  });

  it("closes on Escape without the Escape reaching the builder", async () => {
    const user = userEvent.setup();
    const { onKeyDown } = renderField();
    await user.click(screen.getByRole("button", { name: "Insert data" }));
    // The builder's capture-phase Escape handler stands aside for an open layer.
    expect(hasOpenEscapeLayer()).toBe(true);
    // Focus never leaves the search box, which the builder's own Escape
    // handler ignores; the menu stops it from bubbling further.
    fireEvent.keyDown(screen.getByRole("combobox", { name: "Search data" }), { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(onKeyDown.mock.calls.filter(([event]) => event.key === "Escape")).toHaveLength(0);
    expect(screen.getByRole("button", { name: "Insert data" })).toHaveFocus();
  });

  it("says where step outputs come from when nothing runs before this step", async () => {
    const user = userEvent.setup();
    renderField({}, "plan");
    await user.click(screen.getByRole("button", { name: "Insert data" }));
    expect(screen.getByText(/Outputs of steps that run before this one appear here/)).toBeInTheDocument();
  });

  it("is not offered outside a workflow's completion context (a run form)", () => {
    render(<Field />);
    expect(screen.queryByRole("button", { name: "Insert data" })).toBeNull();
  });
});
