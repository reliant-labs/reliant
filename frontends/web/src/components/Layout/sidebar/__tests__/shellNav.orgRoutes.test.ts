// Copyright (c) 2026 Reliant Labs

/**
 * The shell's navigation targets with `web.org_routes` on (pr-review [B1] on
 * #2991): the project switcher's picks and every row of the hybrid nav land
 * in the org of the route on screen, in its org form, never in the org-less
 * form or in the tab's other org.
 *
 * No call site passes an org: the builders (lib/paths, Forge/Console/paths)
 * read it themselves, the route's org first, then the tab's (lib/paths/org.ts,
 * withOrg). These tests hold that for the shell's targets, with the tab's org
 * (acme) deliberately different from the route's (side), and pin today's
 * org-less paths with the flag off.
 */

import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { commitRouteOrgScope, useRouteOrgScope } from "@/lib/routeOrg";
import { resetShellForTests } from "@/lib/shell";
import type { AppChipModel, ProjectCatalogRow } from "@/services/projects/catalog";
import type { Project } from "@/store/projectStore";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import { setRuntimeConfig } from "@/test/runtimeConfig";

import { consolePageOf, orgPageOf } from "../../../Forge/Console/paths";
import { pickTarget } from "../ProjectSwitcher";
import { childrenOf, hrefOf, hybridNav, visibleItems, type NavCtx, type NavItem } from "../navConfig";

const chip = (name: string): AppChipModel => ({ name, link: "source", state: "live", isStaticSite: false, environments: [] });
const projectRow = (id: string, name: string, apps: AppChipModel[]): ProjectCatalogRow => ({
  kind: "project",
  key: id,
  name,
  activityAt: 0,
  project: { id, name, path: `/code/${name}`, is_git_repo: true, last_active: "", created_at: "", updated_at: "", is_forge: apps.length > 0 } as Project,
  shared: false,
  repos: [],
  apps,
  checkouts: [],
  hasCheckout: true,
});
const shop = projectRow("p-shop", "shopfront", [chip("shopfront")]);
const docs = projectRow("p-docs", "docs-site", []);

function ctxAt(pathname: string, over: Partial<NavCtx> = {}): NavCtx {
  const page = consolePageOf(pathname)?.page ?? null;
  return {
    pathname,
    search: {},
    mode: "one",
    projectId: "p-shop",
    projectName: "shopfront",
    app: "shopfront",
    console: { project: "shopfront", page: page ?? { kind: "overview" }, env: "prod", envs: [] },
    page,
    orgPage: orgPageOf(pathname),
    ...over,
  };
}

/** Every href the nav draws for `ctx`: its rows, the rows inside its groups and areas, and the footer. */
function hrefs(ctx: NavCtx): string[] {
  const walk = (items: readonly NavItem[]): string[] =>
    visibleItems(items, ctx).flatMap((item) => [hrefOf(item, ctx), ...walk(childrenOf(item, ctx))].filter((href): href is string => !!href));
  return [...walk(hybridNav.items), ...walk(hybridNav.footer)];
}

/**
 * Settings and its sections (Agent sandboxes is Settings › Machines) are not
 * org-scoped yet: they join withOrg in U8, with their `/o/<org>` routes
 * (lib/paths/org.ts). Every other target is.
 */
const orgLess = (href: string) => /^\/settings(\/|$|\?)/.test(href);

beforeEach(() => {
  resetShellForTests("reliant");
  // The tab's org is acme; the route on screen is in side.
  useActiveOrgStore.setState({ effectiveId: "org-1", segments: { "org-1": "acme", "org-2": "side" } });
  commitRouteOrgScope({ kind: "member", org: { id: "org-2", slug: "side" } });
});

afterEach(() => {
  resetShellForTests();
  useRouteOrgScope.setState({ scope: { kind: "legacy" } });
  useActiveOrgStore.setState({ effectiveId: null, segments: {} });
});

describe("the shell's navigation targets, flag on", () => {
  beforeEach(() => setRuntimeConfig({ ORG_ROUTES: "true" }));

  it("the switcher's picks land in the route's org, in its org form", () => {
    // From an app page: the same section of the picked app.
    expect(pickTarget({ kind: "project", row: docs }, "/o/side/apps/shopfront/observability/errors").href).toBe("/o/side/apps/docs-site/observability/errors");
    expect(pickTarget({ kind: "project", row: shop }, "/o/side/apps/docs-site/releases/v1").href).toBe("/o/side/apps/shopfront/releases");
    // From a chat: the project's chats.
    expect(pickTarget({ kind: "project", row: docs }, "/o/side/c/chat-1")).toEqual({ key: "p-docs", href: "/o/side/project/p-docs" });
    // An app with no project here: its Overview.
    expect(pickTarget({ kind: "app", app: chip("status-page") }, "/o/side/inbox")).toEqual({ key: "app:status-page", href: "/o/side/apps/status-page" });
  });

  it("every nav row on an app page is in the route's org", () => {
    for (const pathname of ["/o/side/apps/shopfront", "/o/side/apps/shopfront/observability/errors", "/o/side/apps/shopfront/releases"]) {
      const all = hrefs(ctxAt(pathname));
      expect(all.filter((href) => !orgLess(href)).length).toBeGreaterThan(5);
      for (const href of all) {
        if (orgLess(href)) continue;
        expect(href, `${href} on ${pathname}`).toMatch(/^\/o\/side(\/|$|\?)/);
      }
    }
    const ctx = ctxAt("/o/side/apps/shopfront/releases");
    const overview = visibleItems(hybridNav.items, ctx).find((item) => item.id === "overview")!;
    expect(hrefOf(overview, ctx)).toBe("/o/side/apps/shopfront");
  });

  it("with several projects or none, the nav's rows are in the route's org too", () => {
    for (const over of [{ mode: "all" as const, projectId: null, app: null }, { mode: "many" as const, projectId: null, app: null }]) {
      for (const href of hrefs(ctxAt("/o/side/inbox", over))) {
        if (orgLess(href)) continue;
        expect(href).toMatch(/^\/o\/side(\/|$|\?)/);
      }
    }
  });
});

describe("the shell's navigation targets, flag off", () => {
  beforeEach(() => setRuntimeConfig({}));

  it("are today's org-less paths, byte for byte", () => {
    expect(pickTarget({ kind: "project", row: docs }, "/forge/p/shopfront/observability/errors").href).toBe("/forge/p/docs-site/observability/errors");
    expect(pickTarget({ kind: "project", row: docs }, "/c/chat-1").href).toBe("/project/p-docs");
    for (const href of hrefs(ctxAt("/forge/p/shopfront/releases"))) expect(href).not.toMatch(/^\/o\//);
  });
});
