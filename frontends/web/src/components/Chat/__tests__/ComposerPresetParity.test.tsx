import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { InlinePresetPicker } from "../../workflow/InlinePresetPicker";
import type { Preset } from "../../../hooks/preset-queries";

const presets = [{ name: "reviewer", description: "Review changes", source: "builtin", tag: "agent", params: { mode: "plan" } }] as unknown as Preset[];
describe.each([false, true])("preset parity mobile=%s", (mobile) => {
  it("keeps group identity and selection semantics", async () => {
    const onChange = vi.fn();
    render(<InlinePresetPicker mobile={mobile} groupLabel="review" presets={presets} onChange={onChange} />);
    await userEvent.click(screen.getByRole("button", { name: "review" }));
    await userEvent.click(screen.getByRole("button", { name: /reviewer Review changes/ }));
    expect(onChange).toHaveBeenCalledWith(presets[0]);
  });
  it.each([{ disabled: true }, { isLoading: true }, { isStreaming: true }])("respects interaction guards %o", (guard) => {
    render(<InlinePresetPicker mobile={mobile} presets={presets} {...guard} />);
    expect(screen.getByRole("button")).toBeDisabled();
  });
  it("requires confirmation before replacing a preset expression", async () => {
    render(<InlinePresetPicker mobile={mobile} presets={presets} value="{{ inputs.preset }}" />);
    await userEvent.click(screen.getByRole("button"));
    expect(screen.getAllByText("Replace expression?").length).toBeGreaterThan(0);
    expect(screen.queryByRole("button", { name: /reviewer Review changes/ })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Replace" }));
    expect(screen.getByRole("button", { name: /reviewer Review changes/ })).toBeInTheDocument();
  });
});
