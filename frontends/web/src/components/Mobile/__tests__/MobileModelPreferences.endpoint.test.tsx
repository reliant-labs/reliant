import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { MobileModelPreferences } from "../MobileModelPreferences";

const mocks = vi.hoisted(() => ({
  saved: [] as Array<[string, string]>,
  stored: {} as Record<string, string>,
  models: [
    { id: "claude-5-opus@anthropic", name: "Claude 5 Opus", provider: "Anthropic", driverId: "anthropic", supportsTemperature: true, capabilities: [], tags: ["flagship"] },
    { id: "qwen3:latest@local", name: "qwen3:latest", provider: "Local", driverId: "local", supportsTemperature: true, capabilities: [], tags: [], local: { daemonId: "gpu", machineName: "GPU box", endpointId: "ollama", endpointKind: "ollama", online: true } },
    { id: "llama@local", name: "llama", provider: "Local", driverId: "local", supportsTemperature: true, capabilities: [], tags: [], local: { daemonId: "", machineName: "Lab GPU", endpointId: "ep-1", endpointKind: "vllm", online: true } },
  ],
}));

vi.mock("../../../store/globalDataStore", () => ({
  useModels: () => ({ models: mocks.models, tiers: {}, loading: false, error: null }),
}));
vi.mock("../../../lib/settingsPersistence", () => ({
  readSetting: async (key: string) => (mocks.stored[key] ? { status: "found", value: mocks.stored[key] } : { status: "missing" }),
  upsertStringSetting: async (key: string, value: string) => {
    mocks.saved.push([key, value]);
  },
  deleteSettingIfExists: async () => {},
}));

const providers = [{ provider: "anthropic", hasApiKey: true }];

describe("MobileModelPreferences custom endpoints", () => {
  beforeEach(() => {
    mocks.saved.length = 0;
    mocks.stored = {};
  });

  it("labels endpoint models by endpoint name and local ones by machine", async () => {
    render(<MobileModelPreferences providers={providers} />);
    const select = (await screen.findAllByRole("combobox"))[0] as HTMLSelectElement;
    const labels = Array.from(select.options).map((o) => o.textContent);
    expect(labels).toContain("llama (Lab GPU)");
    expect(labels).toContain("qwen3:latest (Local · GPU box)");
  });

  it("saves model_id plus providers: [endpoint:<id>]", async () => {
    render(<MobileModelPreferences providers={providers} />);
    const select = (await screen.findAllByRole("combobox"))[0] as HTMLSelectElement;
    fireEvent.change(select, { target: { value: "endpoint:ep-1||llama@local" } });
    await waitFor(() => expect(mocks.saved.length).toBe(1));
    expect(JSON.parse(mocks.saved[0][1])).toEqual({ model_id: "llama@local", providers: ["endpoint:ep-1"] });
  });

  it("shows a saved endpoint pin as selected", async () => {
    mocks.stored["model.tag_config.powerful"] = JSON.stringify({ model_id: "llama@local", providers: ["endpoint:ep-1"] });
    mocks.stored["model.tag_config.flagship"] = JSON.stringify({ model_id: "llama@local", providers: ["endpoint:ep-1"] });
    render(<MobileModelPreferences providers={providers} />);
    await waitFor(async () => {
      const selects = (await screen.findAllByRole("combobox")) as HTMLSelectElement[];
      expect(selects.some((s) => s.value === "endpoint:ep-1||llama@local")).toBe(true);
    });
  });
});
