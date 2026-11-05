import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { InlinePresetPicker, PresetList } from "../../workflow/InlinePresetPicker";
import type { Preset } from "../../../hooks/preset-queries";

const presets = [{ name: "reviewer", description: "Review changes", source: "builtin", tag: "agent", params: { mode: "plan" } }] as unknown as Preset[];

// The inline picker's dropdown and the ⊕ sheet's preset page render the same
// list, so both reach the same choices with the same selection semantics.
describe("preset parity", () => {
  it("inline picker keeps group identity and selection semantics", async () => {
    const onChange = vi.fn();
    render(<InlinePresetPicker groupLabel="review" presets={presets} onChange={onChange} />);
    await userEvent.click(screen.getByRole("button", { name: "review" }));
    await userEvent.click(screen.getByRole("button", { name: /reviewer Review changes/ }));
    expect(onChange).toHaveBeenCalledWith(presets[0]);
  });
  it("the sheet's list selects the same preset, and None clears", async () => {
    const onSelect = vi.fn();
    render(<PresetList presets={presets} value="reviewer" onSelect={onSelect} />);
    await userEvent.click(screen.getByRole("button", { name: /reviewer Review changes/ }));
    expect(onSelect).toHaveBeenLastCalledWith(presets[0]);
    await userEvent.click(screen.getByRole("button", { name: /None/ }));
    expect(onSelect).toHaveBeenLastCalledWith(null);
  });
  it.each([{ disabled: true }, { isLoading: true }, { isStreaming: true }])("respects interaction guards %o", (guard) => {
    render(<InlinePresetPicker presets={presets} {...guard} />);
    expect(screen.getByRole("button")).toBeDisabled();
  });
  it("requires confirmation before replacing a preset expression", async () => {
    render(<InlinePresetPicker presets={presets} value="{{ inputs.preset }}" />);
    await userEvent.click(screen.getByRole("button"));
    expect(screen.getAllByText("Replace expression?").length).toBeGreaterThan(0);
    expect(screen.queryByRole("button", { name: /reviewer Review changes/ })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Replace" }));
    expect(screen.getByRole("button", { name: /reviewer Review changes/ })).toBeInTheDocument();
  });
});
