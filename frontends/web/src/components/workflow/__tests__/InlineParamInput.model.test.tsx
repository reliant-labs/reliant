/**
 * A `model` input can sit in the chat toolbar (#624): its pill is a model
 * picker, listing the same catalog as the composer's own model picker, and
 * what it reports is a model selector (`{ id }`), the shape the workflow
 * reads. It used to fall through to a label-only pill that did nothing.
 */
import { describe, it, expect, vi } from "vitest";
import React from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { createInput, type InputDef } from "../../../lib/inputHelpers";

vi.mock("../../../store/globalDataStore", () => ({
  useModels: () => ({
    models: [
      { id: "claude-sonnet-5@anthropic", name: "Sonnet 5", provider: "Anthropic", driverId: "anthropic" },
      { id: "gpt-5@openai", name: "GPT-5", provider: "OpenAI", driverId: "openai" },
    ],
    loading: false,
  }),
}));

import { InlineParamInput } from "../InlineParamInput";

function renderPill(value: unknown, schema: InputDef = createInput("model") as InputDef) {
  const onChange = vi.fn();
  render(React.createElement(InlineParamInput, { name: "reviewer_model", schema, value, onChange }));
  return { pill: screen.getByRole("button"), onChange };
}

describe("InlineParamInput model", () => {
  it("picks a model from the catalog and reports a model selector", async () => {
    const { pill, onChange } = renderPill(undefined);
    expect(pill).toHaveTextContent(/^Reviewer Model:\s*required$/);

    await userEvent.click(pill);
    await userEvent.click(screen.getByRole("option", { name: /GPT-5/ }));

    expect(onChange).toHaveBeenCalledWith({ id: "gpt-5@openai" });
  });

  it("shows the chosen model by name, matching a bare model id too", () => {
    const { pill } = renderPill({ id: "claude-sonnet-5" });
    expect(pill).toHaveTextContent(/^Reviewer Model:\s*Sonnet 5$/);
  });

  it("names a tier selector rather than calling it required", () => {
    const { pill } = renderPill({ tags: ["fast"] });
    expect(pill).toHaveTextContent(/^Reviewer Model:\s*Tier: fast$/);
  });

  it("does not open while the chat is streaming", async () => {
    const onChange = vi.fn();
    render(
      React.createElement(InlineParamInput, {
        name: "reviewer_model",
        schema: createInput("model") as InputDef,
        value: undefined,
        onChange,
        isStreaming: true,
      }),
    );
    await userEvent.click(screen.getByRole("button"));
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });
});
