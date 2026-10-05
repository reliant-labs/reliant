import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ModelPreferences } from "../ModelPreferences";

const mocks = vi.hoisted(() => ({
  saved: [] as Array<[string, string]>,
  stored: {} as Record<string, string>,
  models: [
    {
      id: "claude-5-opus@anthropic",
      name: "Claude 5 Opus",
      provider: "Anthropic",
      driverId: "anthropic",
      supportsTemperature: true,
      capabilities: [],
      tags: ["flagship"],
    },
    {
      id: "qwen3:latest@local",
      name: "qwen3:latest",
      provider: "Local",
      driverId: "local",
      supportsTemperature: true,
      capabilities: [],
      tags: [],
      local: { daemonId: "gpu", machineName: "GPU box", online: true },
    },
    {
      id: "llama3@local",
      name: "llama3",
      provider: "Local",
      driverId: "local",
      supportsTemperature: true,
      capabilities: [],
      tags: [],
      local: { daemonId: "laptop", machineName: "MacBook", online: false },
    },
  ],
}));

vi.mock("../../../store/globalDataStore", () => ({
  useModels: () => ({ models: mocks.models, tiers: {}, loading: false, error: null }),
}));
vi.mock("../../../lib/settingsPersistence", () => ({
  readSetting: async (key: string) =>
    mocks.stored[key] ? { status: "found", value: mocks.stored[key] } : { status: "missing" },
  upsertStringSetting: async (key: string, value: string) => {
    mocks.saved.push([key, value]);
  },
  deleteSettingIfExists: async () => {},
}));

const providers = [{ provider: "anthropic", hasApiKey: true }];

async function expand(tag: RegExp) {
  render(<ModelPreferences providers={providers} />);
  fireEvent.click(await screen.findByText(tag));
  return (await screen.findAllByRole("combobox"))[0] as HTMLSelectElement;
}

describe("ModelPreferences local models", () => {
  beforeEach(() => {
    mocks.saved.length = 0;
    mocks.stored = {};
  });

  it("lists local models grouped by machine, offline ones disabled", async () => {
    const select = await expand(/^flagship$/i);
    const groups = Array.from(select.querySelectorAll("optgroup")).map((g) => g.label);
    expect(groups).toContain("Local · GPU box");
    expect(groups).toContain("Local · MacBook");
    const offline = Array.from(select.options).find((o) => o.textContent?.includes("llama3"));
    expect(offline?.disabled).toBe(true);
  });

  it("saves model_id plus providers when a local model is chosen", async () => {
    const select = await expand(/^flagship$/i);
    fireEvent.change(select, { target: { value: "local:gpu||qwen3:latest@local" } });
    await waitFor(() => expect(mocks.saved.length).toBe(1));
    expect(mocks.saved[0][0]).toBe("model.tag_config.flagship");
    expect(JSON.parse(mocks.saved[0][1])).toEqual({
      model_id: "qwen3:latest@local",
      providers: ["local:gpu"],
    });
  });

  it("choosing a remote model clears providers", async () => {
    mocks.stored["model.tag_config.flagship"] = JSON.stringify({
      model_id: "qwen3:latest@local",
      providers: ["local:gpu"],
    });
    const select = await expand(/^flagship$/i);
    fireEvent.change(select, { target: { value: "claude-5-opus@anthropic" } });
    await waitFor(() => expect(mocks.saved.length).toBe(1));
    expect(JSON.parse(mocks.saved[0][1])).toEqual({ model_id: "claude-5-opus@anthropic" });
  });

  it("shows the offline note for a pinned machine that is offline", async () => {
    mocks.stored["model.tag_config.flagship"] = JSON.stringify({
      model_id: "llama3@local",
      providers: ["local:laptop"],
    });
    await expand(/^flagship$/i);
    const note = await screen.findByTestId("local-offline-note");
    expect(note.textContent).toContain("offline — using the default for this tier");
  });

  it("shows no note for an online pinned machine", async () => {
    mocks.stored["model.tag_config.flagship"] = JSON.stringify({
      model_id: "qwen3:latest@local",
      providers: ["local:gpu"],
    });
    await expand(/^flagship$/i);
    expect(screen.queryByTestId("local-offline-note")).toBeNull();
  });
});
