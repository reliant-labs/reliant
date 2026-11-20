// Copyright (c) 2026 Reliant Labs

/**
 * The nav variants as data (U6c): each variant's top level, the drill-in the
 * route puts the nav in (and Back out of it), Dashboards appearing once,
 * `?nav=` persistence, and an accordion that collapses on a second click.
 */

import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { consolePath, orgPath } from "../../../Forge/Console/paths";
import { inboxPath, newChatPath, projectChatsPath, runsPath, settingsPath } from "@/lib/paths";
import {
  activeArea,
  backHref,
  flattenIds,
  NAV_CONFIGS,
  resolveNavVariant,
  retainNavParam,
  visibleItems,
  withNavParam,
  type NavCtx,
  type NavVariant,
} from "../navConfig";
import { NavRenderer } from "../NavRenderer";
import { consolePageOf, orgPageOf } from "../../../Forge/Console/paths";

function ctxAt(variant: NavVariant, href: string, over: Partial<NavCtx> = {}): NavCtx {
  const [pathname, query = ""] = href.split("?");
  const search = Object.fromEntries(new URLSearchParams(query));
  return {
    variant,
    pathname,
    search,
    mode: "one",
    projectId: "p1",
    projectName: "shop",
    app: "shop",
    console: null,
    page: consolePageOf(pathname)?.page ?? null,
    orgPage: orgPageOf(pathname),
    ...over,
  };
}

const topIds = (variant: NavVariant, ctx = ctxAt(variant, inboxPath())) =>
  visibleItems(NAV_CONFIGS[variant].items, ctx).filter((item) => item.kind !== "heading").map((item) => item.id);

describe("each variant's top level", () => {
  it("A accordions everything", () => {
    expect(topIds("a")).toEqual([
      "overview",
      "agents",
      "services",
      "storage",
      "dashboards",
      "observability",
      "performance",
      "analytics",
      "releases",
      "data",
      "configure",
    ]);
  });

  it("B is short, with the big areas drilling in", () => {
    expect(topIds("b")).toEqual([
      "overview",
      "agents",
      "services",
      "storage",
      "data",
      "dashboards",
      "observability",
      "analytics",
      "releases",
      "environments",
      "secrets",
      "domains",
      "users",
      "project-usage",
      "activity",
    ]);
  });

  it("C is the hybrid", () => {
    expect(topIds("c")).toEqual(["overview", "agents", "services", "storage", "data", "dashboards", "observability", "analytics", "releases", "configure"]);
  });

  it.each(["a", "b", "c"] as const)("%s: the footer is Inbox, Usage & billing, Settings", (variant) => {
    expect(NAV_CONFIGS[variant].footer.map((item) => item.id)).toEqual(["slot-inbox", "usage-billing", "settings"]);
  });

  it.each(["a", "b", "c"] as const)("%s: Dashboards appears exactly once, at the top level (bug 4)", (variant) => {
    const ctx = ctxAt(variant, inboxPath());
    const ids = [...flattenIds(NAV_CONFIGS[variant].items, ctx), ...flattenIds(NAV_CONFIGS[variant].footer, ctx)];
    expect(ids.filter((id) => id === "dashboards")).toHaveLength(1);
    expect(topIds(variant)).toContain("dashboards");
  });

  it.each(["a", "b", "c"] as const)("%s: all projects keeps the same nav, minus nothing that reads across projects", (variant) => {
    const all = ctxAt(variant, inboxPath(), { mode: "all", projectId: null, app: "shop" });
    expect(topIds(variant, all)).toEqual(topIds(variant));
  });
});

describe("drill-in is derived from the route", () => {
  const settings = settingsPath("ai");
  const chat = projectChatsPath("p1");
  const logs = consolePath("shop", { kind: "obs-view", view: "logs" });
  const events = consolePath("shop", { kind: "events" });

  it.each([
    ["a", settings, "settings"],
    ["a", chat, null],
    ["a", logs, null],
    ["a", events, null],
    ["b", settings, "settings"],
    ["b", chat, "agents"],
    ["b", newChatPath(), "agents"],
    ["b", runsPath(), "agents"],
    ["b", logs, "observability"],
    ["b", orgPath({ kind: "obs-view", view: "traces" }), "observability"],
    ["b", events, "analytics"],
    ["b", inboxPath(), null],
    ["c", chat, "agents"],
    ["c", logs, "observability"],
    ["c", events, null],
    ["c", settings, "settings"],
  ] as const)("%s at %s → %s", (variant, href, area) => {
    expect(activeArea(NAV_CONFIGS[variant], ctxAt(variant, href))?.id ?? null).toBe(area);
  });

  it("Dashboards is a top-level page, never inside Observability", () => {
    const ctx = ctxAt("b", consolePath("shop", { kind: "obs-view", view: "dashboards" }));
    expect(activeArea(NAV_CONFIGS.b, ctx)).toBeNull();
  });

  it("Back goes to the project's Overview, or the summary across projects", () => {
    expect(backHref(ctxAt("b", settingsPath("ai")))).toBe(consolePath("shop", { kind: "overview" }));
    expect(backHref(ctxAt("b", settingsPath("ai"), { mode: "many" }))).not.toBe(consolePath("shop", { kind: "overview" }));
  });

  it("renders the area's rows in place of the nav, and the top level again on the Overview", () => {
    const onNavigate = vi.fn();
    const { rerender } = render(
      <NavRenderer config={NAV_CONFIGS.b} ctx={ctxAt("b", consolePath("shop", { kind: "obs-view", view: "logs" }))} explicitVariant={false} slots={{}} onNavigate={onNavigate} />,
    );
    expect(screen.getByTestId("nav-drill-in")).toHaveAttribute("data-area", "observability");
    expect(screen.queryByTestId("nav-top")).toBeNull();
    expect(screen.getByTestId("nav-obs-logs")).toHaveAttribute("aria-current", "page");
    fireEvent.click(screen.getByTestId("nav-back"));
    expect(onNavigate).toHaveBeenCalledWith(consolePath("shop", { kind: "overview" }));

    // Back lands on the Overview: the route says top level.
    rerender(<NavRenderer config={NAV_CONFIGS.b} ctx={ctxAt("b", consolePath("shop", { kind: "overview" }))} explicitVariant={false} slots={{}} onNavigate={onNavigate} />);
    expect(screen.queryByTestId("nav-drill-in")).toBeNull();
    expect(screen.getByTestId("sidebar-home-button")).toHaveAttribute("aria-current", "page");
  });

  it("Settings replaces the nav in every variant", () => {
    for (const variant of ["a", "b", "c"] as const) {
      const { unmount } = render(<NavRenderer config={NAV_CONFIGS[variant]} ctx={ctxAt(variant, settingsPath("ai"))} explicitVariant={false} slots={{}} onNavigate={vi.fn()} />);
      expect(screen.getByTestId("nav-drill-in")).toHaveAttribute("data-area", "settings");
      expect(screen.getByTestId("nav-settings-ai")).toHaveAttribute("aria-current", "page");
      unmount();
    }
  });
});

describe("accordions (bug 3)", () => {
  it("clicking an open group collapses it, the current one too; navigating reopens the group holding the page", () => {
    const props = { config: NAV_CONFIGS.a, explicitVariant: false, slots: {}, onNavigate: vi.fn() };
    const { rerender } = render(<NavRenderer {...props} ctx={ctxAt("a", runsPath())} />);
    const agents = screen.getByTestId("nav-agents");
    expect(agents).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByTestId("nav-workflows-runs")).toHaveAttribute("aria-current", "page");

    fireEvent.click(agents);
    expect(screen.getByTestId("nav-agents")).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByTestId("nav-workflows-runs")).toBeNull();

    // A closed group opens on click, and closes again.
    fireEvent.click(screen.getByTestId("nav-configure"));
    expect(screen.getByTestId("nav-configure")).toHaveAttribute("aria-expanded", "true");
    fireEvent.click(screen.getByTestId("nav-configure"));
    expect(screen.getByTestId("nav-configure")).toHaveAttribute("aria-expanded", "false");

    rerender(<NavRenderer {...props} ctx={ctxAt("a", projectChatsPath("p1"))} />);
    expect(screen.getByTestId("nav-agents")).toHaveAttribute("aria-expanded", "true");
  });
});

describe("?nav= persistence", () => {
  beforeEach(() => sessionStorage.clear());

  it("defaults to b; the URL wins; the tab remembers", () => {
    expect(resolveNavVariant({})).toEqual({ variant: "b", explicit: false });
    expect(resolveNavVariant({ nav: "a" })).toEqual({ variant: "a", explicit: true });
    expect(resolveNavVariant({ nav: "zz" }).variant).toBe("b");
    sessionStorage.setItem("shell.navVariant", "c");
    expect(resolveNavVariant({})).toEqual({ variant: "c", explicit: true });
    expect(resolveNavVariant({ nav: "a" }).variant).toBe("a");
  });

  it("the root middleware keeps nav on a navigation that does not name it", () => {
    expect(retainNavParam({ search: { nav: "a" }, next: () => ({ q: "x" }) })).toEqual({ q: "x", nav: "a" });
    expect(retainNavParam({ search: { nav: "a" }, next: () => ({ nav: "c" }) })).toEqual({ nav: "c" });
    expect(retainNavParam({ search: {}, next: () => ({}) })).toEqual({});
    sessionStorage.setItem("shell.navVariant", "c");
    expect(retainNavParam({ search: {}, next: () => ({}) })).toEqual({ nav: "c" });
  });

  it("links carry the variant once one is chosen", () => {
    expect(withNavParam("/inbox", "a")).toBe("/inbox?nav=a");
    expect(withNavParam("/forge/p/shop?env=prod#x", "c")).toBe("/forge/p/shop?env=prod&nav=c#x");
  });
});
