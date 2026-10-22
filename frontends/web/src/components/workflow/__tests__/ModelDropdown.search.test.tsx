/**
 * The model picker lists every configured model across every provider — dozens
 * once a few keys are added — so it can be searched, by name, id or provider.
 */
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

const models = [
  { id: "claude-opus-5-5", name: "Claude Opus 5.5", provider: "Anthropic" },
  { id: "claude-sonnet-5", name: "Claude Sonnet 5", provider: "Anthropic" },
  { id: "gpt-5", name: "GPT-5", provider: "OpenAI" },
];

vi.mock("../../../store/globalDataStore", () => ({
  useModels: () => ({ models, loading: false }),
  useGlobalDataStore: (selector: (state: unknown) => unknown) => selector({ isInitialized: true, isPrefetching: false }),
}));

import { ModelDropdown } from "../ModelDropdown";
import { hasOpenEscapeLayer } from "../../../hooks/useEscapeLayer";

function openDropdown(onChange = vi.fn()) {
  const user = userEvent.setup();
  render(<ModelDropdown value={undefined} onChange={onChange} placeholder="Select model..." />);
  return { user, onChange, open: () => user.click(screen.getByRole("button", { name: /Select model/ })) };
}

describe("ModelDropdown search", () => {
  it("narrows the list by name, id or provider", async () => {
    const { user, open } = openDropdown();
    await open();
    const search = screen.getByRole("searchbox", { name: "Search models" });
    expect(search).toHaveFocus();

    await user.type(search, "sonnet");
    expect(screen.getByText("Claude Sonnet 5")).toBeInTheDocument();
    expect(screen.queryByText("Claude Opus 5.5")).not.toBeInTheDocument();
    expect(screen.queryByText("GPT-5")).not.toBeInTheDocument();

    await user.clear(search);
    await user.type(search, "openai");
    expect(screen.getByText("GPT-5")).toBeInTheDocument();
    expect(screen.queryByText("Claude Sonnet 5")).not.toBeInTheDocument();
  });

  it("says when no configured model matches", async () => {
    const { user, open } = openDropdown();
    await open();
    await user.type(screen.getByRole("searchbox", { name: "Search models" }), "llama");
    expect(screen.getByText("No configured model matches “llama”.")).toBeInTheDocument();
  });

  it("picks a model found by search", async () => {
    const { user, open, onChange } = openDropdown();
    await open();
    await user.type(screen.getByRole("searchbox", { name: "Search models" }), "gpt");
    await user.click(screen.getByText("GPT-5"));
    expect(onChange).toHaveBeenCalledWith({ id: "gpt-5" });
  });

  // The builder leaves Escape to an open escape layer (useEscapeLayer), so
  // closing the list never also closes the panel behind it.
  it("closes on Escape as an escape layer, from the search box", async () => {
    const user = userEvent.setup();
    render(<ModelDropdown value={undefined} onChange={vi.fn()} placeholder="Select model..." />);
    await user.click(screen.getByRole("button", { name: /Select model/ }));
    expect(hasOpenEscapeLayer()).toBe(true);
    fireEvent.keyDown(screen.getByRole("searchbox", { name: "Search models" }), { key: "Escape" });
    expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
    expect(hasOpenEscapeLayer()).toBe(false);
  });
});
