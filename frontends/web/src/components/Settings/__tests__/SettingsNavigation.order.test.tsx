import { describe, expect, it, vi } from "vitest";

const config = vi.hoisted(() => ({ hasControlPlane: true }));

vi.mock("@/services/controlPlane/config", () => ({
  get hasControlPlane() {
    return config.hasControlPlane;
  },
}));

import { getVisibleSettingsSectionIds } from "../SettingsNavigation";

describe("SettingsNavigation order", () => {
  it("puts the essential settings first", () => {
    expect(getVisibleSettingsSectionIds().slice(0, 3)).toEqual([
      "general",
      "environments",
      "account",
    ]);
  });
});
