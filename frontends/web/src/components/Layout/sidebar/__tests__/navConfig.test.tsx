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

import { inboxPath, machineSettingsPath, newChatPath, projectChatsPath, runsPath, sandboxesPath, settingsPath } from "@/lib/paths";

import { getVisibleSettingsSectionIds, SETTINGS_SECTIONS_WITH_OTHER_NAV_HOME } from "../../../Settings/SettingsNavigation";

import { registerConsoleNavEntry } from "../../../Forge/Console/consoleNavRegistry";
import { consolePageOf, consolePath, obsOverviewPath, orgPageOf, orgPath } from "../../../Forge/Console/paths";
import { activeArea, backHref, childrenOf, drillInIds, flattenIds, hrefOf, hybridNav, isCurrent, visibleItems, type NavCtx } from "../navConfig";
import { NavRenderer } from "../NavRenderer";

const changesUI = vi.hoisted(() => ({ on: false }));
vi.mock("@/lib/changesFeature", () => ({ changesUIEnabled: () => changesUI.on }));

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
      "analytics",
      "deployments",
      "tests",
      "data",
      "storage",
      "auth",
      "configure",
    ]);
  });

  it("puts Tests right after Deployments", () => {
    const ids = topIds();
    expect(ids.indexOf("tests")).toBe(ids.indexOf("deployments") + 1);
  });

  it("draws Security ▾ only once a page registers into it, with that page's row", () => {
    const console = { project: "shopfront", page: { kind: "secrets" as const }, env: "prod", envs: [] };
    const ctx = ctxAt(consolePath("shopfront", { kind: "secrets" }), { console });
    expect(flattenIds(hybridNav.items, ctx)).not.toContain("security");
    const unregister = registerConsoleNavEntry({ id: "advisories", label: "Advisories", segment: "advisories", after: "more-tools", group: "security" });
    try {
      expect(topIds(ctx)).toEqual(["overview", "slot-inbox", "agents", "dashboards", "observability", "analytics", "deployments", "tests", "data", "storage", "auth", "security", "configure"]);
      expect(flattenIds(hybridNav.items, ctx)).toContain("registered-advisories");
      // Not in Analytics: a Security row joins Security alone.
      const analytics = hybridNav.items.find((item) => item.id === "analytics")!;
      expect(childrenOf(analytics, ctx).map((item) => item.id)).not.toContain("registered-advisories");
    } finally {
      unregister();
    }
  });

  it("has Usage & billing, Help and Settings in the footer, and no footer Inbox", () => {
    expect(hybridNav.footer.map((item) => item.id)).toEqual(["usage-billing", "slot-help", "settings"]);
    expect(hybridNav.footer.find((item) => item.id === "settings")?.kind).toBe("row");
  });

  it("drills in only for Agents and Observability", () => {
    expect(drillInIds(hybridNav)).toEqual(["agents", "observability"]);
  });

  it("lists Dashboards once, and Inbox once at the top level and once in Agents", () => {
    const ctx = ctxAt(inboxPath());
    const ids = [...flattenIds(hybridNav.items, ctx), ...flattenIds(hybridNav.footer, ctx)];
    expect(ids.filter((id) => id === "dashboards")).toHaveLength(1);
    expect(ids.filter((id) => id === "slot-inbox")).toHaveLength(1);
    expect(ids.filter((id) => id === "slot-agents-inbox")).toHaveLength(1);
    // Ids stay unique within the config, the two Inbox rows included.
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("keeps Sign-in users out of Configure (it is Auth now), and Configure is Secrets, Domains, Usage, Activity", () => {
    const ctx = ctxAt(inboxPath());
    const configure = hybridNav.items.find((item) => item.id === "configure")!;
    expect(flattenIds([configure], ctx)).toEqual(["configure", "secrets", "domains", "project-usage", "activity"]);
  });

  it("merges Services, Releases and Environments into Deployments, with Previews a URL of its own", () => {
    const ctx = ctxAt(inboxPath());
    const deployments = hybridNav.items.find((item) => item.id === "deployments")!;
    expect(deployments.kind).toBe("group");
    expect(flattenIds([deployments], ctx)).toEqual(["deployments", "services", "releases", "environments", "previews"]);
    const previews = childrenOf(deployments, ctx).find((item) => item.id === "previews")!;
    expect(hrefOf(previews, ctx)).toBe(`${consolePath("shopfront", { kind: "environments" })}?show=previews`);
    // The previews view lights Previews, not Environments.
    const onPreviews = ctxAt(`${consolePath("shopfront", { kind: "environments" })}?show=previews`);
    expect(isCurrent(previews, onPreviews)).toBe(true);
    expect(isCurrent(childrenOf(deployments, onPreviews).find((item) => item.id === "environments")!, onPreviews)).toBe(false);
  });

  it.each([
    consolePath("shopfront", { kind: "services" }),
    consolePath("shopfront", { kind: "service", name: "api" }),
    `${consolePath("shopfront", { kind: "services" })}?view=charts`,
  ])("opens Deployments and lights Services on %s", (href) => {
    renderAt(href);
    expect(screen.getByTestId("nav-deployments")).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByTestId("nav-services")).toHaveAttribute("aria-current", "page");
  });

  it("leads Observability with its Overview, then a Performance & issues divider over Errors … Sessions", () => {
    const ctx = ctxAt(inboxPath());
    const observability = hybridNav.items.find((item) => item.id === "observability")!;
    expect(flattenIds([observability], ctx)).toEqual([
      "observability",
      "obs-overview",
      "obs-graph",
      "obs-metrics",
      "obs-logs",
      "obs-rpc",
      "obs-profiles",
      "performance-heading",
      "obs-errors",
      "obs-alerts",
      "obs-traces",
      "site-insights",
      "obs-sessions",
    ]);
    // Drilling in lands on the Overview, not the Service graph.
    expect(hrefOf(observability, ctx)).toBe(`${obsOverviewPath("shopfront")}`);
  });

  it("gives Agents a Connectors row and Analytics its tabs", () => {
    const ctx = ctxAt(inboxPath());
    const ids = flattenIds(hybridNav.items, ctx);
    expect(ids).toEqual(expect.arrayContaining(["connectors", "sandboxes", "workflows-runs", "workflows-library", "slot-chat-list", "slot-new-chat"]));
    // AI providers lives in Settings only (U6e).
    expect(ids).not.toContain("ai-providers");
    const analytics = hybridNav.items.find((item) => item.id === "analytics")!;
    expect(flattenIds([analytics], ctx)).toEqual(["analytics", "analytics-overview", "analytics-users", "analytics-product", "analytics-acquisition", "analytics-events"]);
  });

  it("lists Experiments under Analytics behind CHANGES_UI, and always on its own pages", () => {
    const analytics = hybridNav.items.find((item) => item.id === "analytics")!;
    const experiments = consolePath("shopfront", { kind: "experiments" });
    expect(flattenIds([analytics], ctxAt(inboxPath()))).not.toContain("experiments");
    // Opened by its URL with the flag off, it is still where you are.
    const onPage = ctxAt(experiments);
    expect(flattenIds([analytics], onPage)).toContain("experiments");
    expect(activeArea(hybridNav, onPage)).toBe(null);
    changesUI.on = true;
    try {
      const ctx = ctxAt(inboxPath());
      expect(flattenIds([analytics], ctx)).toEqual([
        "analytics",
        "analytics-overview",
        "analytics-users",
        "analytics-product",
        "analytics-acquisition",
        "analytics-events",
        "experiments",
      ]);
      const row = childrenOf(analytics, ctx).find((item) => item.id === "experiments")!;
      expect(hrefOf(row, ctx)).toBe(experiments);
      // Forge Changes is Beta wherever it is surfaced (owner decision); Analytics itself carries nothing.
      expect(row.kind === "row" && row.badge?.(ctx)).toEqual({ type: "label", text: "Beta" });
      expect(analytics.kind === "group" && analytics.badge).toBeFalsy();
      expect(isCurrent(row, ctxAt(consolePath("shopfront", { kind: "experiment", changeId: "c-1" })))).toBe(true);
    } finally {
      changesUI.on = false;
    }
  });

  it("keeps the same top level across several projects", () => {
    expect(topIds(ctxAt(inboxPath(), { mode: "all", projectId: null }))).toEqual(topIds());
  });
});

describe("the Inbox, at the top level and in Agents ›", () => {
  const agents = () => hybridNav.items.find((item) => item.id === "agents")!;
  // A stand-in for the shell's Inbox row: it renders whatever the slot is handed, lit on /inbox.
  const inboxSlot = (ctx: NavCtx, onNavigate: (href: string) => void) => ({
    "new-chat": () => <span>New chat</span>,
    inbox: () => (
      <a href={inboxPath()} aria-current={ctx.pathname === inboxPath() ? ("page" as const) : undefined} data-testid="inbox-row" onClick={(e) => (e.preventDefault(), onNavigate(inboxPath()))}>
        Inbox <span data-testid="inbox-row-badge">3</span>
      </a>
    ),
  });
  const renderWithInbox = (href: string, onNavigate = vi.fn()) => {
    const ctx = ctxAt(href);
    render(<NavRenderer config={hybridNav} ctx={ctx} slots={inboxSlot(ctx, onNavigate)} onNavigate={onNavigate} />);
    return onNavigate;
  };

  it("puts Agents' Inbox right under New chat, on the same slot as the top level's", () => {
    const ctx = ctxAt(projectChatsPath("p1"));
    expect(childrenOf(agents(), ctx).map((item) => item.id)).toEqual(["slot-new-chat", "slot-agents-inbox", "workflows", "sandboxes", "connectors", "slot-chat-list"]);
    const agentsInbox = childrenOf(agents(), ctx).find((item) => item.id === "slot-agents-inbox")!;
    const topInbox = hybridNav.items.find((item) => item.id === "slot-inbox")!;
    expect(agentsInbox.kind === "slot" && agentsInbox.slot).toBe("inbox");
    expect(topInbox.kind === "slot" && topInbox.slot).toBe("inbox");
  });

  it("keeps /inbox on the top level (it is not an Agents route), its top-level row the one drawn and lit", () => {
    expect(activeArea(hybridNav, ctxAt(inboxPath()))).toBeNull();
    expect(isCurrent(agents(), ctxAt(inboxPath()))).toBe(false);
    renderWithInbox(inboxPath());
    expect(screen.getByTestId("nav-top")).toBeInTheDocument();
    expect(screen.getByTestId("nav-slot-inbox")).toBeInTheDocument();
    expect(screen.queryByTestId("nav-slot-agents-inbox")).toBeNull();
    expect(screen.getAllByTestId("inbox-row")).toHaveLength(1);
    expect(screen.getByTestId("inbox-row")).toHaveAttribute("aria-current", "page");
    expect(screen.getByTestId("inbox-row-badge")).toHaveTextContent("3");
  });

  it.each([projectChatsPath("p1"), newChatPath(), runsPath(), sandboxesPath()])("draws Agents' Inbox, with its badge, in the drill-in on %s", (href) => {
    const onNavigate = renderWithInbox(href);
    const drill = screen.getByTestId("nav-drill-in");
    expect(drill).toHaveAttribute("data-area", "agents");
    expect(screen.queryByTestId("nav-slot-inbox")).toBeNull();
    const row = screen.getByTestId("inbox-row");
    expect(screen.getByTestId("nav-slot-agents-inbox")).toContainElement(row);
    expect(row).toHaveAttribute("href", inboxPath());
    expect(row).not.toHaveAttribute("aria-current");
    expect(screen.getByTestId("inbox-row-badge")).toHaveTextContent("3");
    // Under New chat, above Workflows.
    const order = ["nav-slot-new-chat", "nav-slot-agents-inbox", "nav-workflows"].map((id) => screen.getByTestId(id));
    for (let i = 1; i < order.length; i++) expect(order[i - 1].compareDocumentPosition(order[i]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    fireEvent.click(row);
    expect(onNavigate).toHaveBeenLastCalledWith(inboxPath());
  });
});

describe("drill-in is derived from the route", () => {
  const logs = consolePath("shopfront", { kind: "obs-view", view: "logs" });
  it.each([
    [projectChatsPath("p1"), "agents"],
    [newChatPath(), "agents"],
    [runsPath(), "agents"],
    [logs, "observability"],
    [consolePath("shopfront", { kind: "site-insights" }), "observability"],
    [consolePath("shopfront", { kind: "site-insights", view: "seo" }), "observability"],
    // The old name still names the page.
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
    expect(onNavigate).toHaveBeenCalledWith(backHref(ctxAt(logs)), { drill: true });
    rerender(<NavRenderer config={hybridNav} ctx={ctxAt(consolePath("shopfront", { kind: "overview" }))} slots={{}} onNavigate={onNavigate} />);
    expect(screen.queryByTestId("nav-drill-in")).toBeNull();
    expect(screen.getByTestId("sidebar-home-button")).toHaveAttribute("aria-current", "page");
  });

  it("shows Performance & issues as a divider whose rows are always visible", () => {
    renderAt(consolePath("shopfront", { kind: "obs-view", view: "logs" }));
    expect(screen.getByText("Performance & issues")).toBeInTheDocument();
    expect(screen.queryByTestId("nav-performance")).toBeNull();
    for (const id of ["obs-errors", "obs-alerts", "obs-traces", "site-insights", "obs-sessions"]) expect(screen.getByTestId(`nav-${id}`)).toBeInTheDocument();
  });

  it("is Observability's on its Overview page, with the Overview row lit", () => {
    renderAt(obsOverviewPath("shopfront"));
    expect(screen.getByTestId("nav-drill-in")).toHaveAttribute("data-area", "observability");
    expect(screen.getByTestId("nav-obs-overview")).toHaveAttribute("aria-current", "page");
  });

  it("keeps a drawer open on a drill-in and its back header, not on a destination", () => {
    const onNavigate = vi.fn();
    renderAt(inboxPath(), onNavigate);
    fireEvent.click(screen.getByTestId("nav-observability"));
    expect(onNavigate).toHaveBeenLastCalledWith(obsOverviewPath("shopfront"), { drill: true });
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

  // Every Settings section's page shows Settings' sections in the nav, except
  // the ones another area owns, which Settings must then neither light up nor
  // reopen (SETTINGS_SECTIONS_WITH_OTHER_NAV_HOME). A section moved under
  // another area without being listed there fails here: that is how bare
  // /settings came to open Agents › Sandboxes with no way back to Settings.
  it.each(getVisibleSettingsSectionIds())("%s's page is Settings', or listed as another area's", (section) => {
    const area = activeArea(hybridNav, ctxAt(settingsPath(section)))?.id ?? null;
    if (SETTINGS_SECTIONS_WITH_OTHER_NAV_HOME.includes(section)) expect(area).not.toBe("settings");
    else expect(area).toBe("settings");
  });

  it.each([sandboxesPath(), machineSettingsPath("d-1")])("leaves %s to Agents › Sandboxes, with Settings unlit", (href) => {
    renderAt(href);
    expect(screen.getByTestId("nav-drill-in")).toHaveAttribute("data-area", "agents");
    expect(screen.getByTestId("nav-sandboxes")).toHaveAttribute("aria-current", "page");
    expect(screen.getByTestId("sidebar-settings-button")).not.toHaveAttribute("aria-current");
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

describe("the nav's tour anchors", () => {
  it("anchors Overview and the app's section groups by data-tour (one set, for the demo's journey and the sample tour)", () => {
    const unregister = registerConsoleNavEntry({ id: "advisories", label: "Advisories", segment: "advisories", after: "more-tools", group: "security" });
    try {
      const href = consolePath("shopfront", { kind: "secrets" });
      const console = { project: "shopfront", page: { kind: "secrets" as const }, env: "prod", envs: [] };
      const { container } = render(<NavRenderer config={hybridNav} ctx={ctxAt(href, { console })} slots={{}} onNavigate={vi.fn()} />);
      const anchors = [...container.querySelectorAll("[data-tour]")].map((el) => el.getAttribute("data-tour"));
      for (const id of ["forge-console-nav", "nav-overview", "nav-observability", "nav-analytics", "nav-deployments", "nav-tests", "nav-data", "nav-security", "nav-configure"]) {
        expect(anchors, id).toContain(id);
      }
      // A group's anchor holds the group: its row, and its rows while open (Configure, on Secrets).
      const configure = container.querySelector("[data-tour='nav-configure']")!;
      expect(configure.querySelector("[data-testid='nav-configure']")).not.toBeNull();
      expect(configure.querySelector("[data-testid='nav-secrets']")).not.toBeNull();
      expect(container.querySelector("[data-tour='nav-overview'] [data-testid='sidebar-home-button']")).not.toBeNull();
      expect(container.querySelector("[data-tour='nav-observability'] [data-testid='nav-observability']")).not.toBeNull();
    } finally {
      unregister();
    }
  });
});

describe("the Agent sandboxes rows' housekeeping dot", () => {
  const slots = { "sandboxes-housekeeping": () => <span data-testid="housekeeping-slot" /> };

  it("Agents › Sandboxes carries it", () => {
    render(<NavRenderer config={hybridNav} ctx={ctxAt(sandboxesPath())} slots={slots} onNavigate={vi.fn()} />);
    expect(screen.getByTestId("nav-sandboxes")).toContainElement(screen.getByTestId("housekeeping-slot"));
  });

  it("Settings › Agent sandboxes carries it, and no other Settings row does", () => {
    render(<NavRenderer config={hybridNav} ctx={ctxAt(settingsPath("notifications"))} slots={slots} onNavigate={vi.fn()} />);
    expect(screen.getAllByTestId("housekeeping-slot")).toHaveLength(1);
    expect(screen.getByTestId("nav-settings-machines")).toContainElement(screen.getByTestId("housekeeping-slot"));
  });
});
