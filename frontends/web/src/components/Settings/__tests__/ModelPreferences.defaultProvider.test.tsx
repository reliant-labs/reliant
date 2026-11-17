import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  models: [] as Array<Record<string, unknown>>,
  tiers: {} as Record<string, { modelId: string; thinkingLevel: string }>,
  saved: undefined as { status: string; value?: string } | undefined,
  refetchModels: vi.fn().mockResolvedValue(undefined),
  upsertStringSetting: vi.fn().mockResolvedValue(undefined),
  deleteSettingIfExists: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("../../../store/globalDataStore", () => ({
  useModels: () => ({ models: mocks.models, tiers: mocks.tiers, loading: false, error: null }),
  useGlobalDataStore: Object.assign(() => undefined, {
    getState: () => ({ refetchModels: mocks.refetchModels }),
  }),
}));
vi.mock("../../../lib/settingsPersistence", () => ({
  readSetting: async (key: string) =>
    key === "model.provider_preference" && mocks.saved ? mocks.saved : { status: "missing" },
  upsertStringSetting: mocks.upsertStringSetting,
  deleteSettingIfExists: mocks.deleteSettingIfExists,
}));

import { ModelPreferences } from "../ModelPreferences";

const model = (id: string, name: string, driverId: string, provider: string, tags: string[]) => ({
  id: `${id}@${driverId}`,
  name,
  provider,
  driverId,
  capabilities: [],
  tags,
});

const CLAUDE = model("claude-5-opus", "Claude 5 Opus", "claude", "Claude", ["flagship"]);
const CODEX = model("gpt-6-astra", "GPT-6 Astra", "codex", "Codex", ["flagship"]);
const LOCAL = {
  ...model("llama", "Llama", "local", "Local", ["fast"]),
  local: { daemonId: "d", machineName: "m", endpointId: "e", endpointKind: "ollama", online: true },
};

const connected = [
  { provider: "claude", hasApiKey: true },
  { provider: "codex", hasApiKey: true },
];

const renderPrefs = () => render(<ModelPreferences providers={connected} />);

describe("ModelPreferences default provider", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.models = [CLAUDE, CODEX];
    mocks.tiers = {};
    mocks.saved = undefined;
  });
  afterEach(cleanup);

  it("is hidden when only one provider is connected", async () => {
    mocks.models = [CLAUDE, LOCAL];
    renderPrefs();
    await screen.findByText("Flagship", { exact: false });
    expect(screen.queryByTestId("default-provider-control")).toBeNull();
  });

  it("lists Auto plus each connected provider, never local", async () => {
    mocks.models = [CLAUDE, CODEX, LOCAL];
    renderPrefs();
    const select = (await screen.findByLabelText("Default provider")) as HTMLSelectElement;
    expect([...select.options].map((o) => [o.value, o.textContent])).toEqual([
      ["", "Auto"],
      ["claude", "Claude"],
      ["codex", "ChatGPT"],
    ]);
    expect(select.value).toBe("");
  });

  it("writes {providers:[driver]} and refetches the catalog", async () => {
    renderPrefs();
    const select = await screen.findByLabelText("Default provider");
    fireEvent.change(select, { target: { value: "codex" } });
    await waitFor(() => expect(mocks.refetchModels).toHaveBeenCalledTimes(1));
    expect(mocks.upsertStringSetting).toHaveBeenCalledWith(
      "model.provider_preference",
      JSON.stringify({ providers: ["codex"] }),
    );
    expect(mocks.upsertStringSetting.mock.invocationCallOrder[0]).toBeLessThan(
      mocks.refetchModels.mock.invocationCallOrder[0],
    );
  });

  it("deletes the setting when Auto is chosen", async () => {
    mocks.saved = { status: "found", value: JSON.stringify({ providers: ["codex"] }) };
    renderPrefs();
    const select = (await screen.findByLabelText("Default provider")) as HTMLSelectElement;
    await waitFor(() => expect(select.value).toBe("codex"));
    fireEvent.change(select, { target: { value: "" } });
    await waitFor(() => expect(mocks.deleteSettingIfExists).toHaveBeenCalledWith("model.provider_preference"));
    expect(mocks.upsertStringSetting).not.toHaveBeenCalled();
    expect(mocks.refetchModels).toHaveBeenCalledTimes(1);
  });

  it("treats bad JSON as Auto", async () => {
    mocks.saved = { status: "found", value: "{not json" };
    renderPrefs();
    const select = (await screen.findByLabelText("Default provider")) as HTMLSelectElement;
    expect(select.value).toBe("");
  });

  it("shows a saved provider that is no longer connected as unavailable", async () => {
    mocks.saved = { status: "found", value: JSON.stringify({ providers: ["gemini"] }) };
    renderPrefs();
    const select = (await screen.findByLabelText("Default provider")) as HTMLSelectElement;
    await waitFor(() => expect(select.value).toBe("gemini"));
    const option = [...select.options].find((o) => o.value === "gemini")!;
    expect(option.disabled).toBe(true);
    expect(option.textContent).toContain("not connected");
  });

  it("labels each tier with the server's resolution, naming the provider", async () => {
    mocks.tiers = { flagship: { modelId: CODEX.id, thinkingLevel: "high" } };
    renderPrefs();
    expect(await screen.findByText("GPT-6 Astra · ChatGPT")).toBeTruthy();
  });

  it("falls back to the first catalog model when tiers lack the tag", async () => {
    renderPrefs();
    expect(await screen.findByText("Claude 5 Opus · Claude")).toBeTruthy();
  });
});
