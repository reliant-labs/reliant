import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ModelPreferences } from "../ModelPreferences";

const mocks = vi.hoisted(() => ({
  models: [
    {
      id: "claude-5-opus@anthropic",
      name: "Claude 5 Opus",
      provider: "Anthropic",
      driverId: "anthropic",
      supportsTemperature: false,
      capabilities: [],
      tags: ["flagship"],
    },
    {
      id: "gemini-2.5-pro@gemini",
      name: "Gemini 2.5 Pro",
      provider: "Google",
      driverId: "gemini",
      supportsTemperature: true,
      capabilities: [],
      tags: ["fast"],
    },
  ],
}));

vi.mock("../../../store/globalDataStore", () => ({
  useModels: () => ({ models: mocks.models, tiers: {}, loading: false, error: null }),
}));
vi.mock("../../../lib/settingsPersistence", () => ({
  readSetting: async () => ({ status: "missing" }),
  upsertStringSetting: async () => {},
  deleteSettingIfExists: async () => {},
}));

async function expandTag(label: RegExp) {
  render(
    <ModelPreferences
      providers={[
        { provider: "anthropic", hasApiKey: true },
        { provider: "gemini", hasApiKey: true },
      ]}
    />,
  );
  fireEvent.click(await screen.findByText(label));
}

describe("ModelPreferences temperature", () => {
  it("hides the temperature row for a tier resolving to a model that ignores it", async () => {
    await expandTag(/flagship/i);
    await waitFor(() => expect(screen.getByText("Thinking")).toBeTruthy());
    expect(screen.queryByTestId("temperature-control")).toBeNull();
  });

  it("shows it, labelled Default, when honored", async () => {
    await expandTag(/^fast$/i);
    const control = await screen.findByTestId("temperature-control");
    expect(control.textContent).toContain("Default");
  });
});
