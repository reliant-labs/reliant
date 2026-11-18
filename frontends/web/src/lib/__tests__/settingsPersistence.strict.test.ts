/**
 * writeStringSetting and readSettingFromServer are for a caller that rewrites
 * a whole document: a failure must reach it, and the cache must not claim a
 * value the server does not hold.
 */
import { waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { currentSessionOwner, resetSessionOwnerForTests, setSessionOwner } from "../auth/sessionOwner";
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
  fetchSettingsFor: mocks.listSettings,
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
    // Signed in: a write captured with nobody is refused (lib/auth/sessionOwner).
    resetSessionOwnerForTests();
    setSessionOwner("user-a");
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

  it("refuses a read or write captured with nobody, with an opaque key, and publishes nothing", async () => {
    resetSessionOwnerForTests();
    const nobody = currentSessionOwner();
    expect(nobody.userId).toBeNull();
    await expect(writeStringSetting("sidebar.projects", "{}", nobody)).rejects.toBeInstanceOf(SettingWriteError);
    expect(await readSettingFromServer("sidebar.projects", nobody)).toEqual({ status: "error" });
    expect(mocks.batchUpsertSettings).not.toHaveBeenCalled();
    expect(mocks.listSettings).not.toHaveBeenCalled();
    expect(mocks.updateCache).not.toHaveBeenCalled();
  });

  describe("when the account switches mid-flight", () => {
    beforeEach(() => {
      resetSessionOwnerForTests();
      setSessionOwner("alice");
    });

    function deferred<T>() {
      let resolve!: (value: T) => void;
      const promise = new Promise<T>((r) => {
        resolve = r;
      });
      return { promise, resolve };
    }

    it("a deferred read made for one account publishes nothing once another signs in", async () => {
      const alice = currentSessionOwner();
      const answer = deferred<{ settings: Array<{ key: string; value: string }> }>();
      mocks.listSettings.mockReturnValue(answer.promise);
      const read = readSettingFromServer("sidebar.projects", alice);
      await waitFor(() => expect(mocks.listSettings).toHaveBeenCalledTimes(1));
      // The read goes out naming Alice, so the transport sends it only with her credential.
      expect(mocks.listSettings.mock.calls[0]![0]).toBe(alice);

      setSessionOwner("bob");
      answer.resolve({ settings: [{ key: "sidebar.projects", value: '{"a":{"show_all":true}}' }] });
      expect(await read).toEqual({ status: "error" });
      expect(mocks.updateCache).not.toHaveBeenCalled();
      expect(mocks.removeFromCache).not.toHaveBeenCalled();
    });

    it("refuses before sending a write captured for an account no longer signed in", async () => {
      const alice = currentSessionOwner();
      setSessionOwner("bob");
      await expect(writeStringSetting("sidebar.projects", "{}", alice)).rejects.toBeInstanceOf(SettingWriteError);
      expect(mocks.batchUpsertSettings).not.toHaveBeenCalled();
      expect(mocks.updateCache).not.toHaveBeenCalled();
    });

    it("sends a write as its captured owner, and publishes nothing when it answers after a switch", async () => {
      const alice = currentSessionOwner();
      const answer = deferred<never[]>();
      mocks.batchUpsertSettings.mockReturnValue(answer.promise);
      const write = writeStringSetting("sidebar.projects", '{"a":{}}', alice);
      await waitFor(() => expect(mocks.batchUpsertSettings).toHaveBeenCalledTimes(1));
      expect(mocks.batchUpsertSettings.mock.calls[0]![2]).toBe(alice);

      setSessionOwner("bob");
      answer.resolve([]);
      await write;
      expect(mocks.updateCache).not.toHaveBeenCalled();
    });
  });
});
