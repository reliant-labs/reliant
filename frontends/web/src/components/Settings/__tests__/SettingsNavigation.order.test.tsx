import { describe, expect, it, vi } from "vitest";

const config = vi.hoisted(() => ({ hasControlPlane: true }));

vi.mock("@/services/controlPlane/config", () => ({
  get hasControlPlane() {
    return config.hasControlPlane;
  },
}));

import { getVisibleSettingsSectionIds } from "../SettingsNavigation";
import { SETTINGS_SECTION_IDS } from "../../../routeSchemas";

describe("SettingsNavigation order", () => {
  // Vitest runs with import.meta.env.DEV true, so Developer is listed here;
  // the production case is the test below.
  it("groups the nav as Account · AI · Connections · Machines & projects · Preferences · Advanced, with About last as the footer", () => {
    expect(getVisibleSettingsSectionIds()).toEqual([
      "account",
      "billing",
      "member-permissions",
      "policies",
      "org-ai-accounts",
      "org-ai-usage",
      "privacy",
      "ai",
      "integrations",
      "git-connections",
      "mcp",
      "machines",
      "projects",
      "appearance",
      "chat-editor",
      "notifications",
      "shortcuts",
      "prompts",
      "presets",
      "automations",
      "branches",
      "tokens",
      "browser",
      "developer",
      "about",
    ]);
  });

  it("keeps every section reachable: the same ids as before, regrouped", () => {
    expect([...getVisibleSettingsSectionIds()].sort()).toEqual([...SETTINGS_SECTION_IDS].sort());
  });

  it("lists Developer only in a dev build", async () => {
    vi.stubEnv("DEV", false);
    try {
      vi.resetModules();
      const prod = await import("../SettingsNavigation");
      expect(prod.getVisibleSettingsSectionIds()).not.toContain("developer");
      expect(prod.showDeveloperSettings()).toBe(false);

      // An unpackaged Electron app says so in its runtime config.
      window.RELIANT_CONFIG = { ...window.RELIANT_CONFIG, isDev: true } as typeof window.RELIANT_CONFIG;
      expect(prod.getVisibleSettingsSectionIds()).toContain("developer");
    } finally {
      vi.unstubAllEnvs();
      if (window.RELIANT_CONFIG) delete (window.RELIANT_CONFIG as { isDev?: boolean }).isDev;
    }
  });

  it("drops the cloud-only sections without a control plane", async () => {
    config.hasControlPlane = false;
    vi.resetModules();
    const mod = await import("../SettingsNavigation");
    expect(mod.getVisibleSettingsSectionIds()).not.toContain("billing");
    expect(mod.getVisibleSettingsSectionIds()).not.toContain("member-permissions");
    expect(mod.getVisibleSettingsSectionIds()).not.toContain("policies");
    expect(mod.getVisibleSettingsSectionIds()).not.toContain("org-ai-accounts");
    expect(mod.getVisibleSettingsSectionIds()).not.toContain("org-ai-usage");
    expect(mod.getVisibleSettingsSectionIds()[0]).toBe("account");
    config.hasControlPlane = true;
  });
});
