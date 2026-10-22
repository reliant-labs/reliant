/**
 * The Outputs tab (research/WORKFLOW_EDITOR_UX_REVIEW.md issue 5): it lists
 * what downstream steps read — call_llm's message, tool_calls and
 * response_data, with their sub-fields — and keeps debug plumbing such as
 * upstream_proxyman_id behind an "Advanced" disclosure.
 */
import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { NodeInputFieldSchema } from "../../../../gen/reliant/v1/catalog_pb";
import type { Step } from "../../../../types/workflow";
import { NodeOutputsPanel } from "../NodeOutputsPanel";

const field = (name: string, type: string, extra: Partial<Parameters<typeof create<typeof NodeInputFieldSchema>>[1]> = {}) =>
  create(NodeInputFieldSchema, { name, type, description: "", ...extra });

// call_llm's output fields as CatalogService.ListNodes returns them.
const callLLMOutputs = [
  field("message", "object", { children: [field("text", "string"), field("role", "string")] }),
  field("response_text", "string"),
  field("tool_calls", "array", { children: [field("name", "string"), field("input", "string")] }),
  field("response_data", "object"),
  field("upstream_proxyman_id", "string", { visibilityContexts: ["advanced"] }),
  field("last_stream_seq", "integer", { visibilityContexts: ["advanced"] }),
];

const step = { id: "ask", type: "call_llm" } as Step;

describe("NodeOutputsPanel", () => {
  it("leads with the fields people read, and hides debug fields behind Advanced", () => {
    render(<NodeOutputsPanel step={step} catalogOutputFields={callLLMOutputs} />);
    for (const name of ["message", "response_text", "tool_calls", "response_data"]) {
      expect(screen.getByText(name)).toBeVisible();
    }
    const advanced = screen.getByTestId("advanced-outputs");
    expect(within(advanced).getByText("Advanced (2)")).toBeInTheDocument();
    expect(advanced).not.toHaveAttribute("open");
    expect(within(advanced).getByText("upstream_proxyman_id")).not.toBeVisible();
  });

  it("expands a message field into its sub-fields, with copyable paths", async () => {
    render(<NodeOutputsPanel step={step} catalogOutputFields={callLLMOutputs} />);
    await userEvent.click(screen.getByRole("button", { name: "Show the fields of tool_calls" }));
    expect(screen.getByRole("button", { name: "Copy: nodes.ask.tool_calls[0].name" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Show the fields of message" }));
    expect(screen.getByRole("button", { name: "Copy: nodes.ask.message.text" })).toBeInTheDocument();
  });
});
