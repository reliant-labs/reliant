import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ModelSettingsPage } from "../ModelSettingsPage";

const mocks = vi.hoisted(() => ({
  models: [
    {
      id: "claude-5-opus@anthropic",
      name: "Claude 5 Opus",
      provider: "Anthropic",
      driverId: "anthropic",
      canReason: true,
      supportedThinkingLevels: ["low", "high"],
      supportsTemperature: false,
      capabilities: [],
      tags: ["flagship"],
    },
    {
      id: "gemini-2.5-pro@gemini",
      name: "Gemini 2.5 Pro",
      provider: "Google",
      driverId: "gemini",
      canReason: true,
      supportedThinkingLevels: ["low", "high"],
      supportsTemperature: true,
      capabilities: [],
      tags: ["fast"],
    },
  ],
  tiers: {
    flagship: { modelId: "claude-5-opus@anthropic", thinkingLevel: "high" },
    fast: { modelId: "gemini-2.5-pro@gemini", thinkingLevel: "low" },
  },
}));

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));
vi.mock("../../../../store/globalDataStore", () => ({
  useModels: () => ({ models: mocks.models, tiers: mocks.tiers, loading: false, error: null }),
}));

function renderPage(value: unknown) {
  return render(<ModelSettingsPage value={value} onChange={() => {}} onBack={() => {}} onClose={() => {}} />);
}

describe("ModelSettingsPage temperature", () => {
  it("hides the temperature control for an explicit model that ignores it", () => {
    renderPage({ id: "claude-5-opus@anthropic" });
    expect(screen.queryByTestId("temperature-control")).toBeNull();
  });

  it("hides it when the tag resolves to a model that ignores it", () => {
    renderPage({ tags: ["flagship"] });
    expect(screen.queryByTestId("temperature-control")).toBeNull();
  });

  it("shows it, labelled Default rather than 1.0, when honored and unset", () => {
    renderPage({ id: "gemini-2.5-pro@gemini" });
    const control = screen.getByTestId("temperature-control");
    expect(control.textContent).toContain("Default");
    expect(control.textContent).not.toContain("1.0");
  });
});
