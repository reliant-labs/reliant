import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ModelSettingsPage } from "../ModelSettingsPage";

const mocks = vi.hoisted(() => ({
  models: [
    {
      id: "claude-5-opus@anthropic",
      name: "Claude 5 Opus",
      provider: "Anthropic",
      driverId: "anthropic",
      contextWindow: 1_000_000,
      capabilities: [],
      tags: ["flagship"],
    },
    {
      id: "gpt-5.6-terra@codex",
      name: "GPT-5.6 Terra",
      provider: "Codex",
      driverId: "codex",
      contextWindow: 272_000,
      capabilities: [],
      tags: ["moderate"],
    },
  ],
  tiers: {
    flagship: { modelId: "claude-5-opus@anthropic", thinkingLevel: "" },
    moderate: { modelId: "gpt-5.6-terra@codex", thinkingLevel: "" },
  },
}));

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));
vi.mock("../../../../store/globalDataStore", () => ({
  useModels: () => ({ models: mocks.models, tiers: mocks.tiers, loading: false, error: null }),
}));

function renderPage(value: unknown) {
  const onChange = vi.fn();
  render(<ModelSettingsPage value={value} onChange={onChange} onBack={() => {}} onClose={() => {}} />);
  return onChange;
}

const tagButton = (tag: string) => screen.getByRole("button", { name: new RegExp(`^${tag}`) });

// Prod incident 2026-10-09: a 1M compaction_threshold set on one selection
// rode a switch onto a tag that resolved to a 272k-window model. Compaction
// could never fire, and the trim backstop shredded the conversation instead.
describe("ModelSettingsPage compaction_threshold", () => {
  it("drops the threshold when switching to another tag, keeping portable overrides", async () => {
    const onChange = renderPage({ tags: ["flagship"], compaction_threshold: 1_000_000, temperature: 0.4 });
    await userEvent.click(tagButton("moderate"));
    expect(onChange).toHaveBeenCalledWith({ tags: ["moderate"], temperature: 0.4 });
  });

  it("drops the threshold when switching to another model", async () => {
    const onChange = renderPage({ id: "claude-5-opus@anthropic", compaction_threshold: 1_000_000 });
    await userEvent.click(screen.getByRole("button", { name: /GPT-5.6 Terra/ }));
    expect(onChange).toHaveBeenCalledWith({ id: "gpt-5.6-terra@codex" });
  });

  it("drops the threshold when switching from an explicit model to a tag", async () => {
    const onChange = renderPage({ id: "claude-5-opus@anthropic", compaction_threshold: 900_000 });
    await userEvent.click(screen.getByRole("button", { name: "By Tag" }));
    await userEvent.click(tagButton("flagship"));
    expect(onChange).toHaveBeenCalledWith({ tags: ["flagship"] });
  });

  it("keeps the threshold when the same tag is selected again", async () => {
    const onChange = renderPage({ tags: ["moderate"], compaction_threshold: 150_000 });
    await userEvent.click(tagButton("moderate"));
    expect(onChange).toHaveBeenCalledWith({ tags: ["moderate"], compaction_threshold: 150_000 });
  });

  it("keeps the threshold when the same model is selected again", async () => {
    const onChange = renderPage({ id: "gpt-5.6-terra@codex", compaction_threshold: 150_000 });
    await userEvent.click(screen.getByRole("button", { name: /GPT-5.6 Terra/ }));
    expect(onChange).toHaveBeenCalledWith({ id: "gpt-5.6-terra@codex", compaction_threshold: 150_000 });
  });
});
