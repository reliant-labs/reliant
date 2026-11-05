import { describe, expect, it } from "vitest";
import { isTreePath, treeAncestorDirs, withAncestorsExpanded } from "./treePaths";

describe("treeAncestorDirs", () => {
  it("lists a checkout-relative path's directories, outermost first", () => {
    expect(treeAncestorDirs("src/components/a.ts")).toEqual(["src", "src/components"]);
    expect(treeAncestorDirs("README.md")).toEqual([]);
  });

  it("expands nothing toward a path the tree cannot list", () => {
    // The 2026-11-05 poll: these produced GetFileTree for /home, /home/workspace,
    // /home/workspace/projects and ~, ~/projects, ... on every refresh.
    expect(treeAncestorDirs("/home/workspace/projects/reliant-labs/README.md")).toEqual([]);
    expect(treeAncestorDirs("~/projects/growth/content/eng-series/drafts/<slug>/draft.md")).toEqual([]);
    expect(treeAncestorDirs("~")).toEqual([]);
  });
});

describe("isTreePath", () => {
  it("accepts relative paths and refuses absolute and home-relative ones", () => {
    expect(isTreePath("src")).toBe(true);
    expect(isTreePath("~notes/a.md")).toBe(true); // a directory literally named "~notes"
    expect(isTreePath("/etc")).toBe(false);
    expect(isTreePath("~/x")).toBe(false);
    expect(isTreePath("")).toBe(false);
  });
});

describe("withAncestorsExpanded", () => {
  it("returns the same set when nothing is added", () => {
    const expanded = new Set(["src"]);
    expect(withAncestorsExpanded(expanded, "src/a.ts")).toBe(expanded);
    expect(withAncestorsExpanded(expanded, "/home/workspace/a.ts")).toBe(expanded);
  });

  it("adds the missing ancestors without mutating its input", () => {
    const expanded = new Set(["src"]);
    const next = withAncestorsExpanded(expanded, "src/deep/a.ts");
    expect([...next].sort()).toEqual(["src", "src/deep"]);
    expect([...expanded]).toEqual(["src"]);
  });
});
