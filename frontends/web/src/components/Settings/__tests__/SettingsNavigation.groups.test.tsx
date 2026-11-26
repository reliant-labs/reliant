import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ hasControlPlane: true }));

import { SettingsNavigation, type SettingsSection } from "../SettingsNavigation";
import type { SettingsTab } from "../../../routeSchemas";

function renderNav(activeSection: SettingsSection, props: { isCollapsed?: boolean; activeTab?: SettingsTab } = {}) {
  const onSectionChange = vi.fn();
  const view = render(<SettingsNavigation activeSection={activeSection} onSectionChange={onSectionChange} {...props} />);
  return { ...view, onSectionChange };
}

const group = (name: string) => screen.getByTestId(`settings-group-${name}`);
const labels = (name: string) => within(group(name)).queryAllByRole("button").map((b) => b.getAttribute("aria-label") ?? b.textContent);

describe("SettingsNavigation grouping", () => {
  it("shows six groups, in order, with the everyday items open", () => {
    renderNav("account");
    const headings = screen.getAllByTestId(/^settings-group-/).map((el) => el.getAttribute("data-testid")?.replace("settings-group-", ""));
    expect(headings).toEqual(["Account", "AI", "Connections", "Sandboxes & projects", "Preferences", "Advanced"]);

    expect(labels("Account")).toEqual(["Account", "Billing & usage", "Members", "Policies", "AI usage", "Privacy"]);
    expect(labels("AI")).toEqual(["Personal accounts", "Organization accounts", "Models & tools"]);
    expect(labels("Connections")).toEqual(["Integrations", "GitHub", "Organization GitHub", "Tools (MCP)"]);
    expect(labels("Sandboxes & projects")).toEqual(["Agent sandboxes", "Projects"]);
    expect(labels("Preferences")).toEqual(["Appearance", "Chat & editor", "Notifications", "Keyboard shortcuts"]);
  });

  it("collapses Advanced by default: its heading shows, its sections do not", () => {
    renderNav("account");
    const toggle = within(group("Advanced")).getByRole("button", { name: "Advanced" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    for (const name of ["Prompts", "Presets", "Approve agent changes", "Workspaces", "Access tokens", "Built-in browser", "Developer"]) {
      expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
    }
  });

  it("opens Advanced on click and folds it again", () => {
    renderNav("account");
    const toggle = within(group("Advanced")).getByRole("button", { name: "Advanced" });
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(labels("Advanced")).toEqual(["Advanced", "Prompts", "Presets", "Approve agent changes", "Workspaces", "Access tokens", "Built-in browser", "Developer"]);
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("button", { name: "Prompts" })).not.toBeInTheDocument();
  });

  it.each<[SettingsSection, string]>([
    ["tokens", "Access tokens"],
    ["automations", "Approve agent changes"],
    ["branches", "Workspaces"],
    ["prompts", "Prompts"],
  ])("opens Advanced by itself when %s is the active section", (section, label) => {
    renderNav(section);
    expect(within(group("Advanced")).getByRole("button", { name: "Advanced" })).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("button", { name: label })).toHaveAttribute("aria-current", "page");
  });

  it("keeps Advanced open while its section is active: the heading cannot hide the current item", () => {
    renderNav("tokens");
    const toggle = () => within(group("Advanced")).getByRole("button", { name: "Advanced" });
    fireEvent.click(toggle());
    expect(toggle()).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("button", { name: "Access tokens" })).toHaveAttribute("aria-current", "page");
  });

  it("an open made on one section does not stick to the next: leaving Advanced's sections folds it", () => {
    const { rerender, onSectionChange } = renderNav("account");
    const toggle = () => within(group("Advanced")).getByRole("button", { name: "Advanced" });
    fireEvent.click(toggle());
    expect(toggle()).toHaveAttribute("aria-expanded", "true");
    rerender(<SettingsNavigation activeSection="privacy" onSectionChange={onSectionChange} />);
    expect(toggle()).toHaveAttribute("aria-expanded", "false");
  });

  it("keeps the AI entries' routes: Personal accounts and Models & tools are /settings/ai's tabs", () => {
    const { onSectionChange } = renderNav("account");
    const ai = within(group("AI"));
    fireEvent.click(ai.getByRole("button", { name: "Personal accounts" }));
    expect(onSectionChange).toHaveBeenLastCalledWith("ai");
    fireEvent.click(ai.getByRole("button", { name: "Organization accounts" }));
    expect(onSectionChange).toHaveBeenLastCalledWith("org-ai-accounts");
    fireEvent.click(ai.getByRole("button", { name: "Models & tools" }));
    expect(onSectionChange).toHaveBeenLastCalledWith("ai", "models");
  });

  it.each<[SettingsTab | undefined, string]>([
    [undefined, "Personal accounts"],
    ["providers", "Personal accounts"],
    ["models", "Models & tools"],
    ["local", "Models & tools"],
  ])("on /settings/ai?tab=%s marks only %s current", (activeTab, label) => {
    renderNav("ai", { activeTab });
    const current = within(group("AI")).getAllByRole("button").filter((b) => b.getAttribute("aria-current") === "page");
    expect(current.map((b) => b.getAttribute("aria-label"))).toEqual([label]);
  });

  it("keeps Agent sandboxes and its route id (machines) in Sandboxes & projects", () => {
    const { onSectionChange } = renderNav("account");
    fireEvent.click(within(group("Sandboxes & projects")).getByRole("button", { name: "Agent sandboxes" }));
    expect(onSectionChange).toHaveBeenCalledWith("machines");
  });

  it("puts About in the footer, outside every group, and navigates to it", () => {
    const { onSectionChange } = renderNav("account");
    const about = screen.getByRole("button", { name: "About" });
    expect(screen.getAllByTestId(/^settings-group-/).some((g) => g.contains(about))).toBe(false);
    expect(about.textContent).toMatch(/About$/);
    fireEvent.click(about);
    expect(onSectionChange).toHaveBeenCalledWith("about");
  });

  it("marks About current when it is the active section, without opening Advanced", () => {
    renderNav("about");
    expect(screen.getByRole("button", { name: "About" })).toHaveAttribute("aria-current", "page");
    expect(within(group("Advanced")).getByRole("button", { name: "Advanced" })).toHaveAttribute("aria-expanded", "false");
  });

  it("lists every section in the narrow rail, Advanced included, and keeps About", () => {
    renderNav("account", { isCollapsed: true });
    for (const name of ["Account", "Tools (MCP)", "Access tokens", "Approve agent changes", "About"]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument();
    }
    expect(screen.queryByRole("button", { name: "Advanced" })).not.toBeInTheDocument();
  });
});
