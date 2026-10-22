import { describe, expect, it, vi } from "vitest";

const config = vi.hoisted(() => ({ hasControlPlane: true }));

vi.mock("@/services/controlPlane/config", () => ({
  get hasControlPlane() {
    return config.hasControlPlane;
  },
}));

import { getVisibleSettingsSectionIds } from "../SettingsNavigation";
import { SETTINGS_SECTION_IDS } from "../../../routeSchemas";

/**
 * Connectors moved into Settings → Machines (each machine's Access section),
 * so the standalone section is gone. GitHub and MCP Servers are separate
 * concerns and stay exactly where they were.
 */
describe("Settings sections", () => {
  it("has no standalone Connectors section", () => {
    expect(getVisibleSettingsSectionIds()).not.toContain("connectors");
    expect(SETTINGS_SECTION_IDS as readonly string[]).not.toContain("connectors");
  });

  it("keeps GitHub and MCP Servers", () => {
    const ids = getVisibleSettingsSectionIds();
    expect(ids).toContain("git-connections");
    expect(ids).toContain("mcp");
  });

  // App access lives on a machine, and connectors exist without a control
  // plane, so Machines must be reachable in every build — otherwise a grant
  // made through the consent screen could never be revoked.
  it.each([true, false])("shows Machines when hasControlPlane=%s", async (hasCP) => {
    config.hasControlPlane = hasCP;
    vi.resetModules();
    const mod = await import("../SettingsNavigation");
    expect(mod.getVisibleSettingsSectionIds()).toContain("environments");
  });
});
