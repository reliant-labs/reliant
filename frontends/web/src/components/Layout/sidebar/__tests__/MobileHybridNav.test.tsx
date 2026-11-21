// Copyright (c) 2026 Reliant Labs

/**
 * The phone's nav is the one hybrid model (U6e, owner: drilling in on a phone
 * brought back the old nav): the same rows, the same route-derived drill-ins
 * with their "‹ Area" header, read from the phone's own screens.
 */

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { inboxPath, mobileChatPath, mobileChatsPath, mobileInboxPath, mobileRunsPath, mobileSettingsPath, chatPath, homePath, runsPath, settingsPath } from "@/lib/paths";

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
  ])("%s reads as %s", (phone, desktop) => {
    expect(desktopPathOfMobile(phone)).toBe(desktop);
  });
});

describe("the phone's nav", () => {
  it("is the hybrid top level off an area, Inbox lit on the phone's Inbox", () => {
    renderAt(mobileInboxPath());
    expect(screen.getByTestId("nav-top")).toBeInTheDocument();
    expect(screen.getByTestId("sidebar-inbox-button")).toHaveAttribute("aria-current", "page");
    for (const id of ["agents", "dashboards", "observability", "analytics", "deployments", "data", "storage", "users", "configure"]) {
      expect(screen.getByTestId(`nav-${id}`)).toBeInTheDocument();
    }
    expect(screen.getByTestId("sidebar-settings-button")).toBeInTheDocument();
  });

  it("drills into Agents on the phone's chats, never the old nav, and its back header goes to the Overview", () => {
    const onNavigate = renderAt(mobileChatPath("c1"));
    expect(screen.getByTestId("nav-drill-in")).toHaveAttribute("data-area", "agents");
    expect(screen.queryByTestId("nav-ai-providers")).toBeNull();
    expect(screen.getByTestId("mobile-nav-new-chat")).toBeInTheDocument();
    expect(screen.getByTestId("mobile-nav-chats")).toHaveAttribute("aria-current", "page");
    fireEvent.click(screen.getByTestId("nav-back"));
    expect(onNavigate).toHaveBeenLastCalledWith(consolePath("shopfront", { kind: "overview" }), { drill: true });
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
