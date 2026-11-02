/**
 * MobileSettingsScreen is the section list for `/m/settings`. Pins that
 * sections respect their capability gate, billing is hidden without
 * control-plane, drill-in is URL-backed, and each
 * section's own back button returns to the list.
 */

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { act } from "@testing-library/react";
import { mobilePathFor } from "../../../lib/mobileRedirect";

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

async function renderMobile(initialEntry = "/m/settings", surface: "mobile" | "embed" = "mobile") {
  const root = createRootRoute();
  const settings = createRoute({
    getParentRoute: () => root,
    path: "/m/settings",
    validateSearch: (search: Record<string, unknown>): { section?: string; returnTo?: string } => ({
      section: typeof search.section === "string" ? search.section : undefined,
      returnTo: typeof search.returnTo === "string" ? search.returnTo : undefined,
    }),
    component: () => <SurfaceProvider surface={surface}><MobileSettingsScreen /></SurfaceProvider>,
  });
  const claim = createRoute({ getParentRoute: () => root, path: "/claim", component: () => <div>claim-page</div> });
  const router = createRouter({
    routeTree: root.addChildren([settings, claim]),
    history: createMemoryHistory({ initialEntries: [initialEntry] }),
  });
  await act(async () => {
    await router.load();
    render(<RouterProvider router={router} />);
  });
  return router;
}

beforeEach(() => {
  hasControlPlane = true;
});

describe("MobileSettingsScreen", () => {
  it("lists every mobile settings section", async () => {
    await renderMobile();
    expect(screen.getByText("Account")).toBeInTheDocument();
    expect(screen.getByText("AI providers")).toBeInTheDocument();
    expect(screen.getByText("Billing")).toBeInTheDocument();
    expect(screen.getByText("GitHub")).toBeInTheDocument();
    expect(screen.getByText("Notifications")).toBeInTheDocument();
    expect(screen.getByText("Privacy")).toBeInTheDocument();
    expect(screen.getByText("Appearance")).toBeInTheDocument();
    expect(screen.getByText("Workspace preferences")).toBeInTheDocument();
    expect(screen.getByText("About")).toBeInTheDocument();
    expect(screen.getByText("Diagnostics")).toBeInTheDocument();
  });

  it("opens Diagnostics, the phone's way to see what went wrong", async () => {
    const user = userEvent.setup();
    await renderMobile();
    await user.click(screen.getByText("Diagnostics"));
    expect(screen.getByText("diagnostics-section")).toBeInTheDocument();
  });

  it("hides Billing when there is no control-plane backend", async () => {
    hasControlPlane = false;
    await renderMobile();
    expect(screen.queryByText("Billing")).not.toBeInTheDocument();
  });

  it("hides GitHub when there is no control-plane backend", async () => {
    // Same gate as Billing: git credential storage and CloneRepo are both
    // control-plane RPCs (see GitConnectionsSettings's cloud-only branch).
    hasControlPlane = false;
    await renderMobile();
    expect(screen.queryByText("GitHub")).not.toBeInTheDocument();
  });

  it("notes top-level mobile management and what remains desktop-only", async () => {
    await renderMobile();
    expect(
      screen.getByText(/Machines and GitHub are available from the main menu/i),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/MCP servers, prompts, keyboard shortcuts/i),
    ).toBeInTheDocument();
  });

  it("does not render the full desktop settings tree as its own row", async () => {
    // `settings` stays false for this surface — the list here is a fixed,
    // narrower set. This is the concrete regression that flags someone
    // adding an MCP/Prompts/Connectors row as a section button (the footer
    // note mentioning them by name in prose is expected and stays).
    await renderMobile();
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

  it("opens the existing AI screen via the desktop redirect href and returns to settings", async () => {
    const router = await renderMobile();
    await act(async () => { await router.navigate({ href: mobilePathFor("/settings/ai"), replace: true }); });
    expect(router.state.location.pathname).toBe("/m/settings");
    expect(router.state.location.search).toEqual({ section: "ai" });
    expect(screen.getByText("ai-section")).toBeInTheDocument();
    await userEvent.click(screen.getByText("back-from-ai"));
    expect(router.state.location.search).toEqual({});
    expect(screen.getByText("AI providers")).toBeInTheDocument();
  });

  it("restores a bookmarked section and responds to browser back/forward", async () => {
    const router = await renderMobile("/m/settings?section=ai");
    expect(screen.getByText("ai-section")).toBeInTheDocument();
    await act(async () => { await router.navigate({ to: "/m/settings", search: {} }); });
    expect(screen.getByText("AI providers")).toBeInTheDocument();
    await act(async () => { router.history.back(); });
    expect(await screen.findByText("ai-section")).toBeInTheDocument();
    await act(async () => { router.history.forward(); });
    expect(await screen.findByText("AI providers")).toBeInTheDocument();
  });

  it.each(["unknown", "billing", "github"])("falls back to the list for unavailable section %s", async (section) => {
    hasControlPlane = false;
    await renderMobile(`/m/settings?section=${section}`);
    expect(screen.getByText("AI providers")).toBeInTheDocument();
    expect(screen.queryByText(`${section}-section`)).not.toBeInTheDocument();
  });

  it("drills into a section on tap using search", async () => {
    const user = userEvent.setup();
    await renderMobile();
    await user.click(screen.getByText("AI providers"));
    expect(screen.getByText("ai-section")).toBeInTheDocument();
    expect(screen.queryByText("Account")).not.toBeInTheDocument();
  });

  it("returns to the list from a section's own back button", async () => {
    const user = userEvent.setup();
    await renderMobile();
    await user.click(screen.getByText("Account"));
    expect(screen.getByText("account-section")).toBeInTheDocument();

    await user.click(screen.getByText("back-from-account"));
    expect(screen.getByText("Account")).toBeInTheDocument();
    expect(screen.queryByText("account-section")).not.toBeInTheDocument();
  });

  it("hides every settings section on the embed surface", async () => {
    await renderMobile("/m/settings?section=ai", "embed");
    expect(screen.queryByText("ai-section")).not.toBeInTheDocument();
    expect(screen.queryByText("Account")).not.toBeInTheDocument();
    expect(screen.queryByText("AI providers")).not.toBeInTheDocument();
  });
});

describe("MobileSettingsScreen opened with a way back (the claim page's Subscribe)", () => {
  it("opens on billing and its back control returns to where the person came from", async () => {
    const router = await renderMobile(`/m/settings?${new URLSearchParams({ section: "billing", returnTo: "/claim" })}`);
    expect(screen.getByText("billing-section")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Back to your claim" }));
    expect(router.state.location.pathname).toBe("/claim");
    expect(await screen.findByText("claim-page")).toBeInTheDocument();
  });

  it("ignores a way back that leaves the app, and falls back to the list", async () => {
    const router = await renderMobile(`/m/settings?${new URLSearchParams({ section: "billing", returnTo: "//evil.example/x" })}`);
    await userEvent.click(screen.getByRole("button", { name: "back-from-billing" }));
    expect(router.state.location.pathname).toBe("/m/settings");
    expect(router.state.location.search).toEqual({});
    expect(screen.getByText("AI providers")).toBeInTheDocument();
  });
});
