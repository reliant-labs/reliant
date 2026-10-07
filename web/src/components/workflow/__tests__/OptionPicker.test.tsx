/**
 * The picker for reference-shaped fields (Run Tool's tool, a workflow step's
 * ref): pick what exists, with what each one does, instead of typing a name
 * from memory. The list never walls the author in: "Enter manually" takes any
 * name, and a value the list does not know opens as text rather than vanishing.
 */
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { OptionPicker, type PickerOption } from "../OptionPicker";
import { hasOpenEscapeLayer } from "../../../hooks/useEscapeLayer";

const tools: PickerOption[] = [
  { value: "view", label: "view", description: "Read a file with line numbers.", group: "Files" },
  { value: "edit", label: "edit", description: "Replace text in a file.", group: "Files" },
  { value: "websearch", label: "Web search", description: "Search the web.", group: "Web" },
];

function Picker({ initial = "", onValue }: { initial?: string; onValue?: (value: string) => void }) {
  const [value, setValue] = useState(initial);
  return (
    <>
      <label htmlFor="tool">Tool</label>
      <OptionPicker
        id="tool"
        value={value}
        onChange={(next) => {
          setValue(next);
          onValue?.(next);
        }}
        options={tools}
        placeholder="Select a tool…"
        searchPlaceholder="Search tools"
        manualPlaceholder="tool_name"
      />
    </>
  );
}

describe("OptionPicker", () => {
  it("lists the options under their groups, with what each one does", async () => {
    const user = userEvent.setup();
    render(<Picker />);
    await user.click(screen.getByRole("button", { name: "Tool" }));

    const files = screen.getByRole("group", { name: "Files" });
    expect(within(files).getAllByRole("option").map((o) => o.textContent)).toEqual([
      "viewRead a file with line numbers.",
      "editReplace text in a file.",
    ]);
    // A label that differs from the value shows the value it stores too.
    const web = screen.getByRole("group", { name: "Web" });
    expect(within(web).getByRole("option")).toHaveTextContent("Web searchwebsearchSearch the web.");
  });

  it("narrows by search (name or description) and picks with the keyboard", async () => {
    const user = userEvent.setup();
    const onValue = vi.fn();
    render(<Picker onValue={onValue} />);
    await user.click(screen.getByRole("button", { name: "Tool" }));

    const search = screen.getByRole("combobox", { name: "Search tools" });
    expect(search).toHaveFocus();
    await user.type(search, "replace");
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["editReplace text in a file.", "Enter manually"]);

    // Focus stays in the search box; the highlighted row is announced.
    const highlighted = search.getAttribute("aria-activedescendant");
    expect(document.getElementById(highlighted!)).toHaveTextContent("edit");
    await user.keyboard("{Enter}");
    expect(onValue).toHaveBeenLastCalledWith("edit");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Tool" })).toHaveTextContent("edit");
    expect(screen.getByRole("button", { name: "Tool" })).toHaveFocus();
  });

  it("moves the highlight with the arrow keys", async () => {
    const user = userEvent.setup();
    const onValue = vi.fn();
    render(<Picker onValue={onValue} />);
    await user.click(screen.getByRole("button", { name: "Tool" }));
    await user.keyboard("{ArrowDown}{ArrowDown}{ArrowUp}{Enter}");
    expect(onValue).toHaveBeenLastCalledWith("edit");
  });

  it("says when nothing matches, and still offers manual entry", async () => {
    const user = userEvent.setup();
    render(<Picker />);
    await user.click(screen.getByRole("button", { name: "Tool" }));
    await user.type(screen.getByRole("combobox"), "zzz");
    expect(screen.getByText("Nothing matches “zzz”.")).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Enter manually" })).toBeInTheDocument();
  });

  it("closes on Escape without letting it reach the builder", async () => {
    const user = userEvent.setup();
    const outer = vi.fn();
    render(
      <div onKeyDown={outer}>
        <Picker />
      </div>,
    );
    await user.click(screen.getByRole("button", { name: "Tool" }));
    // The builder's capture-phase Escape handler stands aside for an open layer.
    expect(hasOpenEscapeLayer()).toBe(true);
    fireEvent.keyDown(screen.getByRole("combobox"), { key: "Escape" });
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(outer).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Tool" })).toHaveFocus();
  });

  it("takes a name the list does not know through Enter manually", async () => {
    const user = userEvent.setup();
    const onValue = vi.fn();
    render(<Picker onValue={onValue} />);
    await user.click(screen.getByRole("button", { name: "Tool" }));
    await user.click(screen.getByRole("option", { name: "Enter manually" }));

    const text = screen.getByRole("textbox", { name: "Tool" });
    expect(text).toHaveAttribute("placeholder", "tool_name");
    await user.type(text, "my_tool");
    expect(onValue).toHaveBeenLastCalledWith("my_tool");

    // And back to the list.
    await user.click(screen.getByRole("button", { name: "Pick from list" }));
    expect(screen.getByRole("listbox")).toBeInTheDocument();
  });

  it("opens a value the list does not know as text, not as an empty picker", () => {
    render(<Picker initial="{{ inputs.tool }}" />);
    expect(screen.getByRole("textbox", { name: "Tool" })).toHaveValue("{{ inputs.tool }}");
  });

  it("shows the picked option's label, and marks it selected in the list", async () => {
    const user = userEvent.setup();
    render(<Picker initial="websearch" />);
    const button = screen.getByRole("button", { name: "Tool" });
    expect(button).toHaveTextContent("Web search");
    await user.click(button);
    expect(screen.getByRole("option", { name: /Web search/ })).toHaveAttribute("aria-selected", "true");
  });
});

describe("OptionPicker placement", () => {
  // The last field of a config panel sits on the panel's clipped bottom edge;
  // a list that always opened downward opened out of sight.
  it("opens upward when there is not room below it", async () => {
    const user = userEvent.setup();
    const rect = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
      // The picker sits 40px above the bottom of a 768px window.
      return { top: 700, bottom: 728, left: 0, right: 300, width: 300, height: 28, x: 0, y: 700, toJSON: () => ({}) } as DOMRect;
    });
    try {
      render(<Picker />);
      await user.click(screen.getByRole("button", { name: "Tool" }));
      expect(screen.getByRole("listbox").parentElement).toHaveAttribute("data-placement", "top");
    } finally {
      rect.mockRestore();
    }
  });

  it("opens downward when there is room", async () => {
    const user = userEvent.setup();
    render(<Picker />);
    await user.click(screen.getByRole("button", { name: "Tool" }));
    expect(screen.getByRole("listbox").parentElement).toHaveAttribute("data-placement", "bottom");
  });
});
