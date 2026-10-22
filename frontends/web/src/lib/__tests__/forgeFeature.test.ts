/**
 * The forge UI gate.
 *
 * The property that matters most is the DEFAULT: the feature has shipped, so
 * every user gets it — in a packaged production build as much as in a dev one —
 * unless they have explicitly turned it off. Two of the gated screens front
 * write paths, one of which deploys to a live cluster, so the explicit
 * off-switch has to keep working in both directions.
 */
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import {
  FORGE_UI_FLAG_KEY,
  clearForgeUIPreference,
  isForgeUIEnabled,
  setForgeUIEnabled,
} from "../forgeFeature";

// getIsDev is pinned FALSE — i.e. a packaged production build — for every case
// in this file. That is the configuration the default had to change for, and it
// is not the one the test runner produces on its own: under jsdom getIsDev()
// answers true, so a test that left it alone would pass against both the old
// default and the new one and discriminate nothing.
vi.mock("../constants", () => ({
  getIsDev: () => false,
}));

describe("forge UI gate", () => {
  beforeEach(() => {
    window.localStorage.clear();
  });

  afterEach(() => {
    window.localStorage.clear();
  });

  describe("the default, with no stored preference", () => {
    it("is ON — the headline property, and it no longer depends on the build type", () => {
      // The gate used to fall through to getIsDev(), which made a packaged
      // build behave differently from a dev one. It deliberately does not
      // consult the build type any more: production is on.
      expect(isForgeUIEnabled()).toBe(true);
    });

    it("is ON even when nothing has ever written the key", () => {
      expect(window.localStorage.getItem(FORGE_UI_FLAG_KEY)).toBeNull();

      expect(isForgeUIEnabled()).toBe(true);
    });
  });

  describe("an explicit preference wins over the default", () => {
    it("opting out disables it — the off-switch users are pointed at", () => {
      setForgeUIEnabled(false);

      expect(isForgeUIEnabled()).toBe(false);
    });

    it("opting in is a no-op against the default, and still records the choice", () => {
      setForgeUIEnabled(true);

      expect(isForgeUIEnabled()).toBe(true);
      expect(window.localStorage.getItem(FORGE_UI_FLAG_KEY)).toBe("true");
    });

    it("clearing the preference returns to the default, which is now ON", () => {
      setForgeUIEnabled(false);
      expect(isForgeUIEnabled()).toBe(false);

      clearForgeUIPreference();

      expect(isForgeUIEnabled()).toBe(true);
    });
  });

  describe("robustness", () => {
    it("ignores a junk stored value rather than treating it as opted-out", () => {
      // Anything other than the two literals is not a preference, so it must
      // fall through to the default rather than being read as a choice the
      // user never made.
      window.localStorage.setItem(FORGE_UI_FLAG_KEY, "yes-please");

      expect(isForgeUIEnabled()).toBe(true);
    });

    it("falls back to the default when storage throws", () => {
      // Safari private mode throws on localStorage access. An unreadable store
      // must not pin the feature off for someone who never opted out.
      const getItem = vi
        .spyOn(window.localStorage, "getItem")
        .mockImplementation(() => {
          throw new Error("storage unavailable");
        });

      expect(isForgeUIEnabled()).toBe(true);

      getItem.mockRestore();
    });

    it("does not throw when a storage write fails", () => {
      const setItem = vi
        .spyOn(window.localStorage, "setItem")
        .mockImplementation(() => {
          throw new Error("quota exceeded");
        });

      expect(() => setForgeUIEnabled(false)).not.toThrow();

      setItem.mockRestore();
    });

    it("reads storage on every call, so a mid-session toggle takes effect", () => {
      // A module-scope snapshot would need a reload, which would make the
      // settings toggle look broken.
      expect(isForgeUIEnabled()).toBe(true);

      setForgeUIEnabled(false);

      expect(isForgeUIEnabled()).toBe(false);
    });
  });
});
