/**
 * writeStringSetting and readSettingFromServer are for a caller that rewrites
 * a whole document: a failure must reach it, and the cache must not claim a
 * value the server does not hold.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";

import { readSettingFromServer, SettingWriteError, writeStringSetting } from "../settingsPersistence";

const mocks = vi.hoisted(() => ({
  batchUpsertSettings: vi.fn(),
  deleteSetting: vi.fn(),
  listSettings: vi.fn(),
  getToken: vi.fn(),
  updateCache: vi.fn(),
  removeFromCache: vi.fn(),
}));

vi.mock("../../hooks/settings-queries", () => ({
  batchUpsertSettings: mocks.batchUpsertSettings,
  deleteSetting: mocks.deleteSetting,
  fetchSettings: mocks.listSettings,
}));

vi.mock("../../api/authProvider", () => ({
  getAuthTokenProvider: () => ({ getToken: mocks.getToken }),
}));

vi.mock("../../services/settingsSync", () => ({
  settingsSync: {
    initialize: vi.fn(),
    isInitialized: () => true,
    getSettingFromCache: () => null,
    updateCache: mocks.updateCache,
    removeFromCache: mocks.removeFromCache,
  },
}));

describe("settingsPersistence strict read and write", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset();
    mocks.getToken.mockResolvedValue("token-abc");
    mocks.batchUpsertSettings.mockResolvedValue([]);
  });

  it("resolves a stored write and only then updates the cache", async () => {
    await writeStringSetting("sidebar.projects", "{}");
    expect(mocks.batchUpsertSettings).toHaveBeenCalledTimes(1);
    expect(mocks.updateCache).toHaveBeenCalledWith("sidebar.projects", "{}");
  });

  it("rejects a refused write and leaves the cache alone", async () => {
    mocks.batchUpsertSettings.mockRejectedValue(new Error("permission denied"));
    await expect(writeStringSetting("sidebar.projects", "{}")).rejects.toBeInstanceOf(SettingWriteError);
    expect(mocks.updateCache).not.toHaveBeenCalled();
  });

  it("rejects a write while signed out, sending nothing", async () => {
    mocks.getToken.mockRejectedValue(new Error("no session"));
    await expect(writeStringSetting("sidebar.projects", "{}")).rejects.toBeInstanceOf(SettingWriteError);
    expect(mocks.batchUpsertSettings).not.toHaveBeenCalled();
  });

  it("reads the server's copy, and says error rather than missing when it cannot", async () => {
    mocks.listSettings.mockResolvedValue({
      settings: [
        { key: "sidebar.projects", value: '{"o":{}}', projectId: "p1" },
        { key: "sidebar.projects", value: '{"o":{"show_all":true}}' },
      ],
    });
    expect(await readSettingFromServer("sidebar.projects")).toEqual({
      status: "found",
      value: '{"o":{"show_all":true}}',
    });
    mocks.listSettings.mockResolvedValue({ settings: [] });
    expect(await readSettingFromServer("sidebar.projects")).toEqual({ status: "missing" });
    mocks.listSettings.mockRejectedValue(new Error("unavailable"));
    expect(await readSettingFromServer("sidebar.projects")).toEqual({ status: "error" });
  });
});
