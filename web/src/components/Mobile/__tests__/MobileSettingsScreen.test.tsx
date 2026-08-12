/**
 * MobileSettingsScreen is the section list for `/m/settings`. Pins that
 * sections respect their capability gate, billing is hidden without
 * control-plane, drill-in swaps to the section (not a route push), and each
 * section's own back button returns to the list.
 */

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SurfaceProvider } from "../../../lib/surfaceContext";

let hasControlPlane = true;
vi.mock("../../../services/controlPlane/config", () => ({
  get hasControlPlane() {
    return hasControlPlane;
  },
}));

vi.mock("../MobileAccountScreen", () => ({
  MobileAccountScreen: ({ onBack }: { onBack: () => void }) => (
    <div>
      <button onClick={onBack}>back-from-account</button>
      account-section
    </div>
  ),
}));
vi.mock("../MobileAISettingsScreen", () => ({
  MobileAISettingsScreen: ({ onBack }: { onBack: () => void }) => (
    <div>
      <button onClick={onBack}>back-from-ai</button>
      ai-section
    </div>
  ),
}));
vi.mock("../MobileBillingScreen", () => ({
  MobileBillingScreen: () => <div>billing-section</div>,
}));
vi.mock("../MobileGitHubScreen", () => ({
  MobileGitHubScreen: () => <div>github-section</div>,
}));
vi.mock("../MobileNotificationsScreen", () => ({
  MobileNotificationsScreen: () => <div>notifications-section</div>,
}));
vi.mock("../MobilePrivacyScreen", () => ({
  MobilePrivacyScreen: () => <div>privacy-section</div>,
}));
vi.mock("../MobileAppearanceScreen", () => ({
  MobileAppearanceScreen: () => <div>appearance-section</div>,
}));
vi.mock("../MobileWorkspacePreferencesScreen", () => ({
  MobileWorkspacePreferencesScreen: () => <div>workspace-section</div>,
}));
vi.mock("../MobileAboutScreen", () => ({
  MobileAboutScreen: () => <div>about-section</div>,
}));

const { MobileSettingsScreen } = await import("../MobileSettingsScreen");

function renderMobile() {
  return render(
    <SurfaceProvider surface="mobile">
      <MobileSettingsScreen />
    </SurfaceProvider>,
  );
}

beforeEach(() => {
  hasControlPlane = true;
});

describe("MobileSettingsScreen", () => {
  it("lists every mobile settings section", () => {
    renderMobile();
    expect(screen.getByText("Account")).toBeInTheDocument();
    expect(screen.getByText("AI providers")).toBeInTheDocument();
    expect(screen.getByText("Billing")).toBeInTheDocument();
    expect(screen.getByText("GitHub")).toBeInTheDocument();
    expect(screen.getByText("Notifications")).toBeInTheDocument();
    expect(screen.getByText("Privacy")).toBeInTheDocument();
    expect(screen.getByText("Appearance")).toBeInTheDocument();
    expect(screen.getByText("Workspace preferences")).toBeInTheDocument();
    expect(screen.getByText("About")).toBeInTheDocument();
  });

  it("hides Billing when there is no control-plane backend", () => {
    hasControlPlane = false;
    renderMobile();
    expect(screen.queryByText("Billing")).not.toBeInTheDocument();
  });

  it("hides GitHub when there is no control-plane backend", () => {
    // Same gate as Billing: git credential storage and CloneRepo are both
    // control-plane RPCs (see GitConnectionsSettings's cloud-only branch).
    hasControlPlane = false;
    renderMobile();
    expect(screen.queryByText("GitHub")).not.toBeInTheDocument();
  });

  it("notes what isn't available on mobile", () => {
    renderMobile();
    expect(
      screen.getByText(/MCP servers, prompts, keyboard shortcuts/i),
    ).toBeInTheDocument();
  });

  it("does not render the full desktop settings tree as its own row", () => {
    // `settings` stays false for this surface — the list here is a fixed,
    // narrower set. This is the concrete regression that flags someone
    // adding an MCP/Prompts/Connectors row as a section button (the footer
    // note mentioning them by name in prose is expected and stays).
    renderMobile();
    expect(
      screen.queryByRole("button", { name: /mcp/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /^prompts$/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /connectors/i }),
    ).not.toBeInTheDocument();
  });

  it("drills into a section on tap without a route push", async () => {
    const user = userEvent.setup();
    renderMobile();
    await user.click(screen.getByText("AI providers"));
    expect(screen.getByText("ai-section")).toBeInTheDocument();
    expect(screen.queryByText("Account")).not.toBeInTheDocument();
  });

  it("returns to the list from a section's own back button", async () => {
    const user = userEvent.setup();
    renderMobile();
    await user.click(screen.getByText("Account"));
    expect(screen.getByText("account-section")).toBeInTheDocument();

    await user.click(screen.getByText("back-from-account"));
    expect(screen.getByText("Account")).toBeInTheDocument();
    expect(screen.queryByText("account-section")).not.toBeInTheDocument();
  });

  it("hides every settings section on the embed surface", () => {
    render(
      <SurfaceProvider surface="embed">
        <MobileSettingsScreen />
      </SurfaceProvider>,
    );
    expect(screen.queryByText("Account")).not.toBeInTheDocument();
    expect(screen.queryByText("AI providers")).not.toBeInTheDocument();
  });
});
