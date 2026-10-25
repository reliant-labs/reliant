/**
 * forge's combobox, as this app relies on it: what ui/SearchableDropdown and
 * ui/MultiSelectDropdown did (search on label/value/description, groups, a
 * clearable single choice, a multi-select with a count and Clear, empty and
 * loading states) plus the WAI-ARIA combobox contract R5 gained
 * (research/forge-migration/13-p9-frontend-plan.md §6): a named trigger in a
 * FormField, aria-activedescendant, the keyboard, focus kept in the search.
 */
import { describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";

import Combobox, { type ComboboxOption } from "../combobox";
import { FormField } from "../form";
import Label from "../label";

const models: ComboboxOption[] = [
  { value: "sonnet", label: "Claude Sonnet", description: "Fast, capable", group: "Anthropic" },
  { value: "opus", label: "Claude Opus", description: "Most capable", group: "Anthropic" },
  { value: "gpt", label: "GPT-5", group: "OpenAI", keywords: ["openai"] },
  { value: "old", label: "Legacy model", group: "OpenAI", disabled: true },
];

function key(el: Element, k: string) {
  fireEvent.keyDown(el, { key: k });
}

function Single({ onChange = vi.fn(), clearable = false, initial = null as string | null }) {
  const [value, setValue] = useState<string | null>(initial);
  return (
    <FormField>
      <Label>Model</Label>
      <Combobox
        options={models}
        value={value}
        clearable={clearable}
        placeholder="Select a model"
        onChange={(v) => {
          setValue(v);
          onChange(v);
        }}
      />
    </FormField>
  );
}

function Multi({ onChange = vi.fn() }) {
  const [value, setValue] = useState<string[]>([]);
  return (
    <Combobox
      multiple
      aria-label="Entry steps"
      options={models}
      value={value}
      onChange={(v) => {
        setValue(v);
        onChange(v);
      }}
    />
  );
}

describe("Combobox", () => {
  it("is named by its FormField label and reads its choice as a description", () => {
    render(<Single initial="opus" />);
    const trigger = screen.getByRole("button", { name: "Model" });
    expect(trigger).toHaveAccessibleDescription("Claude Opus");
    expect(trigger).toHaveAttribute("aria-haspopup", "dialog");
  });

  it("opens on a search box that owns focus and points at the chosen option", () => {
    render(<Single initial="opus" />);
    fireEvent.click(screen.getByRole("button", { name: "Model" }));
    const search = screen.getByRole("combobox", { name: "Search" });
    expect(search).toHaveFocus();
    const listbox = screen.getByRole("listbox");
    expect(search).toHaveAttribute("aria-controls", listbox.id);
    const opus = screen.getByRole("option", { name: /Claude Opus/ });
    expect(search).toHaveAttribute("aria-activedescendant", opus.id);
    expect(opus).toHaveAttribute("aria-selected", "true");
  });

  it("groups options under their headings", () => {
    render(<Single />);
    fireEvent.click(screen.getByRole("button", { name: "Model" }));
    const anthropic = screen.getByRole("group", { name: "Anthropic" });
    expect(within(anthropic).getAllByRole("option")).toHaveLength(2);
    expect(screen.getByRole("group", { name: "OpenAI" })).toBeInTheDocument();
  });

  it("filters on label, description and keywords, and says when nothing matches", () => {
    render(<Single />);
    fireEvent.click(screen.getByRole("button", { name: "Model" }));
    const search = screen.getByRole("combobox");
    fireEvent.change(search, { target: { value: "capable" } });
    expect(screen.getAllByRole("option")).toHaveLength(2);
    fireEvent.change(search, { target: { value: "openai" } });
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["GPT-5"]);
    expect(search).toHaveAttribute("aria-activedescendant", screen.getByRole("option").id);
    fireEvent.change(search, { target: { value: "zzz" } });
    expect(screen.queryAllByRole("option")).toHaveLength(0);
    expect(screen.getByText("No results")).toBeInTheDocument();
  });

  it("moves with the arrows over enabled options only, wrapping, and chooses with Enter", () => {
    const onChange = vi.fn();
    render(<Single onChange={onChange} />);
    const trigger = screen.getByRole("button", { name: "Model" });
    fireEvent.click(trigger);
    const search = screen.getByRole("combobox");
    const activeName = () => document.getElementById(search.getAttribute("aria-activedescendant")!)?.textContent;
    expect(activeName()).toMatch(/Claude Sonnet/);
    key(search, "ArrowDown");
    key(search, "ArrowDown");
    expect(activeName()).toBe("GPT-5");
    key(search, "ArrowDown"); // skips the disabled legacy model and wraps
    expect(activeName()).toMatch(/Claude Sonnet/);
    key(search, "ArrowUp");
    expect(activeName()).toBe("GPT-5");
    key(search, "Enter");
    expect(onChange).toHaveBeenCalledWith("gpt");
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(trigger).toHaveFocus();
    expect(trigger).toHaveAccessibleDescription("GPT-5");
  });

  it("chooses with a click without moving focus out of the search first", () => {
    const onChange = vi.fn();
    render(<Single onChange={onChange} />);
    fireEvent.click(screen.getByRole("button", { name: "Model" }));
    const option = screen.getByRole("option", { name: /Claude Opus/ });
    const press = new MouseEvent("mousedown", { bubbles: true, cancelable: true });
    act(() => {
      option.dispatchEvent(press);
    });
    expect(press.defaultPrevented).toBe(true);
    fireEvent.click(option);
    expect(onChange).toHaveBeenCalledWith("opus");
  });

  it("ignores a disabled option", () => {
    const onChange = vi.fn();
    render(<Single onChange={onChange} />);
    fireEvent.click(screen.getByRole("button", { name: "Model" }));
    const legacy = screen.getByRole("option", { name: "Legacy model" });
    expect(legacy).toHaveAttribute("aria-disabled", "true");
    fireEvent.click(legacy);
    expect(onChange).not.toHaveBeenCalled();
  });

  it("clears a clearable single choice from the trigger", () => {
    const onChange = vi.fn();
    render(<Single onChange={onChange} clearable initial="opus" />);
    fireEvent.click(screen.getByRole("button", { name: "Clear" }));
    expect(onChange).toHaveBeenCalledWith(null);
    expect(screen.queryByRole("button", { name: "Clear" })).toBeNull();
    expect(screen.getByRole("button", { name: "Model" })).toHaveAccessibleDescription("Select a model");
  });

  it("toggles several choices, stays open, counts them and clears them", () => {
    const onChange = vi.fn();
    render(<Multi onChange={onChange} />);
    fireEvent.click(screen.getByRole("button", { name: "Entry steps" }));
    expect(screen.getByRole("listbox")).toHaveAttribute("aria-multiselectable", "true");
    const search = screen.getByRole("combobox");
    key(search, "Enter");
    key(search, "ArrowDown");
    key(search, "Enter");
    expect(onChange).toHaveBeenLastCalledWith(["sonnet", "opus"]);
    expect(screen.getByRole("listbox")).toBeInTheDocument();
    expect(screen.getByText("2 selected")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("option", { name: /Claude Sonnet/ }));
    expect(onChange).toHaveBeenLastCalledWith(["opus"]);
    fireEvent.click(screen.getByRole("button", { name: "Clear" }));
    expect(onChange).toHaveBeenLastCalledWith([]);
  });

  it("closes on Escape and on Tab, focus back on the trigger", () => {
    render(<Single />);
    const trigger = screen.getByRole("button", { name: "Model" });
    fireEvent.click(trigger);
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    });
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(trigger).toHaveFocus();
    fireEvent.click(trigger);
    key(screen.getByRole("combobox"), "Tab");
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(trigger).toHaveFocus();
  });

  it("says when its options are loading, and does not open when disabled", () => {
    const { rerender } = render(<Combobox aria-label="Branch" options={[]} value={null} onChange={vi.fn()} loading />);
    fireEvent.click(screen.getByRole("button", { name: "Branch" }));
    expect(screen.getByRole("listbox")).toHaveAttribute("aria-busy", "true");
    expect(screen.getByText("Loading…")).toBeInTheDocument();
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    });
    rerender(<Combobox aria-label="Branch" options={models} value={null} onChange={vi.fn()} disabled />);
    const trigger = screen.getByRole("button", { name: "Branch" });
    expect(trigger).toBeDisabled();
    fireEvent.click(trigger);
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("hands a custom trigger the props, the choice and the open state", () => {
    render(
      <Combobox
        options={models}
        value="gpt"
        onChange={vi.fn()}
        aria-label="Model"
        trigger={({ selected, open, ...props }) => (
          <button type="button" {...props} data-open={open}>
            {selected[0]?.label ?? "none"}
          </button>
        )}
      />,
    );
    const trigger = screen.getByRole("button", { name: "GPT-5" });
    fireEvent.click(trigger);
    expect(trigger).toHaveAttribute("data-open", "true");
    expect(screen.getByRole("listbox")).toBeInTheDocument();
  });
});
