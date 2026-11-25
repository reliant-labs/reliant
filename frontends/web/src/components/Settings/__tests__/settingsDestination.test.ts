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
  ])("only restores supported navigation fields from %j", (saved, expected) => {
    localStorage.setItem(key, JSON.stringify(saved));
    expect(readSettingsDestination()).toEqual(expected);
  });

  // Agent sandboxes is Agents › Sandboxes in the nav: restoring it opened
  // Settings with no Settings sections in the nav, on every later open.
  it.each([{ section: "machines" }, { section: "machines", machineId: "d-1" }])(
    "never restores a page another nav area owns, even one saved before: %j", (saved) => {
      localStorage.setItem(key, JSON.stringify(saved));
      expect(readSettingsDestination()).toEqual({ section: "account" });
    },
  );

  it("keeps the previous destination when a page another nav area owns is visited", () => {
    rememberSettingsDestination({ section: "appearance" });
    rememberSettingsDestination({ section: "machines" });
    expect(JSON.parse(localStorage.getItem(key)!)).toEqual({ section: "appearance" });
    expect(readSettingsDestination()).toEqual({ section: "appearance" });
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
