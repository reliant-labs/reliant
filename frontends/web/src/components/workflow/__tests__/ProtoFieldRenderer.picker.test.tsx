/**
 * Two field shapes the renderer gained:
 *
 *  - the 'picker' widget, for a field whose values the server lists (Run
 *    Tool's tool): a searchable list with descriptions, and still Fixed /
 *    Expression, since a step may compute which tool to run;
 *  - a format hint for a fixed value that fails the field's pattern (Slack's
 *    channel wants an ID like C0123ABCDEF, not "#general"). It warns and
 *    does not block: the server is what validates.
 */
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("../../../lib/monacoManager", () => ({ useMonaco: () => null }));
vi.mock("../../../lib/cel-completion-service", async () => ({
  ...(await vi.importActual<typeof import("../../../lib/cel-completion-service")>("../../../lib/cel-completion-service")),
  ensureCELCompletionsCached: async () => {},
}));

import { ProtoFieldRenderer, failsPattern } from "../ProtoFieldRenderer";
import type { ProtoFieldSchema } from "../../../types/workflowFieldSchema";

const toolField: ProtoFieldSchema = {
  key: "tool",
  label: "Tool",
  widget: "picker",
  valueKind: "string",
  celCapable: true,
  showCelModeToggle: true,
  options: [
    { value: "view", label: "view", description: "Read a file with line numbers." },
    { value: "websearch", label: "websearch", description: "Search the web." },
  ],
};

const channelField: ProtoFieldSchema = {
  key: "channel",
  label: "Channel",
  widget: "text",
  valueKind: "string",
  celCapable: true,
  example: "C0123ABCDEF",
  pattern: "^[CGD][A-Z0-9]{8,}$",
};

function Field({ schema, initial = "", onValue }: { schema: ProtoFieldSchema; initial?: string; onValue?: (v: unknown) => void }) {
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

describe("ProtoFieldRenderer picker widget", () => {
  it("picks a listed value, with what each one does", async () => {
    const user = userEvent.setup();
    const onValue = vi.fn();
    render(<Field schema={toolField} onValue={onValue} />);
    const picker = screen.getByLabelText("Tool", { selector: "button" });
    expect(picker).toHaveTextContent("Select tool…");
    await user.click(picker);
    expect(screen.getByRole("option", { name: /websearch/ })).toHaveTextContent("Search the web.");
    await user.click(screen.getByRole("option", { name: /websearch/ }));
    expect(onValue).toHaveBeenLastCalledWith("websearch");
  });

  it("keeps Expression, for a step that computes which tool to run", async () => {
    const user = userEvent.setup();
    render(<Field schema={toolField} />);
    await user.click(screen.getByRole("button", { name: /Expression/ }));
    expect(screen.queryByLabelText("Tool", { selector: "button" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Expression/ })).toHaveAttribute("aria-pressed", "true");
  });

  it("opens an expression value in Expression mode", () => {
    render(<Field schema={toolField} initial="{{ inputs.tool }}" />);
    expect(screen.getByRole("button", { name: /Expression/ })).toHaveAttribute("aria-pressed", "true");
  });
});

describe("ProtoFieldRenderer format hint", () => {
  it("warns under a fixed value that fails the field's pattern, naming the expected shape", () => {
    render(<Field schema={channelField} initial="#general" />);
    expect(screen.getByLabelText("Channel")).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByText(/This doesn't look like a Channel/)).toHaveTextContent("Expected something like C0123ABCDEF.");
  });

  it("says nothing for a value that matches, or before one is typed", () => {
    const { unmount } = render(<Field schema={channelField} initial="C0123ABCDEF" />);
    expect(screen.queryByText(/doesn't look like/)).not.toBeInTheDocument();
    expect(screen.getByLabelText("Channel")).not.toHaveAttribute("aria-invalid");
    unmount();
    render(<Field schema={channelField} />);
    expect(screen.queryByText(/doesn't look like/)).not.toBeInTheDocument();
  });
});

describe("failsPattern", () => {
  it("checks a fixed value against the pattern", () => {
    expect(failsPattern("^[CGD][A-Z0-9]{8,}$", "general")).toBe(true);
    expect(failsPattern("^[CGD][A-Z0-9]{8,}$", "C0123ABCDEF")).toBe(false);
  });

  it("leaves templates, empty values and an unusable pattern alone", () => {
    expect(failsPattern("^[CGD][A-Z0-9]{8,}$", "{{ inputs.channel }}")).toBe(false);
    expect(failsPattern("^[CGD][A-Z0-9]{8,}$", "")).toBe(false);
    expect(failsPattern(undefined, "anything")).toBe(false);
    expect(failsPattern("([", "anything")).toBe(false);
  });
});
