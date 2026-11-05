/**
 * The execution-mode pill does not offer "auto". It stays the parameter's
 * default (an untouched pill reads "Default" and sends nothing new), and
 * choosing the active mode again returns to it.
 */
import { describe, it, expect, vi } from "vitest";
import React from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { createInput } from "../../../lib/inputHelpers";

vi.mock("../../../store/globalDataStore", () => ({ useModels: () => ({ models: [], loading: false }) }));

import { InlineParamInput } from "../InlineParamInput";

const mode = (values: string[]) => createInput("enum", { enumValues: values, default: "auto" });

function renderPill(value: unknown, values = ["manual", "auto", "plan"]) {
  const onChange = vi.fn();
  render(React.createElement(InlineParamInput, { name: "mode", schema: mode(values), value, onChange }));
  return { pill: screen.getByRole("button"), onChange };
}

describe("InlineParamInput execution mode", () => {
  it("lists manual and plan, not auto", async () => {
    const { pill } = renderPill("auto");
    expect(pill).toHaveTextContent("Default");
    await userEvent.click(pill);
    expect(screen.getByRole("button", { name: "Manual" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Plan" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Auto" })).toBeNull();
  });

  it("sends the picked mode", async () => {
    const first = renderPill("auto");
    await userEvent.click(first.pill);
    await userEvent.click(screen.getByRole("button", { name: "Plan" }));
    expect(first.onChange).toHaveBeenLastCalledWith("plan");
  });

  it("resets to the parameter's default when the active mode is picked again", async () => {
    const { pill, onChange } = renderPill("plan");
    expect(pill).toHaveTextContent("Plan");
    await userEvent.click(pill);
    await userEvent.click(screen.getAllByRole("button", { name: "Plan" }).at(-1)!);
    // A reset, not the literal "auto": the host drops the key.
    expect(onChange).toHaveBeenLastCalledWith(undefined);
  });

  it("still lists auto in an enum that is not an execution mode", async () => {
    const onChange = vi.fn();
    render(React.createElement(InlineParamInput, { name: "layout", schema: mode(["auto", "fixed"]), value: "fixed", onChange }));
    await userEvent.click(screen.getByRole("button"));
    expect(screen.getByRole("button", { name: "Auto" })).toBeInTheDocument();
  });
});
