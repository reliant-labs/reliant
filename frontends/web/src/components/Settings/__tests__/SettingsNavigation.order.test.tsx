import { describe, expect, it, vi } from "vitest";

const config = vi.hoisted(() => ({ hasControlPlane: true }));

vi.mock("@/services/controlPlane/config", () => ({
  get hasControlPlane() {
    return config.hasControlPlane;
  },
}));

import { getVisibleSettingsSectionIds } from "../SettingsNavigation";

describe("SettingsNavigation order", () => {
  it("groups the nav as Account · AI · Work · App, leading with the default section", () => {
    expect(getVisibleSettingsSectionIds()).toEqual([
      "account",
      "billing",
      "member-permissions",
      "ai",
      "mcp",
      "prompts",
      "presets",
      "browser",
      "projects",
      "branches",
      "automations",
      "machines",
      "git-connections",
      "tokens",
      "appearance",
      "shortcuts",
      "notifications",
      "privacy",
      "about",
      "developer",
    ]);
  });

  it("drops the cloud-only sections without a control plane", async () => {
    config.hasControlPlane = false;
    vi.resetModules();
    const mod = await import("../SettingsNavigation");
    expect(mod.getVisibleSettingsSectionIds()).not.toContain("billing");
    expect(mod.getVisibleSettingsSectionIds()).not.toContain("member-permissions");
    expect(mod.getVisibleSettingsSectionIds()[0]).toBe("account");
    config.hasControlPlane = true;
  });
});
