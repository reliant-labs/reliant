import { beforeEach, describe, expect, it, vi } from "vitest";
import { readSettingsDestination, rememberSettingsDestination } from "../settingsDestination";

const key = "reliant.settings.lastDestination";
beforeEach(() => localStorage.clear());

describe("settings destination persistence", () => {
  it.each(["broken json", "null", "[]", '"/settings/ai"', '{"section":"retired"}', '{"section":123}', '{"section":"https://example.com"}'])(
    "rejects invalid saved destinations: %s", (saved) => {
      localStorage.setItem(key, saved);
      expect(readSettingsDestination()).toEqual({ section: "account" });
    },
  );

  it.each([
    [{ section: "ai", tab: "models" }, { section: "ai", tab: "models" }],
    [{ section: "ai", tab: "usage" }, { section: "ai" }],
    [{ section: "billing", tab: "invoices" }, { section: "billing", tab: "invoices" }],
    [{ section: "billing", tab: "local" }, { section: "billing" }],
    [{ section: "appearance", tab: "models", machineId: "d-1" }, { section: "appearance" }],
    [{ section: "machines", machineId: "d-1", checkout: "success", from: "connectors" }, { section: "machines", machineId: "d-1" }],
    [{ section: "machines", machineId: {} }, { section: "machines" }],
    [{ section: "machines", machineId: "  " }, { section: "machines" }],
  ])("only restores supported navigation fields from %j", (saved, expected) => {
    localStorage.setItem(key, JSON.stringify(saved));
    expect(readSettingsDestination()).toEqual(expected);
  });

  it("writes only the destination allowlist", () => {
    rememberSettingsDestination({ section: "billing", tab: "plans", ...{ checkout: "success", returnTo: "/other" } });
    expect(JSON.parse(localStorage.getItem(key)!)).toEqual({ section: "billing", tab: "plans" });
  });

  it("tolerates storage being inaccessible before even reading a key", () => {
    const storage = vi.spyOn(window, "localStorage", "get").mockImplementation(() => { throw new Error("SecurityError"); });
    try {
      expect(readSettingsDestination()).toEqual({ section: "account" });
      expect(() => rememberSettingsDestination({ section: "ai" })).not.toThrow();
    } finally {
      storage.mockRestore();
    }
  });
});
