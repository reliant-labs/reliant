/**
 * The Trigger payload tab (research/WORKFLOW_UI.md §3.2): it lists what
 * `trigger.*` exposes to CEL, and clicking a field inserts its path into the
 * CEL input that was focused last, through CELCompletionContext.
 *
 * The input is the real CELInput. Monaco does not load under jsdom, so it
 * renders its plain-input fallback, which takes the same insertion path.
 */

import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("../../../../lib/monacoManager", () => ({
  useMonaco: () => null,
}));

import { CELCompletionProvider } from "../../CELCompletionContext";
import { CELInput } from "../../CELInput";
import { TriggerPayloadPanel } from "../TriggerPayloadPanel";
import { TRIGGER_CEL_FIELDS } from "../../../../lib/trigger-cel-fields";

// The CEL editor fetches its completion catalog when it mounts. These tests
// render the editor but never complete anything, and unmocked the fetch was a
// real RPC that settled after the test.
vi.mock("../../../../lib/cel-completion-service", async () => ({
  ...(await vi.importActual<typeof import("../../../../lib/cel-completion-service")>(
    "../../../../lib/cel-completion-service",
  )),
  ensureCELCompletionsCached: async () => {},
}));

const completion = { nodeIds: [], nodeTypeMap: {}, inputParams: {} };

function Harness({ pureExpression = false, initial = "" }: { pureExpression?: boolean; initial?: string }) {
  const [value, setValue] = useState(initial);
  return (
    <CELCompletionProvider value={completion}>
      <CELInput
        id="prompt"
        label="Prompt"
        value={value}
        onChange={setValue}
        pureExpression={pureExpression}
        placeholder="Prompt"
        hideCELHint
      />
      <TriggerPayloadPanel onClose={() => undefined} />
    </CELCompletionProvider>
  );
}

describe("TriggerPayloadPanel", () => {
  it("lists every trigger field with its description", () => {
    render(<Harness />);
    const list = screen.getByRole("list", { name: "Trigger fields" });
    for (const field of TRIGGER_CEL_FIELDS) {
      const item = within(list).getByRole("button", { name: new RegExp(`trigger\\.${field.name}\\b`) });
      expect(item).toHaveTextContent(field.description);
    }
  });

  it("asks for a focused input before inserting", () => {
    render(<Harness />);
    expect(screen.getByText(/Click into an expression field/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /trigger\.kind\b/ })).toBeDisabled();
  });

  it("inserts the clicked field's path into the last focused input, wrapped for a template", async () => {
    const user = userEvent.setup();
    render(<Harness initial="Run for " />);

    const input = screen.getByPlaceholderText("Prompt");
    await user.click(input);
    // The click moves focus to the panel; the input stays the target.
    await user.click(screen.getByRole("button", { name: /trigger\.scheduled_for\b/ }));

    expect(input).toHaveValue("Run for {{ trigger.scheduled_for }}");
  });

  it("inserts the bare path into a pure expression", async () => {
    const user = userEvent.setup();
    render(<Harness pureExpression initial="" />);

    await user.click(screen.getByPlaceholderText("Prompt"));
    await user.click(screen.getByRole("button", { name: /trigger\.kind\b/ }));

    expect(screen.getByPlaceholderText("Prompt")).toHaveValue("trigger.kind");
  });
});
