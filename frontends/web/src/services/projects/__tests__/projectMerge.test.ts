// Copyright (c) 2025 Reliant Labs

import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { HostedAppsState, ProjectInstallState, ProjectSchema } from "@/gen/reliant/v1/project_pb";

import { catalogFromProjects, catalogFromResponse, type ProjectCatalogRow, type WireProjectWithApps } from "../catalog";
import { listOf, machinesOf, mergeMoveLines, mergeTargetLabel, mergeTargets } from "../projectMerge";

interface Fixture {
  id: string;
  name?: string;
  remoteKey?: string;
  viewerAccess?: boolean;
  orgId?: string;
  machines?: string[];
}

function project(f: Fixture): WireProjectWithApps {
  return {
    project: create(ProjectSchema, {
      id: f.id,
      name: f.name ?? f.id,
      path: `/home/me/${f.id}`,
      lastActive: "2026-01-01T00:00:00Z",
      createdAt: "2025-01-01T00:00:00Z",
      updatedAt: "2025-01-01T00:00:00Z",
      remoteKey: f.remoteKey,
      orgId: f.orgId ?? "org-1",
      viewerAccess: f.viewerAccess ?? false,
    }),
    repos: [],
    apps: [],
    checkouts: (f.machines ?? []).map((name, i) => ({ daemonId: `d-${f.id}-${i}`, daemonName: name, installState: ProjectInstallState.INSTALLED })),
  };
}

function catalogOf(...fixtures: Fixture[]) {
  return catalogFromResponse({ orgId: "org-1", hostedAppsState: HostedAppsState.OK, projects: fixtures.map(project), unlinkedApps: [] });
}

const keysOf = (rows: ProjectCatalogRow[] | undefined) => rows?.map((r) => r.key);

describe("the catalog's remoteKey", () => {
  it("carries Project.remote_key onto the row, and leaves it off when unset", () => {
    const c = catalogOf({ id: "a", remoteKey: "github.com/acme/shop" }, { id: "b" });
    const [a, b] = c.rows as ProjectCatalogRow[];
    expect(a.remoteKey).toBe("github.com/acme/shop");
    expect(b).not.toHaveProperty("remoteKey");
  });
});

describe("mergeTargets", () => {
  it("pairs the person's own projects of one repository, each with the others", () => {
    const targets = mergeTargets(
      catalogOf(
        { id: "laptop", remoteKey: "github.com/acme/shop" },
        { id: "notes", remoteKey: "github.com/acme/notes" },
        { id: "cloud", remoteKey: "github.com/acme/shop" },
        { id: "third", remoteKey: "github.com/acme/shop" },
      ),
    );
    expect(keysOf(targets.get("laptop"))).toEqual(["cloud", "third"]);
    expect(keysOf(targets.get("cloud"))).toEqual(["laptop", "third"]);
    expect(keysOf(targets.get("third"))).toEqual(["laptop", "cloud"]);
    // One project of its repository: nothing to merge.
    expect(targets.has("notes")).toBe(false);
  });

  it("never offers a project shared with the person, on either side", () => {
    const targets = mergeTargets(
      catalogOf({ id: "mine", remoteKey: "github.com/acme/shop" }, { id: "teammate", remoteKey: "github.com/acme/shop", viewerAccess: true }),
    );
    expect(targets.size).toBe(0);
  });

  it("never pairs projects without a key (no remote, or a host-less one)", () => {
    const targets = mergeTargets(catalogOf({ id: "a" }, { id: "b" }, { id: "c", remoteKey: "" }, { id: "d", remoteKey: "  " }));
    expect(targets.size).toBe(0);
  });

  it("never pairs across orgs", () => {
    const targets = mergeTargets(
      catalogOf({ id: "a", remoteKey: "github.com/acme/shop", orgId: "org-1" }, { id: "b", remoteKey: "github.com/acme/shop", orgId: "org-2" }),
    );
    expect(targets.size).toBe(0);
  });

  it("offers nothing on a partial catalog or none at all", () => {
    expect(mergeTargets(undefined).size).toBe(0);
    const partial = catalogFromProjects("org-1", [
      { id: "a", name: "a", path: "/a", is_git_repo: true, is_forge: false, last_active: "", created_at: "", updated_at: "" },
    ]);
    expect(mergeTargets(partial).size).toBe(0);
  });
});

describe("mergeTargetLabel", () => {
  const c = catalogOf(
    { id: "a", name: "shop", remoteKey: "k", machines: ["laptop"] },
    { id: "b", name: "shop", remoteKey: "k", machines: ["cloud-1", "desk"] },
    { id: "c", name: "shop-old", remoteKey: "k" },
  );
  const [a, b, cRow] = c.rows as ProjectCatalogRow[];

  it("names the target, and its machines when the name alone would not tell it apart", () => {
    const targets = mergeTargets(c).get("a")!;
    expect(mergeTargetLabel(a, b, targets)).toBe("Merge into shop on cloud-1 and desk…");
    expect(mergeTargetLabel(a, cRow, targets)).toBe("Merge into shop-old…");
    expect(mergeTargetLabel(cRow, a, mergeTargets(c).get("c")!)).toBe("Merge into shop on laptop…");
  });

  it("says when an ambiguous target is on no machine", () => {
    const d = catalogOf({ id: "x", name: "shop", remoteKey: "k" }, { id: "y", name: "shop", remoteKey: "k" });
    const [x, y] = d.rows as ProjectCatalogRow[];
    expect(mergeTargetLabel(x, y, [y])).toBe("Merge into shop (on no machine)…");
  });
});

describe("words", () => {
  it("lists names", () => {
    expect(listOf([])).toBe("");
    expect(listOf(["a"])).toBe("a");
    expect(listOf(["a", "b"])).toBe("a and b");
    expect(listOf(["a", "b", "c"])).toBe("a, b and c");
    expect(machinesOf({ checkouts: [{ daemonId: "d", daemonName: "", installState: ProjectInstallState.INSTALLED }] })).toBe("an unnamed machine");
  });

  it("says what a merge moves, leaving out what it does not", () => {
    expect(
      mergeMoveLines(
        { chats: 3, worktrees: 1, checkouts: [{ daemonId: "d1", daemonName: "laptop" }, { daemonId: "d2", daemonName: "cloud" }], triggers: 0, repos: 2, otherRows: 1 },
        "shop",
      ),
    ).toEqual([
      "3 chats",
      "1 workspace",
      "Its checkouts on laptop and cloud, which become shop’s at the same paths",
      "2 repositories",
      "1 other record (settings, configs, catalogs and the like)",
    ]);
    expect(mergeMoveLines({ chats: 0, worktrees: 0, checkouts: [{ daemonId: "d1", daemonName: "laptop" }], triggers: 1, repos: 0, otherRows: 0 }, "shop")).toEqual([
      "Its checkout on laptop, which becomes shop’s at the same path",
      "1 automation",
    ]);
    expect(mergeMoveLines({ chats: 0, worktrees: 0, checkouts: [], triggers: 0, repos: 0, otherRows: 0 }, "shop")).toEqual([]);
    expect(mergeMoveLines(undefined, "shop")).toEqual([]);
  });
});
