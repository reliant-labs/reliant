import { describe, it, expect } from "vitest";
import { deriveVisibleWorktrees } from "../discoverWorktreesUtils";
import type { DiscoveredWorktree } from "../../../store/worktreeStore";

const fixture: DiscoveredWorktree[] = [
  {
    name: "alpha",
    branch: "feature/search",
    path: "/tmp/alpha",
    is_imported: false,
    is_prunable: false,
  },
  {
    name: "beta",
    branch: "feature/ui",
    path: "/tmp/beta",
    is_imported: true,
    is_prunable: true,
  },
  {
    name: "gamma",
    branch: "bugfix/login",
    path: "/Users/test/worktrees/gamma",
    is_imported: false,
    is_prunable: true,
  },
];

describe("deriveVisibleWorktrees", () => {
  it("filters to unimported by default", () => {
    const result = deriveVisibleWorktrees(fixture, {
      search: "",
      statusFilter: "unimported",
      sortField: "name",
      sortDirection: "asc",
    });

    expect(result.map((w) => w.name)).toEqual(["alpha", "gamma"]);
  });

  it("matches search against name, branch, and path (case-insensitive)", () => {
    const byName = deriveVisibleWorktrees(fixture, {
      search: "ALPHA",
      statusFilter: "all",
      sortField: "name",
      sortDirection: "asc",
    });

    const byBranch = deriveVisibleWorktrees(fixture, {
      search: "bugfix",
      statusFilter: "all",
      sortField: "name",
      sortDirection: "asc",
    });

    const byPath = deriveVisibleWorktrees(fixture, {
      search: "worktrees/gamma",
      statusFilter: "all",
      sortField: "name",
      sortDirection: "asc",
    });

    expect(byName.map((w) => w.name)).toEqual(["alpha"]);
    expect(byBranch.map((w) => w.name)).toEqual(["gamma"]);
    expect(byPath.map((w) => w.name)).toEqual(["gamma"]);
  });

  it("supports imported-only filter", () => {
    const importedOnly = deriveVisibleWorktrees(fixture, {
      search: "",
      statusFilter: "imported",
      sortField: "name",
      sortDirection: "asc",
    });

    expect(importedOnly.map((w) => w.name)).toEqual(["beta"]);
  });

  it("sorts by field and direction", () => {
    const byBranchAsc = deriveVisibleWorktrees(fixture, {
      search: "",
      statusFilter: "all",
      sortField: "branch",
      sortDirection: "asc",
    });

    const byPathDesc = deriveVisibleWorktrees(fixture, {
      search: "",
      statusFilter: "all",
      sortField: "path",
      sortDirection: "desc",
    });

    expect(byBranchAsc.map((w) => w.name)).toEqual(["gamma", "alpha", "beta"]);
    expect(byPathDesc.map((w) => w.name)).toEqual(["gamma", "beta", "alpha"]);
  });
});
