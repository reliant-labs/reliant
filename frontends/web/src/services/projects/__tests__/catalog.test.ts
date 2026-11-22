// Copyright (c) 2025 Reliant Labs

import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { HostedAppsState, ProjectAppLink, ProjectInstallState, ProjectSchema } from "@/gen/reliant/v1/project_pb";

import {
  appEntries,
  appRowKey,
  catalogFromProjects,
  catalogFromResponse,
  catalogGroups,
  chipStateOf,
  compareByActivity,
  filterCounts,
  listCatalog,
  matchesQuery,
  type CatalogFilter,
  type CatalogPrefs,
  type ListedRow,
  type WireHostedApp,
  type WireListProjectsWithAppsResponse,
  type WireProjectWithApps,
} from "../catalog";

// ── Fixtures ────────────────────────────────────────────────────────────────

function hosted(name: string, over: Partial<WireHostedApp> = {}): WireHostedApp {
  return { name, createdAt: "2026-01-01T00:00:00Z", environments: [], hasStaticSite: false, sources: [], warning: "", ...over };
}

interface ProjectFixture {
  id: string;
  name?: string;
  lastActive?: string;
  viewerAccess?: boolean;
  remoteUrl?: string;
  path?: string;
  repos?: WireProjectWithApps["repos"];
  apps?: WireProjectWithApps["apps"];
  checkouts?: WireProjectWithApps["checkouts"];
}

function project(f: ProjectFixture): WireProjectWithApps {
  return {
    project: create(ProjectSchema, {
      id: f.id,
      name: f.name ?? f.id,
      path: f.path ?? `/home/me/${f.id}`,
      lastActive: f.lastActive ?? "2026-01-01T00:00:00Z",
      createdAt: "2025-01-01T00:00:00Z",
      updatedAt: "2025-01-01T00:00:00Z",
      remoteUrl: f.remoteUrl,
      viewerAccess: f.viewerAccess ?? false,
    }),
    repos: f.repos ?? [],
    apps: f.apps ?? [],
    checkouts: f.checkouts ?? [],
  };
}

function response(projects: WireProjectWithApps[], unlinkedApps: WireHostedApp[] = [], hostedAppsState: HostedAppsState = HostedAppsState.OK): WireListProjectsWithAppsResponse {
  return { orgId: "org-1", projects, unlinkedApps, hostedAppsState };
}

const NO_PREFS: CatalogPrefs = { pinned: new Set(), hidden: new Set() };
const prefs = (pinned: string[] = [], hidden: string[] = []): CatalogPrefs => ({ pinned: new Set(pinned), hidden: new Set(hidden) });
const keys = (listed: ListedRow[]) => listed.map((l) => l.row.key);

/** The shared fixture: a code-only project, a site, an app project, a viewer's project, an app-only app. */
const CATALOG = catalogFromResponse(
  response(
    [
      project({
        id: "p-shop",
        name: "Shop",
        lastActive: "2026-03-01T00:00:00Z",
        remoteUrl: "https://github.com/acme/shop.git",
        repos: [{ id: "r-1", name: "shop", relativePath: "", remote: "github.com/acme/shop" }],
        apps: [
          { name: "shop", repoId: "r-1", subpath: "", link: ProjectAppLink.SOURCE, hosted: hosted("shop", { lastDeployedAt: "2026-03-05T00:00:00Z", environments: ["prod", "staging"] }) },
          { name: "shop-admin", repoId: "r-1", subpath: "admin", link: ProjectAppLink.NOT_HOSTED },
        ],
        checkouts: [{ daemonId: "d-1", daemonName: "laptop", installState: ProjectInstallState.INSTALLED }],
      }),
      project({
        id: "p-site",
        name: "Landing",
        lastActive: "2026-02-01T00:00:00Z",
        apps: [{ name: "landing", repoId: "r-2", subpath: "", link: ProjectAppLink.NAME, hosted: hosted("landing", { hasStaticSite: true }) }],
        checkouts: [{ daemonId: "d-1", daemonName: "laptop", installState: ProjectInstallState.INSTALLING }],
      }),
      project({ id: "p-code", name: "dotfiles", lastActive: "2026-04-01T00:00:00Z", repos: [{ id: "r-3", name: "dotfiles", relativePath: "", remote: "github.com/me/dotfiles" }] }),
      project({ id: "p-shared", name: "Team notes", lastActive: "2025-12-01T00:00:00Z", viewerAccess: true }),
    ],
    [hosted("billing-api", { lastDeployedAt: "2026-03-10T00:00:00Z", sources: [{ remote: "github.com/acme/billing", subpath: "", commit: "abc", lastSeenAt: "" }] })],
  ),
);

// ── catalogFromResponse ─────────────────────────────────────────────────────

describe("catalogFromResponse", () => {
  it("makes a project row per project and an app-only row per unlinked app, in the server's order", () => {
    expect(CATALOG.rows.map((r) => [r.kind, r.key])).toEqual([
      ["project", "p-shop"],
      ["project", "p-site"],
      ["project", "p-code"],
      ["project", "p-shared"],
      ["app", "app:billing-api"],
    ]);
    expect(CATALOG.hostedAppsState).toBe("ok");
    expect(CATALOG.orgId).toBe("org-1");
  });

  it("gives each project's forge.yaml a chip with its link, state and site flag", () => {
    const shop = CATALOG.rows[0];
    if (shop.kind !== "project") throw new Error("expected a project");
    expect(shop.apps.map((a) => [a.name, a.link, a.state, a.isStaticSite, a.subpath])).toEqual([
      ["shop", "source", "live", false, ""],
      ["shop-admin", "not_hosted", "not_deployed", false, "admin"],
    ]);
    expect(shop.apps[0].environments).toEqual(["prod", "staging"]);
    const site = CATALOG.rows[1];
    if (site.kind !== "project") throw new Error("expected a project");
    expect(site.apps.map((a) => [a.link, a.state, a.isStaticSite])).toEqual([["name", "not_deployed", true]]);
  });

  it("says a machine has the code only for an INSTALLED checkout", () => {
    const byKey = new Map(CATALOG.rows.map((r) => [r.key, r]));
    const has = (key: string) => {
      const r = byKey.get(key);
      return r?.kind === "project" ? r.hasCheckout : undefined;
    };
    expect(has("p-shop")).toBe(true);
    expect(has("p-site")).toBe(false); // still cloning
    expect(has("p-code")).toBe(false);
  });

  it("marks a project shared with the person", () => {
    const shared = CATALOG.rows.find((r) => r.key === "p-shared");
    expect(shared?.kind === "project" && shared.shared).toBe(true);
  });

  it("takes a project's activity from its own use or its newest app deploy, an app-only row's from its deploy", () => {
    const at = (key: string) => CATALOG.rows.find((r) => r.key === key)?.activityAt;
    expect(at("p-shop")).toBe(Date.parse("2026-03-05T00:00:00Z")); // the deploy is newer than last_active
    expect(at("p-code")).toBe(Date.parse("2026-04-01T00:00:00Z"));
    expect(at("app:billing-api")).toBe(Date.parse("2026-03-10T00:00:00Z"));
  });

  it("dates a never-deployed app-only row by its creation", () => {
    const c = catalogFromResponse(response([], [hosted("fresh", { createdAt: "2026-05-01T00:00:00Z" })]));
    expect(c.rows[0].activityAt).toBe(Date.parse("2026-05-01T00:00:00Z"));
  });

  it("skips a row with no project rather than inventing one", () => {
    const c = catalogFromResponse(response([{ repos: [], apps: [], checkouts: [] }]));
    expect(c.rows).toEqual([]);
  });

  it.each([
    ["no access", HostedAppsState.NO_ACCESS, "no_access"],
    ["unavailable", HostedAppsState.UNAVAILABLE, "unavailable"],
    ["an older server's unset state", HostedAppsState.UNSPECIFIED, "unavailable"],
  ] as const)("with the org's apps unreadable (%s), lists the projects with plain chips and no app-only rows", (_, wire, state) => {
    const c = catalogFromResponse(
      response(
        [project({ id: "p", apps: [{ name: "shop", repoId: "r", subpath: "", link: ProjectAppLink.SOURCE, hosted: hosted("shop", { lastDeployedAt: "2026-01-02T00:00:00Z" }) }] })],
        [hosted("stray")],
        wire,
      ),
    );
    expect(c.hostedAppsState).toBe(state);
    expect(c.rows.map((r) => r.key)).toEqual(["p"]);
    const row = c.rows[0];
    if (row.kind !== "project") throw new Error("expected a project");
    expect(row.apps[0].state).toBe(state);
    expect(row.apps[0].hosted).toBeUndefined();
  });
});

describe("chipStateOf", () => {
  it.each([
    ["ok", { lastDeployedAt: "2026-01-01T00:00:00Z" }, "live"],
    ["ok", {}, "not_deployed"],
    ["ok", undefined, "not_deployed"],
    ["no_access", { lastDeployedAt: "2026-01-01T00:00:00Z" }, "no_access"],
    ["unavailable", undefined, "unavailable"],
  ] as const)("%s with %j is %s", (state, over, want) => {
    expect(chipStateOf(state, over ? { ...hostedAppOfFixture(), ...over } : undefined)).toBe(want);
  });
});

function hostedAppOfFixture() {
  return { name: "x", createdAt: "", environments: [], hasStaticSite: false, sources: [], warning: "" };
}

// ── Filters, search, sort ───────────────────────────────────────────────────

describe("listCatalog filters", () => {
  it.each<[CatalogFilter, string[]]>([
    ["all", ["p-code", "app:billing-api", "p-shop", "p-site", "p-shared"]],
    ["with_apps", ["app:billing-api", "p-shop", "p-site"]],
    ["sites", ["p-site"]],
    ["code_only", ["p-code", "p-shared"]],
  ])("%s lists %j, most recently active first", (filter, want) => {
    expect(keys(listCatalog(CATALOG, NO_PREFS, { filter }))).toEqual(want);
  });

  it("With apps and Code only partition the project rows", () => {
    const projects = (f: CatalogFilter) => listCatalog(CATALOG, NO_PREFS, { filter: f, kinds: "projects" }).length;
    expect(projects("with_apps") + projects("code_only")).toBe(projects("all"));
  });

  it("counts every filter over the same visible rows", () => {
    expect(filterCounts(CATALOG, prefs([], ["p-code"]))).toEqual({ all: 4, with_apps: 3, sites: 1, code_only: 1 });
    expect(filterCounts(CATALOG, prefs([], ["p-code"]), { showHidden: true })).toEqual({ all: 5, with_apps: 3, sites: 1, code_only: 2 });
  });
});

describe("listCatalog kinds", () => {
  it.each([
    ["any", ["p-code", "app:billing-api", "p-shop", "p-site", "p-shared"]],
    ["projects", ["p-code", "p-shop", "p-site", "p-shared"]],
    ["apps", ["app:billing-api", "p-shop", "p-site"]],
  ] as const)("%s lists %j", (kinds, want) => {
    expect(keys(listCatalog(CATALOG, NO_PREFS, { kinds }))).toEqual(want);
  });
});

describe("matchesQuery", () => {
  const row = (key: string) => {
    const r = CATALOG.rows.find((x) => x.key === key);
    if (!r) throw new Error(key);
    return r;
  };
  it.each([
    ["", "p-code", true],
    ["   ", "p-code", true],
    ["SHOP", "p-shop", true], // name, case-insensitive
    ["admin", "p-shop", true], // an app's name
    ["acme/shop", "p-shop", true], // a repo's remote
    ["github.com/acme/shop.git", "p-shop", true], // the project's remote URL
    ["/home/me/p-code", "p-code", true], // the folder
    ["dotfiles me", "p-code", true], // every term, anywhere
    ["dotfiles shop", "p-code", false], // one term missing
    ["acme/billing", "app:billing-api", true], // an app-only row's source remote
    ["landing", "p-shop", false],
  ])("%j against %s: %s", (query, key, want) => {
    expect(matchesQuery(row(key), query)).toBe(want);
  });

  it("filters the list", () => {
    expect(keys(listCatalog(CATALOG, NO_PREFS, { query: "shop" }))).toEqual(["p-shop"]);
  });
});

describe("compareByActivity", () => {
  const at = (key: string, name: string, activityAt: number) => ({ kind: "app" as const, key, name, activityAt, app: {} as never });
  it("orders newest first, then by name ignoring case, then by key", () => {
    const rows = [at("k3", "b", 1), at("k1", "A", 1), at("k0", "a", 1), at("k2", "z", 9)];
    expect(rows.sort(compareByActivity).map((r) => r.key)).toEqual(["k2", "k0", "k1", "k3"]);
  });
});

// ── Pins and hides ──────────────────────────────────────────────────────────

describe("listCatalog pins and hides", () => {
  it("puts pinned rows first, each group by activity, and pins an app-only row by its app key", () => {
    const listed = listCatalog(CATALOG, prefs(["p-shared", appRowKey("billing-api")]));
    expect(keys(listed)).toEqual(["app:billing-api", "p-shared", "p-code", "p-shop", "p-site"]);
    expect(listed.filter((l) => l.pinned).map((l) => l.row.key)).toEqual(["app:billing-api", "p-shared"]);
  });

  it("leaves hidden rows out unless show-hidden, and a hidden row is never shown pinned", () => {
    const p = prefs(["p-shop"], ["p-shop", "app:billing-api"]);
    expect(keys(listCatalog(CATALOG, p))).toEqual(["p-code", "p-site", "p-shared"]);
    const all = listCatalog(CATALOG, p, { showHidden: true });
    expect(keys(all)).toEqual(["p-code", "app:billing-api", "p-shop", "p-site", "p-shared"]);
    expect(all.find((l) => l.row.key === "p-shop")).toMatchObject({ hidden: true, pinned: false });
  });
});

// ── Groups ──────────────────────────────────────────────────────────────────

describe("catalogGroups", () => {
  const many = catalogFromResponse(
    response(Array.from({ length: 9 }, (_, i) => project({ id: `p-${i}`, lastActive: `2026-01-0${i + 1}T00:00:00Z` }))),
  );

  it("groups Pinned, Recent (the most active unpinned) and All (the rest), each row once", () => {
    const groups = catalogGroups(listCatalog(many, prefs(["p-0"])), { recentCount: 3 });
    expect(groups.map((g) => [g.id, keys(g.rows)])).toEqual([
      ["pinned", ["p-0"]],
      ["recent", ["p-8", "p-7", "p-6"]],
      ["all", ["p-5", "p-4", "p-3", "p-2", "p-1"]],
    ]);
  });

  it("defaults Recent to five rows and leaves empty groups out", () => {
    const groups = catalogGroups(listCatalog(many, NO_PREFS));
    expect(groups.map((g) => [g.id, g.rows.length])).toEqual([
      ["recent", 5],
      ["all", 4],
    ]);
  });

  it("is one Results group while searching, pinned still first", () => {
    const listed = listCatalog(many, prefs(["p-1"]), { query: "p-" });
    const groups = catalogGroups(listed, { searching: true });
    expect(groups.map((g) => g.id)).toEqual(["results"]);
    expect(keys(groups[0].rows)[0]).toBe("p-1");
  });

  it("has no groups for nothing", () => {
    expect(catalogGroups([])).toEqual([]);
    expect(catalogGroups([], { searching: true })).toEqual([]);
  });
});

describe("appEntries", () => {
  it("lists every app once, with the project that holds it, in row order", () => {
    const twice = catalogFromResponse(
      response([
        project({ id: "a", lastActive: "2026-02-01T00:00:00Z", apps: [{ name: "shop", repoId: "r", subpath: "", link: ProjectAppLink.SOURCE }] }),
        project({ id: "b", lastActive: "2026-01-01T00:00:00Z", apps: [{ name: "shop", repoId: "r2", subpath: "", link: ProjectAppLink.NAME }] }),
      ], [hosted("solo")]),
    );
    const entries = appEntries(listCatalog(twice, NO_PREFS));
    expect(entries.map((e) => [e.app.name, e.project?.key ?? null])).toEqual([
      ["shop", "a"],
      ["solo", null],
    ]);
  });
});

describe("catalogFromProjects", () => {
  it("lists plain projects as partial rows with no apps, by their last use", () => {
    const c = catalogFromProjects("org-1", [
      { id: "a", name: "A", path: "/a", is_git_repo: true, is_forge: false, last_active: "2026-01-01T00:00:00Z", created_at: "", updated_at: "" },
    ]);
    expect(c.hostedAppsState).toBe("unavailable");
    expect(c.rows).toMatchObject([{ kind: "project", key: "a", partial: true, apps: [], activityAt: Date.parse("2026-01-01T00:00:00Z") }]);
    expect(keys(listCatalog(c, NO_PREFS, { filter: "code_only" }))).toEqual(["a"]);
  });
});
