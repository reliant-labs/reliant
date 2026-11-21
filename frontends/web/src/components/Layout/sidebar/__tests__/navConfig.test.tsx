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
import { activeArea, backHref, childrenOf, drillInIds, flattenIds, hrefOf, hybridNav, isCurrent, visibleItems, type NavCtx } from "../navConfig";
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
      "dashboards",
      "observability",
      "deployments",
      "data",
      "storage",
      "users",
      "configure",
    ]);
  });

  it("has Settings in the footer, and no footer Inbox", () => {
    expect(hybridNav.footer.map((item) => item.id)).toEqual(["settings"]);
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

  it("Configure is Secrets, Domains, Activity", () => {
    const ctx = ctxAt(inboxPath());
    const configure = hybridNav.items.find((item) => item.id === "configure")!;
    expect(flattenIds([configure], ctx)).toEqual(["configure", "secrets", "domains", "activity"]);
  });

  it("merges Releases and Environments into Deployments, with Previews a URL of its own", () => {
    const ctx = ctxAt(inboxPath());
    const deployments = hybridNav.items.find((item) => item.id === "deployments")!;
    expect(deployments.kind).toBe("group");
    expect(flattenIds([deployments], ctx)).toEqual(["deployments", "releases", "environments", "previews"]);
    const previews = childrenOf(deployments, ctx).find((item) => item.id === "previews")!;
    expect(hrefOf(previews, ctx)).toBe(`${consolePath("shopfront", { kind: "environments" })}?show=previews`);
    // The previews view lights Previews, not Environments.
    const onPreviews = ctxAt(`${consolePath("shopfront", { kind: "environments" })}?show=previews`);
    expect(isCurrent(previews, onPreviews)).toBe(true);
    expect(isCurrent(childrenOf(deployments, onPreviews).find((item) => item.id === "environments")!, onPreviews)).toBe(false);
  });

  it("lists Observability's views, then a Performance & issues divider over Errors … Sessions", () => {
    const ctx = ctxAt(inboxPath());
    const observability = hybridNav.items.find((item) => item.id === "observability")!;
    expect(flattenIds([observability], ctx)).toEqual([
      "observability",
      "obs-graph",
      "obs-metrics",
      "obs-logs",
      "obs-rpc",
      "obs-profiles",
      "performance-heading",
      "obs-errors",
      "obs-alerts",
      "obs-traces",
      "obs-sessions",
    ]);
    // Drilling in lands on its first view, the Service graph.
    expect(hrefOf(observability, ctx)).toBe(consolePath("shopfront", { kind: "obs-view", view: "graph" }));
  });

  it("gives Agents its rows", () => {
    const ids = flattenIds(hybridNav.items, ctxAt(inboxPath()));
    expect(ids).toEqual(expect.arrayContaining(["sandboxes", "workflows-runs", "workflows-library", "slot-chat-list", "slot-new-chat"]));
    // AI providers lives in Settings only (U6e).
    expect(ids).not.toContain("ai-providers");
  });

  it("draws Analytics ▾ for an app with the Product analytics hub, its Insights row lit on a hub dashboard", () => {
    expect(topIds()).not.toContain("analytics");
    const hub = { open: { kind: "dashboard" as const, dashKind: "release" as const, key: "product-users" }, keys: ["product-users"] };
    const console = { project: "shopfront", page: hub.open, env: "prod", envs: [], productAnalytics: hub };
    const ctx = ctxAt(consolePath("shopfront", hub.open), { console });
    expect(topIds(ctx)).toContain("analytics");
    const analytics = hybridNav.items.find((item) => item.id === "analytics")!;
    expect(flattenIds([analytics], ctx)).toEqual(["analytics", "analytics-insights"]);
    expect(isCurrent(childrenOf(analytics, ctx)[0]!, ctx)).toBe(true);
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
    [orgPath({ kind: "obs-view", view: "traces" }), "observability"],
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
    expect(onNavigate).toHaveBeenCalledWith(backHref(ctxAt(logs)), { drill: true });
    rerender(<NavRenderer config={hybridNav} ctx={ctxAt(consolePath("shopfront", { kind: "overview" }))} slots={{}} onNavigate={onNavigate} />);
    expect(screen.queryByTestId("nav-drill-in")).toBeNull();
    expect(screen.getByTestId("sidebar-home-button")).toHaveAttribute("aria-current", "page");
  });

  it("shows Performance & issues as a divider whose rows are always visible", () => {
    renderAt(consolePath("shopfront", { kind: "obs-view", view: "logs" }));
    expect(screen.getByText("Performance & issues")).toBeInTheDocument();
    expect(screen.queryByTestId("nav-performance")).toBeNull();
    for (const id of ["obs-errors", "obs-alerts", "obs-traces", "obs-sessions"]) expect(screen.getByTestId(`nav-${id}`)).toBeInTheDocument();
  });

  it("keeps a drawer open on a drill-in and its back header, not on a destination", () => {
    const onNavigate = vi.fn();
    renderAt(inboxPath(), onNavigate);
    fireEvent.click(screen.getByTestId("nav-observability"));
    expect(onNavigate).toHaveBeenLastCalledWith(consolePath("shopfront", { kind: "obs-view", view: "graph" }), { drill: true });
    fireEvent.click(screen.getByTestId("sidebar-settings-button"));
    expect(onNavigate).toHaveBeenLastCalledWith(settingsPath());
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

    fireEvent.click(screen.getByTestId("nav-deployments"));
    expect(screen.getByTestId("nav-deployments")).toHaveAttribute("aria-expanded", "true");
    fireEvent.click(screen.getByTestId("nav-deployments"));
    expect(screen.getByTestId("nav-deployments")).toHaveAttribute("aria-expanded", "false");

    rerender(<NavRenderer config={hybridNav} ctx={ctx(consolePath("shopfront", { kind: "domains" }))} slots={{}} onNavigate={onNavigate} />);
    expect(configure()).toHaveAttribute("aria-expanded", "true");
  });
});
