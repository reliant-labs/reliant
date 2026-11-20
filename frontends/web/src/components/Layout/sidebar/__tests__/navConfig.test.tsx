// Copyright (c) 2026 Reliant Labs

/**
 * The owner's hybrid nav (U6d): its top-level order, that only Agents and
 * Observability drill in (route-derived, Back restores the top level),
 * Settings is a plain link whose pages show their own sections, accordions
 * collapse on a second click, Dashboards and Inbox appear once, and a stale
 * `?nav=` changes nothing.
 */

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { inboxPath, newChatPath, projectChatsPath, runsPath, settingsPath } from "@/lib/paths";

import { consolePageOf, consolePath, orgPageOf, orgPath } from "../../../Forge/Console/paths";
import { activeArea, backHref, drillInIds, flattenIds, hybridNav, visibleItems, type NavCtx } from "../navConfig";
import { NavRenderer } from "../NavRenderer";

function ctxAt(href: string, over: Partial<NavCtx> = {}): NavCtx {
  const [pathname, query = ""] = href.split("?");
  return {
    pathname,
    search: Object.fromEntries(new URLSearchParams(query)),
    mode: "one",
    projectId: "p1",
    projectName: "shopfront",
    app: "shopfront",
    console: null,
    page: consolePageOf(pathname)?.page ?? null,
    orgPage: orgPageOf(pathname),
    ...over,
  };
}

const topIds = (ctx = ctxAt(inboxPath())) => visibleItems(hybridNav.items, ctx).map((item) => item.id);
const renderAt = (href: string, onNavigate = vi.fn()) => render(<NavRenderer config={hybridNav} ctx={ctxAt(href)} slots={{}} onNavigate={onNavigate} />);

describe("the hybrid nav's shape", () => {
  it("has the owner's top-level order", () => {
    expect(topIds()).toEqual([
      "overview",
      "slot-inbox",
      "agents",
      "services",
      "data",
      "storage",
      "auth",
      "dashboards",
      "observability",
      "analytics",
      "security",
      "releases",
      "configure",
    ]);
  });

  it("has Usage & billing and Settings in the footer, and no footer Inbox", () => {
    expect(hybridNav.footer.map((item) => item.id)).toEqual(["usage-billing", "settings"]);
    expect(hybridNav.footer.find((item) => item.id === "settings")?.kind).toBe("row");
  });

  it("drills in only for Agents and Observability", () => {
    expect(drillInIds(hybridNav)).toEqual(["agents", "observability"]);
  });

  it("lists Dashboards and Inbox exactly once", () => {
    const ctx = ctxAt(inboxPath());
    const ids = [...flattenIds(hybridNav.items, ctx), ...flattenIds(hybridNav.footer, ctx)];
    expect(ids.filter((id) => id === "dashboards")).toHaveLength(1);
    expect(ids.filter((id) => id === "slot-inbox")).toHaveLength(1);
  });

  it("keeps Sign-in users out of Configure (it is Auth now), and Configure is Environments, Secrets, Domains, Usage", () => {
    const ctx = ctxAt(inboxPath());
    const configure = hybridNav.items.find((item) => item.id === "configure")!;
    expect(flattenIds([configure], ctx)).toEqual(["configure", "environments", "secrets", "domains", "project-usage"]);
  });

  it("puts Speed insights and Sessions in Performance & issues, inside the Observability drill-in", () => {
    const ctx = ctxAt(inboxPath());
    const observability = hybridNav.items.find((item) => item.id === "observability")!;
    expect(flattenIds([observability], ctx)).toEqual([
      "observability",
      "obs-graph",
      "obs-metrics",
      "obs-logs",
      "obs-rpc",
      "obs-profiles",
      "performance",
      "obs-errors",
      "obs-alerts",
      "obs-traces",
      "speed-insights",
      "obs-sessions",
    ]);
  });

  it("gives Agents a Connectors row and Analytics its tabs", () => {
    const ctx = ctxAt(inboxPath());
    const ids = flattenIds(hybridNav.items, ctx);
    expect(ids).toEqual(expect.arrayContaining(["connectors", "sandboxes", "ai-providers", "workflows-runs", "workflows-library", "slot-chat-list", "slot-new-chat"]));
    const analytics = hybridNav.items.find((item) => item.id === "analytics")!;
    expect(flattenIds([analytics], ctx)).toEqual(["analytics", "analytics-overview", "analytics-users", "analytics-product", "analytics-acquisition", "analytics-events"]);
  });

  it("keeps the same top level across several projects", () => {
    expect(topIds(ctxAt(inboxPath(), { mode: "all", projectId: null }))).toEqual(topIds());
  });
});

describe("drill-in is derived from the route", () => {
  const logs = consolePath("shopfront", { kind: "obs-view", view: "logs" });
  it.each([
    [projectChatsPath("p1"), "agents"],
    [newChatPath(), "agents"],
    [runsPath(), "agents"],
    [logs, "observability"],
    [consolePath("shopfront", { kind: "speed-insights" }), "observability"],
    [orgPath({ kind: "obs-view", view: "traces" }), "observability"],
    [consolePath("shopfront", { kind: "analytics" }), null],
    [consolePath("shopfront", { kind: "obs-view", view: "dashboards" }), null],
    [inboxPath(), null],
    [settingsPath("ai"), "settings"],
  ] as const)("%s → %s", (href, area) => {
    expect(activeArea(hybridNav, ctxAt(href))?.id ?? null).toBe(area);
  });

  it("ignores a stale ?nav= in the URL", () => {
    expect(activeArea(hybridNav, ctxAt(`${inboxPath()}?nav=a`))).toBeNull();
    expect(activeArea(hybridNav, ctxAt(`${logs}?nav=b`))?.id).toBe("observability");
  });

  it("replaces the nav with Observability's rows, and Back lands on the top level", () => {
    const onNavigate = vi.fn();
    const { rerender } = renderAt(logs, onNavigate);
    expect(screen.getByTestId("nav-drill-in")).toHaveAttribute("data-area", "observability");
    expect(screen.queryByTestId("nav-top")).toBeNull();
    expect(screen.getByTestId("nav-obs-logs")).toHaveAttribute("aria-current", "page");
    fireEvent.click(screen.getByTestId("nav-back"));
    expect(onNavigate).toHaveBeenCalledWith(backHref(ctxAt(logs)));
    rerender(<NavRenderer config={hybridNav} ctx={ctxAt(consolePath("shopfront", { kind: "overview" }))} slots={{}} onNavigate={onNavigate} />);
    expect(screen.queryByTestId("nav-drill-in")).toBeNull();
    expect(screen.getByTestId("sidebar-home-button")).toHaveAttribute("aria-current", "page");
  });

  it("opens Performance & issues inside the drill-in on one of its pages", () => {
    renderAt(consolePath("shopfront", { kind: "speed-insights" }));
    expect(screen.getByTestId("nav-performance")).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByTestId("nav-speed-insights")).toHaveAttribute("aria-current", "page");
  });
});

describe("Settings", () => {
  it("is a plain link, not a drill-in row", () => {
    const onNavigate = vi.fn();
    renderAt(inboxPath(), onNavigate);
    const settings = screen.getByTestId("sidebar-settings-button");
    expect(settings).toHaveAttribute("href", settingsPath());
    expect(settings).not.toHaveAttribute("aria-expanded");
    fireEvent.click(settings);
    expect(onNavigate).toHaveBeenCalledWith(settingsPath());
  });

  it("shows its sections in the nav column on its pages, and stays lit", () => {
    renderAt(settingsPath("ai"));
    expect(screen.getByTestId("nav-drill-in")).toHaveAttribute("data-area", "settings");
    expect(screen.getByTestId("nav-settings-ai")).toHaveAttribute("aria-current", "page");
    expect(screen.getByTestId("sidebar-settings-button")).toHaveAttribute("aria-current", "page");
  });
});

describe("accordions", () => {
  it("collapse on a second click, the current one too; navigating reopens the group holding the page", () => {
    const onNavigate = vi.fn();
    const ctx = (href: string) => ctxAt(href);
    const { rerender } = render(<NavRenderer config={hybridNav} ctx={ctx(consolePath("shopfront", { kind: "secrets" }))} slots={{}} onNavigate={onNavigate} />);
    const configure = () => screen.getByTestId("nav-configure");
    expect(configure()).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByTestId("nav-secrets")).toHaveAttribute("aria-current", "page");
    fireEvent.click(configure());
    expect(configure()).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByTestId("nav-secrets")).toBeNull();

    fireEvent.click(screen.getByTestId("nav-security"));
    expect(screen.getByTestId("nav-security")).toHaveAttribute("aria-expanded", "true");
    fireEvent.click(screen.getByTestId("nav-security"));
    expect(screen.getByTestId("nav-security")).toHaveAttribute("aria-expanded", "false");

    rerender(<NavRenderer config={hybridNav} ctx={ctx(consolePath("shopfront", { kind: "domains" }))} slots={{}} onNavigate={onNavigate} />);
    expect(configure()).toHaveAttribute("aria-expanded", "true");
  });
});
