// Copyright (c) 2026 Reliant Labs

/**
 * The phone's nav is the one hybrid model (U6e, owner: drilling in on a phone
 * brought back the old nav): the same rows, the same route-derived drill-ins
 * with their "‹ Area" header, read from the phone's own screens.
 */

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import {
  chatPath,
  homePath,
  inboxPath,
  mobileChatPath,
  mobileChatsPath,
  mobileChatWorkflowPath,
  mobileInboxPath,
  mobileRunsPath,
  mobileSettingsPath,
  mobileWorkflowPath,
  mobileWorkflowsPath,
  runPath,
  runsPath,
  settingsPath,
  workflowDetailPath,
  workflowLibraryPath,
} from "@/lib/paths";

import { consolePath, obsOverviewPath } from "../../../Forge/Console/paths";

vi.mock("../shellProject", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../shellProject")>()),
  useShellProject: () => ({ mode: "one", ids: ["p1"], key: "p1", name: "shopfront", projectId: "p1", app: "shopfront", deployed: true, row: undefined, catalog: undefined }),
}));
vi.mock("../../../Forge/Console/useConsoleNavModel", () => ({
  useConsoleNavModel: () => ({ project: "shopfront", page: { kind: "overview" }, env: "prod", envs: [], alertsFiring: 0, starred: [], customDashboards: [] }),
}));
vi.mock("../../../inbox/InboxNavItem", () => ({
  InboxNavItem: ({ href, current, onOpen }: { href: string; current?: boolean; onOpen?: () => void }) => (
    <a href={href} aria-current={current ? "page" : undefined} data-testid="sidebar-inbox-button" onClick={(e) => (e.preventDefault(), onOpen?.())}>
      Inbox
    </a>
  ),
}));

const { MobileHybridNav, desktopPathOfMobile } = await import("../MobileHybridNav");

const renderAt = (pathname: string, onNavigate = vi.fn()) => {
  render(<MobileHybridNav pathname={pathname} search={{}} onNavigate={onNavigate} />);
  return onNavigate;
};

describe("desktopPathOfMobile", () => {
  it.each([
    [mobileChatsPath(), homePath()],
    [mobileChatPath("c1"), chatPath("c1")],
    [mobileInboxPath(), inboxPath()],
    [mobileRunsPath(), runsPath()],
    [mobileSettingsPath(), settingsPath()],
    ["/forge/p/shopfront", "/forge/p/shopfront"],
    // The detail screens: one workflow, and a workflow chat's run.
    [mobileWorkflowsPath(), workflowLibraryPath()],
    [mobileWorkflowPath("deploy"), workflowDetailPath("deploy")],
    [mobileWorkflowPath("builtin://get-it-right"), workflowDetailPath("builtin://get-it-right")],
    [mobileChatWorkflowPath("c1"), runPath("c1")],
  ])("%s reads as %s", (phone, desktop) => {
    expect(desktopPathOfMobile(phone)).toBe(desktop);
  });
});

describe("the phone's nav", () => {
  it("draws the desktop header's order: the org, Find, the project, then the rows", () => {
    render(
      <MobileHybridNav
        pathname={mobileInboxPath()}
        search={{}}
        onNavigate={vi.fn()}
        org={<div data-testid="org-slot" />}
        project={<div data-testid="project-slot" />}
      />,
    );
    const order = ["org-slot", "mobile-nav-find", "project-slot", "nav-top"].map((id) => screen.getByTestId(id));
    for (let i = 1; i < order.length; i++) {
      expect(order[i - 1].compareDocumentPosition(order[i]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    }
  });

  it("is the hybrid top level off an area, Inbox lit on the phone's Inbox", () => {
    renderAt(mobileInboxPath());
    expect(screen.getByTestId("nav-top")).toBeInTheDocument();
    expect(screen.getByTestId("sidebar-inbox-button")).toHaveAttribute("aria-current", "page");
    for (const id of ["agents", "dashboards", "observability", "analytics", "deployments", "data", "storage", "auth", "configure"]) {
      expect(screen.getByTestId(`nav-${id}`)).toBeInTheDocument();
    }
    expect(screen.getByTestId("nav-usage-billing")).toBeInTheDocument();
  });

  it("drills into Agents on the phone's chats, never the old nav", () => {
    renderAt(mobileChatPath("c1"));
    expect(screen.getByTestId("nav-drill-in")).toHaveAttribute("data-area", "agents");
    expect(screen.queryByTestId("nav-ai-providers")).toBeNull();
    expect(screen.getByTestId("mobile-nav-new-chat")).toBeInTheDocument();
    expect(screen.getByTestId("mobile-nav-chats")).toHaveAttribute("aria-current", "page");
  });

  // reliant #3264: Back navigated to backHref, a desktop page (the Overview),
  // which on a phone left the phone shell and closed the drawer.
  it("shows the top level in place on Back from a drill-in, without navigating", () => {
    const onNavigate = renderAt(mobileChatsPath());
    expect(screen.getByTestId("nav-drill-in")).toHaveAttribute("data-area", "agents");
    // The link still names the desktop page, for a new tab.
    expect(screen.getByTestId("nav-back")).toHaveAttribute("href", consolePath("shopfront", { kind: "overview" }));
    fireEvent.click(screen.getByTestId("nav-back"));
    expect(onNavigate).not.toHaveBeenCalled();
    expect(screen.queryByTestId("nav-drill-in")).toBeNull();
    expect(screen.getByTestId("nav-top")).toBeInTheDocument();
    // The top-level areas are reachable from the chat list now, the Inbox included.
    expect(screen.getByTestId("sidebar-inbox-button")).toBeInTheDocument();
    expect(screen.getByTestId("nav-observability")).toBeInTheDocument();
  });

  it("drills into Agents again from the top level Back showed", () => {
    const onNavigate = renderAt(mobileChatsPath());
    fireEvent.click(screen.getByTestId("nav-back"));
    fireEvent.click(screen.getByTestId("nav-agents"));
    expect(onNavigate).toHaveBeenLastCalledWith(expect.any(String), { drill: true });
    // The screen did not change (the chat list is Agents' page), and the drill-in is back.
    expect(screen.getByTestId("nav-drill-in")).toHaveAttribute("data-area", "agents");
  });

  it("lists the Inbox in Agents' drill-in on the phone's chats, a destination that closes the drawer", () => {
    const onNavigate = renderAt(mobileChatPath("c1"));
    const inbox = screen.getByTestId("sidebar-inbox-button");
    expect(screen.getByTestId("nav-slot-agents-inbox")).toContainElement(inbox);
    expect(screen.queryByTestId("nav-slot-inbox")).toBeNull();
    expect(inbox).toHaveAttribute("href", inboxPath());
    expect(inbox).not.toHaveAttribute("aria-current");
    fireEvent.click(inbox);
    // Not a drill: the Inbox is the top level's page, so the drawer closes on it.
    expect(onNavigate).toHaveBeenLastCalledWith(inboxPath());
  });

  it.each([
    ["a workflow's screen", mobileWorkflowPath("deploy"), "nav-workflows-library"],
    ["a workflow chat's run", mobileChatWorkflowPath("c1"), "nav-workflows-runs"],
  ])("drills into Agents on %s, its Workflows tab lit", (_, phone, row) => {
    renderAt(phone);
    expect(screen.getByTestId("nav-drill-in")).toHaveAttribute("data-area", "agents");
    expect(screen.getByTestId(row)).toHaveAttribute("aria-current", "page");
    expect(screen.getByTestId("mobile-nav-chats")).not.toHaveAttribute("aria-current");
  });

  it("drills into Observability, landing on its Overview, with the divider's rows showing", () => {
    const onNavigate = renderAt(inboxPath());
    fireEvent.click(screen.getByTestId("nav-observability"));
    expect(onNavigate).toHaveBeenLastCalledWith(expect.stringContaining(obsOverviewPath("shopfront")), { drill: true });
  });

  it("shows Observability's rows on one of its pages, and a destination is not a drill", () => {
    const onNavigate = renderAt(consolePath("shopfront", { kind: "obs-view", view: "logs" }));
    expect(screen.getByTestId("nav-drill-in")).toHaveAttribute("data-area", "observability");
    expect(screen.getByText("Performance & issues")).toBeInTheDocument();
    fireEvent.click(screen.getByTestId("nav-obs-errors"));
    expect(onNavigate.mock.calls.at(-1)).toHaveLength(1);
  });
});
