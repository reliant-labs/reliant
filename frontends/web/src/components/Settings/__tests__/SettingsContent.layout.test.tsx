/**
 * Every settings section renders in ONE layout: one content width, on the
 * page background, with no card wrapping the page (a card around a section
 * made each of its panels a card in a card), no `[&_h3]` parent selector and
 * no `.cloud-settings` font scope.
 */

import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { SETTINGS_SECTION_IDS, type SettingsSection } from "../../../routeSchemas";

vi.mock("../AISettings", () => ({ AISettings: () => <div data-testid="section">ai</div> }));
vi.mock("../AboutSection", () => ({ AboutSection: () => <div data-testid="section">about</div> }));
vi.mock("../DeveloperSettings", () => ({ DeveloperSettings: () => <div data-testid="section">developer</div> }));
vi.mock("../AppearanceSettings", () => ({ AppearanceSettings: () => <div data-testid="section">appearance</div> }));
vi.mock("../KeyboardShortcutsSettings", () => ({ KeyboardShortcutsSettings: () => <div data-testid="section">shortcuts</div> }));
vi.mock("../AccountSettings", () => ({ AccountSettings: () => <div data-testid="section">account</div> }));
vi.mock("../PrivacySettings", () => ({ PrivacySettings: () => <div data-testid="section">privacy</div> }));
vi.mock("../NotificationSettings", () => ({ NotificationSettings: () => <div data-testid="section">notifications</div> }));
vi.mock("../MCPSettings", () => ({ MCPSettings: () => <div data-testid="section">mcp</div> }));
vi.mock("../BranchesSection", () => ({ BranchesSection: () => <div data-testid="section">branches</div> }));
vi.mock("../BrowserSettings", () => ({ BrowserSettings: () => <div data-testid="section">browser</div> }));
vi.mock("../TokenSettings", () => ({ TokenSettings: () => <div data-testid="section">tokens</div> }));
vi.mock("../cloud/MemberPermissions", () => ({ MemberPermissions: () => <div data-testid="section">member-permissions</div> }));
vi.mock("../GitConnectionsSettings", () => ({ GitConnectionsSettings: () => <div data-testid="section">git-connections</div> }));
vi.mock("../PromptsSettings", () => ({ PromptsSettings: () => <div data-testid="section">prompts</div> }));
vi.mock("../PresetsSettings", () => ({ PresetsSettings: () => <div data-testid="section">presets</div> }));
vi.mock("../AutomationSettings", () => ({ AutomationSettings: () => <div data-testid="section">automations</div> }));
vi.mock("../ProjectsSection", () => ({ ProjectsSection: () => <div data-testid="section">projects</div> }));
vi.mock("../cloud/billing", () => ({ BillingSection: () => <div data-testid="section">billing</div> }));
vi.mock("../cloud/machines", () => ({ MachinesSection: () => <div data-testid="section">machines</div> }));
vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (sel: (s: { currentProject: null }) => unknown) => sel({ currentProject: null }),
}));

import { SettingsContent } from "../SettingsContent";

describe("SettingsContent layout", () => {
  it.each(SETTINGS_SECTION_IDS.map((id) => [id]))("renders %s in the one shared layout", async (id) => {
    const { container } = render(<SettingsContent activeSection={id as SettingsSection} />);
    expect(await screen.findByTestId("section")).toHaveTextContent(id);

    const layout = screen.getByTestId("settings-layout");
    expect(layout.className).toContain("max-w-4xl");
    // Nothing between the layout and the section draws a surface.
    expect(layout.className).not.toMatch(/bg-card|border/);
    expect(container.innerHTML).not.toContain("cloud-settings");
    expect(container.innerHTML).not.toContain("[&amp;_h3]");
  });
});
