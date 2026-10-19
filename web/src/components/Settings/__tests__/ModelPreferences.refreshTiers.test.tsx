import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  refetchModels: vi.fn().mockResolvedValue(undefined),
  upsertStringSetting: vi.fn().mockResolvedValue(undefined),
  deleteSettingIfExists: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("../../../store/globalDataStore", () => ({
  useModels: () => ({ models: [], tiers: {}, loading: false, error: null }),
  useGlobalDataStore: Object.assign(() => undefined, {
    getState: () => ({ refetchModels: mocks.refetchModels }),
  }),
}));
vi.mock("../../../lib/settingsPersistence", () => ({
  readSetting: async () => ({ status: "missing" }),
  upsertStringSetting: mocks.upsertStringSetting,
  deleteSettingIfExists: mocks.deleteSettingIfExists,
}));

import { saveTagConfig } from "../ModelPreferences";

describe("saving a tag preference", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("refetches the tier table after the preference is written, so the composer pill follows", async () => {
    await saveTagConfig("flagship", { model_id: "gpt-5.6-sol" });

    expect(mocks.upsertStringSetting).toHaveBeenCalledTimes(1);
    expect(mocks.refetchModels).toHaveBeenCalledTimes(1);
    expect(mocks.upsertStringSetting.mock.invocationCallOrder[0]).toBeLessThan(
      mocks.refetchModels.mock.invocationCallOrder[0],
    );
  });

  it("refetches after clearing a preference too", async () => {
    await saveTagConfig("flagship", {});

    expect(mocks.deleteSettingIfExists).toHaveBeenCalledTimes(1);
    expect(mocks.refetchModels).toHaveBeenCalledTimes(1);
  });
});
