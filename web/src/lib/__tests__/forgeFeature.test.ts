/**
 * The forge UI gate.
 *
 * The property that matters most is the DEFAULT: a packaged build must behave
 * exactly as it did before the feature existed, because "ready to show a user"
 * is the author's call and has not been made yet. Two of the gated screens front
 * write paths, one of which deploys to a live cluster, so a gate that fails open
 * is not a cosmetic bug.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  FORGE_UI_FLAG_KEY,
  clearForgeUIPreference,
  isForgeUIEnabled,
  setForgeUIEnabled,
} from "../forgeFeature";

// getIsDev is the build-type fallback. Mocked so each case can state the build
// type directly instead of depending on how the test runner happens to be built.
vi.mock("../constants", () => ({
  getIsDev: () => mockIsDev.value,
}));

const mockIsDev = { value: false };

describe("forge UI gate", () => {
  beforeEach(() => {
    mockIsDev.value = false;
    window.localStorage.clear();
  });

  afterEach(() => {
    window.localStorage.clear();
  });

  describe("the default, with no stored preference", () => {
    it("is OFF in a packaged build — the headline property", () => {
      mockIsDev.value = false;

      expect(isForgeUIEnabled()).toBe(false);
    });

    it("is ON in a dev build, matching the precedent for in-progress surfaces", () => {
      mockIsDev.value = true;

      expect(isForgeUIEnabled()).toBe(true);
    });
  });

  describe("an explicit preference wins over the build type", () => {
    // Both directions matter. Opting IN on a packaged build is how the author
    // reviews the feature; opting OUT on a dev build is how someone gets the old
    // behaviour back without rebuilding.
    it("opting in enables it on a packaged build", () => {
      mockIsDev.value = false;
      setForgeUIEnabled(true);

      expect(isForgeUIEnabled()).toBe(true);
    });

    it("opting out disables it even in a dev build", () => {
      mockIsDev.value = true;
      setForgeUIEnabled(false);

      expect(isForgeUIEnabled()).toBe(false);
    });

    it("clearing the preference returns to the build-type default", () => {
      mockIsDev.value = false;
      setForgeUIEnabled(true);
      expect(isForgeUIEnabled()).toBe(true);

      clearForgeUIPreference();

      expect(isForgeUIEnabled()).toBe(false);
    });
  });

  describe("robustness", () => {
    it("ignores a junk stored value rather than treating it as enabled", () => {
      // A value this build does not understand must not read as opt-in. Anything
      // other than the two literals falls through to the build-type default —
      // the same reasoning as forge's own decoders, which reject an unknown
      // token rather than defaulting it to the permissive value.
      mockIsDev.value = false;
      window.localStorage.setItem(FORGE_UI_FLAG_KEY, "yes-please");

      expect(isForgeUIEnabled()).toBe(false);
    });

    it("falls back to the build default when storage throws", () => {
      // Safari private mode throws on localStorage access. An unreadable store
      // must not pin the feature off in a dev build where it should be on.
      mockIsDev.value = true;
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

      expect(() => setForgeUIEnabled(true)).not.toThrow();

      setItem.mockRestore();
    });

    it("reads storage on every call, so a mid-session toggle takes effect", () => {
      // A module-scope snapshot would need a reload, which would make the
      // settings toggle look broken.
      mockIsDev.value = false;
      expect(isForgeUIEnabled()).toBe(false);

      setForgeUIEnabled(true);

      expect(isForgeUIEnabled()).toBe(true);
    });
  });
});
