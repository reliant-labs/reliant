// Copyright (c) 2025 Reliant Labs

/**
 * The console sidebar as the reader sees it: Observability is expanded on
 * every page, its chevron collapses it, the choice is remembered, and the
 * collapsed icon rail is unchanged. Rendered through the real forge-ui
 * SidebarLayout with the nav the console builds, wired the way ConsoleLayout
 * wires it (useObsNavOpen).
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

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
afterEach(() => vi.unstubAllGlobals());

function mobileViewport() {
  vi.stubGlobal("matchMedia", vi.fn(() => ({
    matches: true,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  })));
}

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

  it("cycles mobile keyboard focus inside the live nav, not the page or external portals", async () => {
    mobileViewport();
    const user = userEvent.setup();
    render(<><button>Outside shell</button><Sidebar page={{ kind: "overview" }} /></>);
    const trigger = screen.getByRole("button", { name: "Open navigation" });
    await user.click(trigger);
    const drawer = screen.getByRole("dialog");
    const first = within(drawer).getAllByRole("link")[0];
    const last = within(drawer).getByRole("link", { name: "Activity" });
    // jsdom has no Tailwind layout. Model the desktop-only collapse button's CSS.
    within(drawer).getByRole("button", { name: "Collapse sidebar" }).style.display = "none";
    expect(first).toHaveFocus();
    last.focus();
    await user.tab();
    expect(first).toHaveFocus();
    await user.tab({ shift: true });
    expect(last).toHaveFocus();
    expect(trigger.closest("[inert]")).not.toBeNull();
    await user.click(obsToggle());
    expect(logsLink()).not.toBeInTheDocument();
    last.focus();
    await user.tab();
    expect(first).toHaveFocus();
    screen.getByRole("button", { name: "Outside shell" }).focus();
    await user.tab();
    expect(first).toHaveFocus();
    screen.getByRole("button", { name: "Outside shell" }).focus();
    await user.tab({ shift: true });
    expect(last).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(trigger.closest("[inert]")).toBeNull();
    expect(trigger).toHaveFocus();
  });

  it("keeps an empty mobile drawer focusable and restores its trigger on scrim close", async () => {
    mobileViewport();
    const user = userEvent.setup();
    render(<SidebarLayout brand="shop" navItems={[]}>page</SidebarLayout>);
    const trigger = screen.getByRole("button", { name: "Open navigation" });
    await user.click(trigger);
    const drawer = screen.getByRole("dialog");
    within(drawer).getByRole("button", { name: "Collapse sidebar" }).style.display = "none";
    drawer.focus();
    await user.tab();
    expect(drawer).toHaveFocus();
    await user.tab({ shift: true });
    expect(drawer).toHaveFocus();
    fireEvent.click(screen.getByRole("presentation"));
    expect(trigger).toHaveFocus();
    expect(trigger.closest("[inert]")).toBeNull();
  });

  it("does not contain desktop keyboard focus", async () => {
    const user = userEvent.setup();
    render(<><Sidebar page={{ kind: "overview" }} /><button>Outside shell</button></>);
    const last = screen.getByRole("button", { name: "Collapse sidebar" });
    last.focus();
    await user.tab();
    expect(screen.getByRole("button", { name: "Outside shell" })).toHaveFocus();
    expect(document.querySelector("[inert]")).toBeNull();
  });

  it("leaves the icon rail as it was: no sub-pages, no chevron", () => {
    render(<Sidebar page={{ kind: "obs-view", view: "logs" }} rail />);
    expect(logsLink()).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Observability pages" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Observability" })).toHaveClass("bg-ink/8");
  });
});
