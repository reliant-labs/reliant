/**
 * Tool tags are namespaced: the coding agent's starting set is
 * `tag:coding:default`, which is what the built-in workflows declare as their
 * tools default. The selector's quick pick offered a bare `tag:default` that
 * no tool carries, and its custom-token box rejected any tag with a second
 * colon, so the default a workflow ships with could not be added back by hand.
 */
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";

import { setTransport } from "../../../lib/connect";
import { mockTransport } from "../../../lib/test-utils";
import { renderWithQuery } from "../../../test/renderWithQuery";
import userEvent from "@testing-library/user-event";

// ListTools is answered at the transport (ToolsSelector reads the generated hook).
setTransport(
  mockTransport({
    "CatalogService.ListTools": { response: { tools: [{ name: "view", description: "Read a file", category: "File" }] } },
  }),
);

import { ToolsSelector } from "../ToolsSelector";

function Selector({ onValue }: { onValue: (tools: string[]) => void }) {
  const [value, setValue] = useState<string[]>([]);
  return (
    <ToolsSelector
      value={value}
      onChange={(next) => {
        setValue(next);
        onValue(next);
      }}
    />
  );
}

async function openSelector() {
  const user = userEvent.setup();
  const onValue = vi.fn();
  renderWithQuery(<Selector onValue={onValue} />);
  await user.click(await screen.findByRole("button", { name: /token\(s\) selected/ }));
  return { user, onValue };
}

describe("ToolsSelector tags", () => {
  it("offers the coding agent's starting set as a quick pick", async () => {
    const { user, onValue } = await openSelector();
    expect(screen.queryByRole("button", { name: /^tag:default$/ })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "The coding agent's starting tool set" }));
    expect(onValue).toHaveBeenLastCalledWith(["tag:coding:default"]);
  });

  it("accepts a namespaced tag as a custom token", async () => {
    const { user, onValue } = await openSelector();
    await user.type(screen.getByPlaceholderText(/Add custom token/), "tag:coding:plan{Enter}");
    expect(screen.queryByText(/Invalid token/)).not.toBeInTheDocument();
    expect(onValue).toHaveBeenLastCalledWith(["tag:coding:plan"]);
  });

  it("still rejects a malformed tag", async () => {
    const { user, onValue } = await openSelector();
    await user.type(screen.getByPlaceholderText(/Add custom token/), "tag:coding:{Enter}");
    expect(screen.getByText(/Invalid token/)).toBeInTheDocument();
    expect(onValue).not.toHaveBeenCalled();
  });
});
