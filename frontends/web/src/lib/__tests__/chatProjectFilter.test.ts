// @vitest-environment node
import { describe, expect, it } from "vitest";

import {
  chatListScope,
  formatProjectsParam,
  MAX_FILTER_PROJECTS,
  normalizeProjectIds,
  parseProjectsParam,
  projectIdsOfScope,
  projectsSummary,
  sameProjectSet,
  scopeListsProject,
} from "../chatProjectFilter";

describe("chatProjectFilter", () => {
  it.each([
    ["a,b", ["a", "b"]],
    [" a , b ,a,,", ["a", "b"]],
    ["", []],
    [undefined, []],
    [42, ["42"]],
    [["x", 7, "x"], ["x", "7"]],
    [{ a: 1 }, []],
  ] as const)("parses ?projects=%j as %j", (raw, want) => {
    expect(parseProjectsParam(raw)).toEqual(want);
  });

  it("formats two or more as the param, fewer as none", () => {
    expect(formatProjectsParam(["a", "b"])).toBe("a,b");
    expect(formatProjectsParam(["a"])).toBeUndefined();
    expect(formatProjectsParam([])).toBeUndefined();
  });

  it("caps the ids at the server's limit", () => {
    expect(normalizeProjectIds(Array.from({ length: 250 }, (_, i) => `p${i}`))).toHaveLength(MAX_FILTER_PROJECTS);
  });

  it.each([
    [null, null],
    [[], null],
    ["p", "p"],
    [["p"], "p"],
    [["b", "a"], "projects:a,b"],
    [["a", "b"], "projects:a,b"],
  ] as const)("the list scope of %j is %j: one cache entry per set", (ids, want) => {
    expect(chatListScope(ids as never)).toBe(want);
  });

  it("reads a scope back, and says which projects it lists", () => {
    expect(projectIdsOfScope("projects:a,b")).toEqual(["a", "b"]);
    expect(projectIdsOfScope("p")).toEqual(["p"]);
    expect(projectIdsOfScope(null)).toEqual([]);
    expect(scopeListsProject(null, "x")).toBe(true);
    expect(scopeListsProject("projects:a,b", "b")).toBe(true);
    expect(scopeListsProject("projects:a,b", "c")).toBe(false);
    expect(scopeListsProject("a", "a")).toBe(true);
    expect(scopeListsProject("a", undefined)).toBe(false);
  });

  it("compares sets and summarises them", () => {
    expect(sameProjectSet(["a", "b"], ["b", "a"])).toBe(true);
    expect(sameProjectSet(["a"], ["a", "b"])).toBe(false);
    expect(projectsSummary(3)).toBe("3 projects");
    expect(projectsSummary(1)).toBe("1 project");
  });
});
