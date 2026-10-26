import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithQuery } from "../../../../test/renderWithQuery";
import { OnLimitDefault } from "../OnLimitDefault";

const mocks = vi.hoisted(() => ({
  models: [] as Array<Record<string, unknown>>,
  readSetting: vi.fn(),
  upsertStringSetting: vi.fn(),
}));

const claude = { id: "claude-5.5-opus@claude", name: "Claude Opus", provider: "Claude subscription", driverId: "claude", capabilities: [], tags: ["flagship"] };
const sol = { id: "gpt-5.6-sol@codex", name: "GPT-5.6 Sol", provider: "Codex", driverId: "codex", capabilities: [], tags: ["flagship"] };

vi.mock("../../../../store/globalDataStore", () => ({
  useModels: () => ({ models: mocks.models, tiers: {}, loading: false, error: null }),
}));
vi.mock("../../../../lib/settingsPersistence", () => ({
  readSetting: mocks.readSetting,
  upsertStringSetting: mocks.upsertStringSetting,
}));

const select = () => screen.getByRole("combobox", { name: /Claude subscription is rate-limited/ }) as HTMLSelectElement;
const renderDefault = () =>
  renderWithQuery(<OnLimitDefault provider="claude" displayName="Claude subscription" variant="desktop" />);

beforeEach(() => {
  mocks.models = [claude, sol];
  mocks.readSetting.mockReset();
  mocks.readSetting.mockResolvedValue({ status: "missing" });
  mocks.upsertStringSetting.mockReset();
  mocks.upsertStringSetting.mockResolvedValue(undefined);
});

describe("OnLimitDefault (Settings → Providers)", () => {
  it("waits by default: cross-provider fallback is off until someone turns it on", async () => {
    renderDefault();
    await waitFor(() => expect(select().value).toBe("wait"));
    expect(Array.from(select().options).map((o) => o.textContent)).toEqual([
      "Wait for the reset",
      "Use GPT-5.6 Sol · Codex",
    ]);
  });

  it("writes provider.policy.defaults keyed by driver, in the shape the server reads", async () => {
    renderDefault();
    await userEvent.selectOptions(select(), "gpt-5.6-sol@codex");
    await waitFor(() => expect(mocks.upsertStringSetting).toHaveBeenCalled());
    const [key, value] = mocks.upsertStringSetting.mock.calls[0];
    expect(key).toBe("provider.policy.defaults");
    expect(JSON.parse(value)).toEqual({ claude: { on_limit: "fallback", fallback_model: "gpt-5.6-sol@codex" } });
  });

  it("keeps other providers' defaults when one changes", async () => {
    mocks.readSetting.mockResolvedValue({ status: "found", value: JSON.stringify({ codex: { on_limit: "wait" } }) });
    renderDefault();
    await waitFor(() => expect(mocks.readSetting).toHaveBeenCalled());
    await userEvent.selectOptions(select(), "gpt-5.6-sol@codex");
    await waitFor(() => expect(mocks.upsertStringSetting).toHaveBeenCalled());
    expect(JSON.parse(mocks.upsertStringSetting.mock.calls[0][1])).toEqual({
      codex: { on_limit: "wait" },
      claude: { on_limit: "fallback", fallback_model: "gpt-5.6-sol@codex" },
    });
  });

  it("reads back a stored fallback", async () => {
    mocks.readSetting.mockResolvedValue({ status: "found", value: JSON.stringify({ claude: { on_limit: "fallback", fallback_model: "gpt-5.6-sol@codex" } }) });
    renderDefault();
    await waitFor(() => expect(select().value).toBe("gpt-5.6-sol@codex"));
  });

  it("says how to get a fallback when only one provider is connected", () => {
    mocks.models = [claude];
    renderDefault();
    expect(screen.queryByTestId("on-limit-default")).toBeNull();
    expect(screen.getByTestId("on-limit-unavailable")).toHaveTextContent(
      "Connect another provider to fall back when Claude subscription is limited",
    );
  });
});
