/**
 * MobileSettingsScreen is the section list for `/m/settings`. Pins that
 * sections respect their capability gate, billing is hidden without
 * control-plane, the open section is the URL's `?section=` (so a deep link
 * lands on it and Back returns to the list), and each section's own back
 * button returns to the list.
 */

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SurfaceProvider } from "../../../lib/surfaceContext";

let routeSearch: Record<string, string> = {};
const navigate = vi.fn();
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigate,
  useSearch: () => routeSearch,
}));

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
  MobileBillingScreen: ({ onBack, backLabel }: { onBack: () => void; backLabel?: string }) => (
    <div>
      <button onClick={onBack}>{backLabel ?? "back-from-billing"}</button>
      billing-section
    </div>
  ),
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
vi.mock("../MobileDiagnosticsScreen", () => ({
  MobileDiagnosticsScreen: () => <div>diagnostics-section</div>,
}));

const { MobileSettingsScreen } = await import("../MobileSettingsScreen");

const tree = () => (
  <SurfaceProvider surface="mobile">
    <MobileSettingsScreen />
  </SurfaceProvider>
);

// The mocked router: navigate({ href }) becomes the next render's search.
let rerenderTree: (() => void) | undefined;
function renderMobile() {
  const result = render(tree());
  rerenderTree = () => result.rerender(tree());
  return result;
}

beforeEach(() => {
  routeSearch = {};
  navigate.mockReset();
  navigate.mockImplementation(({ href }: { href: string }) => {
    const url = new URL(href, "http://app.test");
    routeSearch = Object.fromEntries(url.searchParams);
    rerenderTree?.();
  });
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
    expect(screen.getByText("Workspaces")).toBeInTheDocument();
    expect(screen.getByText("About")).toBeInTheDocument();
    expect(screen.getByText("Diagnostics")).toBeInTheDocument();
  });

  it("opens Diagnostics, the phone's way to see what went wrong", async () => {
    const user = userEvent.setup();
    renderMobile();
    await user.click(screen.getByText("Diagnostics"));
    expect(screen.getByText("diagnostics-section")).toBeInTheDocument();
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

  it("notes top-level mobile management and what remains desktop-only", () => {
    renderMobile();
    expect(
      screen.getByText(/Machines are in the main menu/i),
    ).toBeInTheDocument();
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

  it("the footer does not call GitHub unavailable while listing it", () => {
    renderMobile();
    expect(screen.getByText("GitHub")).toBeInTheDocument();
    expect(screen.queryByText(/GitHub are available from the main menu/i)).not.toBeInTheDocument();
  });

  it("opens a section by putting it in the URL", async () => {
    const user = userEvent.setup();
    renderMobile();
    await user.click(screen.getByText("AI providers"));
    expect(navigate).toHaveBeenCalledWith({ href: "/m/settings?section=ai" });
    expect(screen.getByText("ai-section")).toBeInTheDocument();
    expect(screen.queryByText("Account")).not.toBeInTheDocument();
  });

  it("opens the section a deep link names", () => {
    routeSearch = { section: "notifications" };
    renderMobile();
    expect(screen.getByText("notifications-section")).toBeInTheDocument();
  });

  it("shows the list for a section it does not have", () => {
    routeSearch = { section: "mcp" };
    renderMobile();
    expect(screen.getByText("Account")).toBeInTheDocument();
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

describe("MobileSettingsScreen opened with a way back (the claim page's Subscribe)", () => {
  it("opens on billing and its back control returns to where the person came from", async () => {
    routeSearch = { section: "billing", returnTo: "/claim" };
    renderMobile();
    expect(screen.getByText("billing-section")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Back to your claim" }));
    expect(navigate).toHaveBeenCalledWith({ href: "/claim" });
  });

  it("ignores a way back that leaves the app, and falls back to the list", async () => {
    routeSearch = { section: "billing", returnTo: "//evil.example/x" };
    renderMobile();
    await userEvent.click(screen.getByRole("button", { name: "back-from-billing" }));
    expect(navigate).toHaveBeenCalledWith({ href: "/m/settings" });
    expect(screen.getByText("Account")).toBeInTheDocument();
  });
});
