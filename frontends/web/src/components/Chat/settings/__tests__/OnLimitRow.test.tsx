import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { renderWithQuery } from "../../../../test/renderWithQuery";
import { ChatSelectionSchema, ProviderPolicy_OnLimit, ProviderPolicySchema } from "../../../../gen/reliant/v1/chat_pb";
import { ModelSettingsPage } from "../ModelSettingsPage";

const mocks = vi.hoisted(() => ({
  models: [
    { id: "claude-5.5-opus@claude", name: "Claude Opus", provider: "Claude subscription", driverId: "claude", capabilities: [], tags: ["flagship"] },
    { id: "gpt-5.6-sol@codex", name: "GPT-5.6 Sol", provider: "Codex", driverId: "codex", capabilities: [], tags: ["flagship"] },
    { id: "gpt-5.6-terra@codex", name: "GPT-5.6 Terra", provider: "Codex", driverId: "codex", capabilities: [], tags: ["moderate"] },
    { id: "qwen3:latest@local", name: "qwen3:latest", provider: "Local", driverId: "local", capabilities: [], tags: [], local: { daemonId: "d1", machineName: "Mac", endpointId: "o", endpointKind: "ollama", online: true } },
  ] as Array<Record<string, unknown>>,
  selection: null as unknown,
  updateProviderPolicy: vi.fn(),
  readSetting: vi.fn(),
  upsertStringSetting: vi.fn(),
}));

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));
vi.mock("../../../../store/globalDataStore", () => ({
  useModels: () => ({ models: mocks.models, tiers: { flagship: { modelId: "claude-5.5-opus@claude", thinkingLevel: "" } }, loading: false, error: null }),
}));
vi.mock("../../../../hooks/chat-queries", async () => {
  const actual = await vi.importActual<typeof import("../../../../hooks/chat-queries")>("../../../../hooks/chat-queries");
  return {
    ...actual,
    useChatSelection: () => ({ data: mocks.selection }),
    setChatSelection: vi.fn(),
  };
});
vi.mock("../../../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../../../api/client")>("../../../../api/client");
  return { ...actual, api: { ...actual.api, chatsV2: { ...actual.api.chatsV2, updateProviderPolicy: mocks.updateProviderPolicy } } };
});
vi.mock("../../../../lib/settingsPersistence", () => ({
  readSetting: mocks.readSetting,
  upsertStringSetting: mocks.upsertStringSetting,
}));

function selectionWith(onLimit: ProviderPolicy_OnLimit, fallbackModel?: string) {
  return create(ChatSelectionSchema, {
    version: 3n,
    providerPolicy: create(ProviderPolicySchema, { onLimit, fallbackModel }),
  });
}

function renderRow(props: { chatId?: string; value?: unknown } = {}) {
  return renderWithQuery(
    <ModelSettingsPage
      value={props.value ?? { id: "claude-5.5-opus@claude" }}
      onChange={() => {}}
      onBack={() => {}}
      onClose={() => {}}
      chatId={"chatId" in props ? props.chatId : "chat-1"}
    />,
  );
}

const select = () => screen.getByRole("combobox", { name: /rate-limited/ }) as HTMLSelectElement;

const allModels = [...mocks.models];

beforeEach(() => {
  mocks.models = [...allModels];
  mocks.selection = null;
  mocks.updateProviderPolicy.mockReset();
  mocks.updateProviderPolicy.mockResolvedValue(selectionWith(ProviderPolicy_OnLimit.WAIT));
  mocks.readSetting.mockResolvedValue({ status: "missing" });
  mocks.upsertStringSetting.mockResolvedValue(undefined);
});

describe("OnLimitRow", () => {
  it("names the chat's provider and offers wait, a stand-in on each OTHER provider, Choose…, and the Settings default", () => {
    renderRow();
    expect(screen.getByText("If Claude subscription is rate-limited:")).toBeTruthy();
    const options = Array.from(select().options).map((o) => o.textContent);
    expect(options).toEqual([
      "Wait for the reset",
      "Use GPT-5.6 Sol · Codex",
      "Choose…",
      "Use Settings default (Wait for the reset)",
    ]);
  });

  it("shows the Settings default (cross-provider OFF) when the chat has chosen nothing", () => {
    renderRow();
    expect(select().value).toBe("inherit");
  });

  it("writes the choice through UpdateChatProviderPolicy with the selection version it was made against", async () => {
    mocks.selection = selectionWith(ProviderPolicy_OnLimit.UNSPECIFIED);
    renderRow();
    await userEvent.selectOptions(select(), "fallback:gpt-5.6-sol@codex");
    await waitFor(() => expect(mocks.updateProviderPolicy).toHaveBeenCalledTimes(1));
    expect(mocks.updateProviderPolicy).toHaveBeenCalledWith(
      "chat-1",
      { onLimit: ProviderPolicy_OnLimit.FALLBACK, fallbackModel: "gpt-5.6-sol@codex" },
      3n,
    );
  });

  it("'Wait' and 'Use Settings default' write WAIT and UNSPECIFIED", async () => {
    mocks.selection = selectionWith(ProviderPolicy_OnLimit.FALLBACK, "gpt-5.6-sol@codex");
    renderRow();
    expect(select().value).toBe("fallback:gpt-5.6-sol@codex");
    await userEvent.selectOptions(select(), "wait");
    await waitFor(() => expect(mocks.updateProviderPolicy).toHaveBeenLastCalledWith("chat-1", { onLimit: ProviderPolicy_OnLimit.WAIT }, 3n));
    await userEvent.selectOptions(select(), "inherit");
    await waitFor(() => expect(mocks.updateProviderPolicy).toHaveBeenLastCalledWith("chat-1", { onLimit: ProviderPolicy_OnLimit.UNSPECIFIED }, 3n));
  });

  it("'Choose…' lists every model on another provider", async () => {
    renderRow();
    await userEvent.selectOptions(select(), "choose");
    const picker = screen.getByRole("combobox", { name: "Fall back to" }) as HTMLSelectElement;
    expect(Array.from(picker.options).map((o) => o.textContent)).toEqual([
      "Pick a model on another provider…",
      "GPT-5.6 Sol · Codex",
      "GPT-5.6 Terra · Codex",
    ]);
    await userEvent.selectOptions(picker, "gpt-5.6-terra@codex");
    await waitFor(() =>
      expect(mocks.updateProviderPolicy).toHaveBeenCalledWith("chat-1", { onLimit: ProviderPolicy_OnLimit.FALLBACK, fallbackModel: "gpt-5.6-terra@codex" }, undefined),
    );
  });

  it("with no other provider connected it says so and offers only waiting", () => {
    mocks.models = mocks.models.filter((m) => m.driverId === "claude");
    renderRow();
    expect(screen.getByTestId("on-limit-no-provider").textContent).toContain("Connect another provider");
    expect(Array.from(select().options).map((o) => o.textContent)).toEqual([
      "Wait for the reset",
      "Use Settings default (Wait for the reset)",
    ]);
  });

  it("is hidden for a local model and for a settings page with no chat", () => {
    renderRow({ value: { id: "qwen3:latest@local", providers: ["local:d1"] } });
    expect(screen.queryByTestId("on-limit-row")).toBeNull();
    renderRow({ chatId: undefined });
    expect(screen.queryByTestId("on-limit-row")).toBeNull();
  });
});
