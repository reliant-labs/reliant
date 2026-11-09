// Copyright (c) 2025 Reliant Labs

/**
 * The console sidebar as the reader sees it: Observability is expanded on
 * every page, its chevron collapses it, the choice is remembered, and the
 * collapsed icon rail is unchanged. Rendered through the real forge-ui
 * SidebarLayout with the nav the console builds, wired the way ConsoleLayout
 * wires it (useObsNavOpen).
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

import SidebarLayout from "@/components/forge-ui/sidebar_layout";

import { buildConsoleNav } from "../ConsoleShell";
import { OBS_NAV_OPEN_KEY, useObsNavOpen } from "../obsNavOpen";
import type { ConsolePage } from "../paths";

const navigate = vi.fn();
vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => navigate,
}));

const ENVS = [{ name: "prod", preview: false }];

function Sidebar({ page, rail = false, remembered = true }: { page: ConsolePage; rail?: boolean; remembered?: boolean }) {
  const [obsOpen, setObsOpen] = useObsNavOpen();
  const { items, footer } = buildConsoleNav({
    project: "shop",
    page,
    env: "prod",
    envs: ENVS,
    ...(remembered ? { obsOpen, onObsOpenChange: setObsOpen } : {}),
  });
  return (
    <SidebarLayout brand="shop" navItems={items} footerItems={footer} collapsed={rail} onToggle={() => {}}>
      page
    </SidebarLayout>
  );
}

const obsToggle = () => screen.getByRole("button", { name: "Observability pages" });
const logsLink = () => screen.queryByRole("link", { name: "Logs" });

beforeEach(() => {
  window.localStorage.clear();
  navigate.mockClear();
});

describe("the console sidebar's Observability group", () => {
  it("is expanded on a page outside it", () => {
    render(<Sidebar page={{ kind: "overview" }} />);
    expect(obsToggle()).toHaveAttribute("aria-expanded", "true");
    expect(logsLink()).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Alerts" })).toBeInTheDocument();
    // The accordion groups are unchanged: Environments opens only on its own pages.
    expect(screen.queryByRole("link", { name: "prod" })).not.toBeInTheDocument();
  });

  it("collapses from its chevron without navigating, and the choice is remembered", () => {
    const { unmount } = render(<Sidebar page={{ kind: "overview" }} />);
    fireEvent.click(obsToggle());
    expect(obsToggle()).toHaveAttribute("aria-expanded", "false");
    expect(logsLink()).not.toBeInTheDocument();
    expect(navigate).not.toHaveBeenCalled();
    expect(window.localStorage.getItem(OBS_NAV_OPEN_KEY)).toBe("0");

    unmount();
    render(<Sidebar page={{ kind: "secrets" }} />);
    expect(obsToggle()).toHaveAttribute("aria-expanded", "false");
    expect(logsLink()).not.toBeInTheDocument();

    fireEvent.click(obsToggle());
    expect(logsLink()).toBeInTheDocument();
    expect(window.localStorage.getItem(OBS_NAV_OPEN_KEY)).toBe("1");
  });

  it("stays collapsed on its own pages when the reader chose so, marking the parent instead", () => {
    window.localStorage.setItem(OBS_NAV_OPEN_KEY, "0");
    render(<Sidebar page={{ kind: "obs-view", view: "logs" }} />);
    expect(logsLink()).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Observability" })).toHaveClass("bg-ink/8");
  });

  it("reopens when its row is followed", () => {
    window.localStorage.setItem(OBS_NAV_OPEN_KEY, "0");
    render(<Sidebar page={{ kind: "overview" }} />);
    fireEvent.click(screen.getByRole("link", { name: "Observability" }));
    expect(navigate).toHaveBeenCalledWith({ to: "/forge/p/shop/observability/dashboards", search: { env: "prod" } });
    expect(logsLink()).toBeInTheDocument();
    expect(window.localStorage.getItem(OBS_NAV_OPEN_KEY)).toBe("1");
  });

  it("toggles for the visit when no choice is kept (the previews, the demo)", () => {
    render(<Sidebar page={{ kind: "overview" }} remembered={false} />);
    expect(logsLink()).toBeInTheDocument();
    fireEvent.click(obsToggle());
    expect(logsLink()).not.toBeInTheDocument();
    expect(window.localStorage.getItem(OBS_NAV_OPEN_KEY)).toBeNull();
  });

  it("leaves the icon rail as it was: no sub-pages, no chevron", () => {
    render(<Sidebar page={{ kind: "obs-view", view: "logs" }} rail />);
    expect(logsLink()).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Observability pages" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Observability" })).toHaveClass("bg-ink/8");
  });
});
