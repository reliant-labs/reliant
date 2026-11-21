// Copyright (c) 2025 Reliant Labs

import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { HostedAppsState, ProjectAppLink, ProjectSchema } from "@/gen/reliant/v1/project_pb";
import { catalogFromResponse, type WireHostedApp, type WireProjectApp, type WireProjectWithApps } from "@/services/projects/catalog";

import { appCode, cloneUrlOf, projectLinkLabel, shortCommit, sourceLabel } from "../appCodeModel";

const SHOP: WireHostedApp = {
  name: "shop",
  createdAt: "2026-01-01T00:00:00Z",
  lastDeployedAt: "2026-03-01T00:00:00Z",
  environments: ["prod"],
  hasStaticSite: false,
  sources: [
    { remote: "github.com/acme/shop", subpath: "services/api", commit: "a1b2c3d4e5f6a7b8", lastSeenAt: "2026-03-01T00:00:00Z" },
    { remote: "github.com/alex/shop", subpath: "", commit: "0badc0ffee00", lastSeenAt: "2026-02-01T00:00:00Z" },
  ],
  warning: "shop is deployed from two repositories.",
};

const app = (name: string, link: ProjectAppLink, over: Partial<WireProjectApp> = {}, hosted: WireHostedApp | undefined = SHOP): WireProjectApp => ({
  name,
  repoId: "r",
  subpath: "",
  link,
  hosted: link === ProjectAppLink.NOT_HOSTED ? undefined : hosted,
  ...over,
});

function project(id: string, apps: WireProjectApp[], over: { lastActive?: string; viewerAccess?: boolean } = {}): WireProjectWithApps {
  return {
    project: create(ProjectSchema, {
      id,
      name: id,
      path: `/home/me/${id}`,
      lastActive: over.lastActive ?? "2026-01-01T00:00:00Z",
      createdAt: "2025-01-01T00:00:00Z",
      updatedAt: "2025-01-01T00:00:00Z",
      viewerAccess: over.viewerAccess ?? false,
    }),
    repos: [],
    apps,
    checkouts: [],
  };
}

const catalog = (projects: WireProjectWithApps[], unlinked: WireHostedApp[] = [], state = HostedAppsState.OK) =>
  catalogFromResponse({ orgId: "org-1", projects, unlinkedApps: unlinked, hostedAppsState: state });

describe("appCode", () => {
  it("lists the person's projects that hold the app, SOURCE before NAME, and shared ones apart", () => {
    const code = appCode(
      catalog([
        // Most recent first in the catalog: the NAME project is the more active one.
        project("fork", [app("shop", ProjectAppLink.NAME)], { lastActive: "2026-03-05T00:00:00Z" }),
        project("main", [app("shop", ProjectAppLink.SOURCE, { subpath: "services/api" })], { lastActive: "2026-03-04T00:00:00Z" }),
        project("other", [app("blog", ProjectAppLink.SOURCE)]),
        project("teammate", [app("shop", ProjectAppLink.NAME)], { viewerAccess: true }),
      ]),
      "shop",
    );
    expect(code.yours.map((p) => [p.row.key, p.link, p.subpath])).toEqual([
      ["main", "source", "services/api"],
      ["fork", "name", ""],
    ]);
    expect(code.shared.map((p) => p.row.key)).toEqual(["teammate"]);
    expect(code.state).toBe("ok");
    expect(code.sources).toHaveLength(2);
    expect(code.warning).toBe("shop is deployed from two repositories.");
  });

  it("takes the strongest of two forge.yamls of the same name in one project", () => {
    const code = appCode(catalog([project("p", [app("shop", ProjectAppLink.NAME, { subpath: "a" }), app("shop", ProjectAppLink.SOURCE, { subpath: "b" })])]), "shop");
    expect(code.yours.map((p) => [p.link, p.subpath])).toEqual([["source", "b"]]);
  });

  it("reads an app no project holds from its app-only row, with no projects", () => {
    const code = appCode(catalog([project("p", [])], [SHOP]), "shop");
    expect(code.hosted?.name).toBe("shop");
    expect(code.yours).toEqual([]);
    expect(code.shared).toEqual([]);
  });

  it("ignores a forge.yaml of the name the org was read and does not host", () => {
    const code = appCode(catalog([project("p", [app("shop", ProjectAppLink.NOT_HOSTED)])]), "shop");
    expect(code.yours).toEqual([]);
    expect(code.hosted).toBeUndefined();
  });

  it("still lists the projects naming the app when the org's apps could not be read, with the link unknown", () => {
    for (const state of [HostedAppsState.NO_ACCESS, HostedAppsState.UNAVAILABLE]) {
      const code = appCode(catalog([project("p", [app("shop", ProjectAppLink.NOT_HOSTED)])], [], state), "shop");
      expect(code.state).toBe(state === HostedAppsState.NO_ACCESS ? "no_access" : "unavailable");
      expect(code.yours.map((p) => p.link)).toEqual([null]);
      expect(code.sources).toEqual([]);
      expect(code.warning).toBe("");
    }
  });
});

describe("words", () => {
  it("labels a source, a local checkout and a commit", () => {
    expect(sourceLabel({ remote: "github.com/acme/shop", subpath: "services/api" })).toBe("github.com/acme/shop@services/api");
    expect(sourceLabel({ remote: "github.com/acme/shop", subpath: "" })).toBe("github.com/acme/shop");
    expect(sourceLabel({ remote: "", subpath: "" })).toBe("a local checkout");
    expect(shortCommit("a1b2c3d4e5f6")).toBe("a1b2c3d");
  });

  it("names the link and the clone URL", () => {
    expect(projectLinkLabel("source")).toBe("deployed from here");
    expect(projectLinkLabel("name")).toBe("same name");
    expect(cloneUrlOf([{ remote: "", subpath: "", commit: "", lastSeenAt: "" }, ...SHOP.sources])).toBe("https://github.com/acme/shop");
    expect(cloneUrlOf([])).toBeUndefined();
  });
});
