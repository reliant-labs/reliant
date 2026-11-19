// Copyright (c) 2025 Reliant Labs

import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { HostedAppsState, ProjectAppLink, ProjectSchema } from "@/gen/reliant/v1/project_pb";

import { catalogFromResponse, listCatalog, type WireHostedApp, type WireProjectWithApps } from "../catalog";
import { appCount, appSourceRemote, cloneUrlOf, projectsPageSections } from "../projectsPage";

function hosted(name: string, over: Partial<WireHostedApp> = {}): WireHostedApp {
  return { name, createdAt: "2026-01-01T00:00:00Z", environments: [], hasStaticSite: false, sources: [], warning: "", ...over };
}

function project(id: string, lastActive: string, over: { viewerAccess?: boolean; apps?: WireProjectWithApps["apps"] } = {}): WireProjectWithApps {
  return {
    project: create(ProjectSchema, {
      id,
      name: id,
      path: `/home/me/${id}`,
      lastActive,
      createdAt: "2025-01-01T00:00:00Z",
      updatedAt: "2025-01-01T00:00:00Z",
      viewerAccess: over.viewerAccess ?? false,
    }),
    repos: [],
    apps: over.apps ?? [],
    checkouts: [],
  };
}

const CATALOG = catalogFromResponse({
  orgId: "org-1",
  hostedAppsState: HostedAppsState.OK,
  projects: [
    project("mine-old", "2026-01-01T00:00:00Z", {
      apps: [{ name: "shop", repoId: "r", subpath: "", link: ProjectAppLink.SOURCE, hosted: hosted("shop", { lastDeployedAt: "2026-01-02T00:00:00Z" }) }],
    }),
    project("mine-new", "2026-03-01T00:00:00Z"),
    project("theirs", "2026-02-01T00:00:00Z", { viewerAccess: true }),
  ],
  unlinkedApps: [
    hosted("ci-only", { sources: [{ remote: "", subpath: "", commit: "a", lastSeenAt: "" }, { remote: "github.com/acme/ci", subpath: "app", commit: "b", lastSeenAt: "" }] }),
    hosted("shop"),
  ],
});

describe("projectsPageSections", () => {
  it("splits the listed rows into your projects, shared, and apps without code, in that order", () => {
    const sections = projectsPageSections(listCatalog(CATALOG, { pinned: new Set(), hidden: new Set() }));
    expect(sections.map((s) => [s.id, s.label, s.rows.map((r) => r.row.key)])).toEqual([
      ["yours", "Your projects", ["mine-new", "mine-old"]],
      ["shared", "Shared with the org", ["theirs"]],
      ["apps", "Apps without code here", ["app:ci-only", "app:shop"]],
    ]);
  });

  it("keeps a pinned row first within its own section, never across one", () => {
    const sections = projectsPageSections(listCatalog(CATALOG, { pinned: new Set(["mine-old", "app:shop"]), hidden: new Set() }));
    expect(sections[0].rows.map((r) => r.row.key)).toEqual(["mine-old", "mine-new"]);
    expect(sections[2].rows.map((r) => r.row.key)).toEqual(["app:shop", "app:ci-only"]);
  });

  it("leaves empty sections out", () => {
    const listed = listCatalog(CATALOG, { pinned: new Set(), hidden: new Set() }, { filter: "code_only" });
    expect(projectsPageSections(listed).map((s) => s.id)).toEqual(["yours", "shared"]);
    expect(projectsPageSections([])).toEqual([]);
  });
});

describe("Get the code", () => {
  it("clones from the newest source that has a remote", () => {
    const ci = CATALOG.rows.find((r) => r.key === "app:ci-only");
    expect(ci?.kind).toBe("app");
    if (ci?.kind !== "app") return;
    expect(appSourceRemote(ci.app)).toBe("github.com/acme/ci");
    expect(cloneUrlOf("github.com/acme/ci")).toBe("https://github.com/acme/ci");
  });

  it("has no remote for an app with no recorded source", () => {
    const shop = CATALOG.rows.find((r) => r.key === "app:shop");
    if (shop?.kind !== "app") throw new Error("fixture");
    expect(appSourceRemote(shop.app)).toBeNull();
  });
});

describe("appCount", () => {
  it("counts an app once however many rows hold it", () => {
    expect(appCount(listCatalog(CATALOG, { pinned: new Set(), hidden: new Set() }))).toBe(2);
  });
});
