import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { configChoiceValue, modelChoiceValue, ModelPreferences, parseModelChoice } from "../ModelPreferences";
import { endpointProviderRef, isCustomEndpointModel, localSourceGroupLabel, pinnedRefOf, pinRefFor } from "../localModels";

const mocks = vi.hoisted(() => ({
  saved: [] as Array<[string, string]>,
  stored: {} as Record<string, string>,
  models: [
    { id: "claude-5-opus@anthropic", name: "Claude 5 Opus", provider: "Anthropic", driverId: "anthropic", supportsTemperature: true, capabilities: [], tags: ["flagship"] },
    {
      id: "qwen3:latest@local", name: "qwen3:latest", provider: "Local", driverId: "local", supportsTemperature: true, capabilities: [], tags: [],
      local: { daemonId: "gpu", machineName: "GPU box", endpointId: "ollama", endpointKind: "ollama", online: true },
    },
    {
      id: "llama@local", name: "llama", provider: "Local", driverId: "local", supportsTemperature: true, capabilities: [], tags: [],
      local: { daemonId: "", machineName: "Lab GPU", endpointId: "ep-1", endpointKind: "vllm", online: true },
    },
    {
      id: "mixtral@local", name: "mixtral", provider: "Local", driverId: "local", supportsTemperature: true, capabilities: [], tags: [],
      local: { daemonId: "", machineName: "Home vLLM", endpointId: "ep-2", endpointKind: "vllm", online: false },
    },
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

async function expand(tag: RegExp) {
  render(<ModelPreferences providers={providers} />);
  fireEvent.click(await screen.findByText(tag));
  return (await screen.findAllByRole("combobox"))[0] as HTMLSelectElement;
}

describe("endpoint pin encoding", () => {
  it("round-trips an endpoint model through the chooser value", () => {
    const m = mocks.models[2];
    const value = modelChoiceValue(m);
    expect(value).toBe("endpoint:ep-1||llama@local");
    expect(parseModelChoice(value)).toEqual({ model_id: "llama@local", providers: ["endpoint:ep-1"] });
    expect(configChoiceValue({ model_id: "llama@local", providers: ["endpoint:ep-1"] })).toBe(value);
  });
  it("still encodes a daemon-detected model as local:<daemon>", () => {
    const value = modelChoiceValue(mocks.models[1]);
    expect(value).toBe("local:gpu||qwen3:latest@local");
    expect(parseModelChoice(value)).toEqual({ model_id: "qwen3:latest@local", providers: ["local:gpu"] });
  });
  it("treats a plain id as unpinned", () => {
    expect(parseModelChoice("claude-5-opus@anthropic")).toEqual({ model_id: "claude-5-opus@anthropic", providers: undefined });
    expect(parseModelChoice("")).toEqual({ model_id: undefined, providers: undefined });
  });
  it("helpers distinguish the two sources", () => {
    expect(isCustomEndpointModel({ daemonId: "" })).toBe(true);
    expect(isCustomEndpointModel({ daemonId: "gpu" })).toBe(false);
    expect(endpointProviderRef("e")).toBe("endpoint:e");
    expect(pinRefFor({ daemonId: "", machineName: "x", endpointId: "e" })).toBe("endpoint:e");
    expect(localSourceGroupLabel({ daemonId: "", machineName: "Lab GPU", endpointId: "e" })).toBe("Lab GPU");
    expect(localSourceGroupLabel({ daemonId: "d", machineName: "Box", endpointId: "ollama" })).toBe("Local · Box");
    expect(pinnedRefOf(["x", "endpoint:e"])).toBe("endpoint:e");
    expect(pinnedRefOf(undefined)).toBeUndefined();
  });
});

describe("ModelPreferences custom endpoints", () => {
  beforeEach(() => {
    mocks.saved.length = 0;
    mocks.stored = {};
  });

  it("lists endpoint models grouped by the endpoint's name, offline ones disabled", async () => {
    const select = await expand(/^flagship$/i);
    const groups = Array.from(select.querySelectorAll("optgroup")).map((g) => g.label);
    expect(groups).toContain("Lab GPU");
    expect(groups).toContain("Home vLLM");
    expect(groups).toContain("Local · GPU box");
    const offline = Array.from(select.options).find((o) => o.textContent?.includes("mixtral"));
    expect(offline?.disabled).toBe(true);
  });

  it("saves model_id plus providers: [endpoint:<id>]", async () => {
    const select = await expand(/^flagship$/i);
    fireEvent.change(select, { target: { value: "endpoint:ep-1||llama@local" } });
    await waitFor(() => expect(mocks.saved.length).toBe(1));
    expect(mocks.saved[0][0]).toBe("model.tag_config.flagship");
    expect(JSON.parse(mocks.saved[0][1])).toEqual({ model_id: "llama@local", providers: ["endpoint:ep-1"] });
  });

  it("selects the saved endpoint pin and shows no offline note while it is online", async () => {
    mocks.stored["model.tag_config.flagship"] = JSON.stringify({ model_id: "llama@local", providers: ["endpoint:ep-1"] });
    const select = await expand(/^flagship$/i);
    expect(select.value).toBe("endpoint:ep-1||llama@local");
    expect(screen.queryByTestId("local-offline-note")).toBeNull();
  });

  it("an endpoint pin never matches a detected model of the same name", async () => {
    mocks.stored["model.tag_config.flagship"] = JSON.stringify({ model_id: "qwen3:latest@local", providers: ["endpoint:ep-1"] });
    await expand(/^flagship$/i);
    expect((await screen.findByTestId("local-offline-note")).textContent).toContain("That custom endpoint is unavailable");
  });

  it("notes an offline endpoint by name and a deleted one generically", async () => {
    mocks.stored["model.tag_config.flagship"] = JSON.stringify({ model_id: "mixtral@local", providers: ["endpoint:ep-2"] });
    await expand(/^flagship$/i);
    expect((await screen.findByTestId("local-offline-note")).textContent).toContain("Home vLLM is offline — using the default for this tier");
  });

  it("choosing a remote model clears an endpoint pin", async () => {
    mocks.stored["model.tag_config.flagship"] = JSON.stringify({ model_id: "llama@local", providers: ["endpoint:ep-1"] });
    const select = await expand(/^flagship$/i);
    fireEvent.change(select, { target: { value: "claude-5-opus@anthropic" } });
    await waitFor(() => expect(mocks.saved.length).toBe(1));
    expect(JSON.parse(mocks.saved[0][1])).toEqual({ model_id: "claude-5-opus@anthropic" });
  });
});
