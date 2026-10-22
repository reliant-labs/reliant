import { describe, expect, it } from "vitest";
import { defaultBaseBranch, normalizeWorkspaceName } from "../workspaceNaming";

describe("normalizeWorkspaceName", () => {
  it("trims and collapses whitespace runs into single hyphens", () => {
    expect(normalizeWorkspaceName("  fix   login\tflow ")).toBe("fix-login-flow");
  });

  it("leaves an already-valid name alone", () => {
    expect(normalizeWorkspaceName("feature/x-1")).toBe("feature/x-1");
  });
});

describe("defaultBaseBranch", () => {
  it("is the main checkout's current local branch", () => {
    expect(
      defaultBaseBranch([
        { name: "origin/main", is_current: true, is_remote: true },
        { name: "develop", is_current: true, is_remote: false },
      ]),
    ).toBe("develop");
  });

  it("uses the full SHA for a detached HEAD", () => {
    expect(
      defaultBaseBranch([
        { name: "abc1234", is_current: true, is_remote: false, is_detached: true, commit_sha: "abc1234deadbeef" },
      ]),
    ).toBe("abc1234deadbeef");
  });

  it("says nothing rather than guessing when no branch is checked out", () => {
    expect(defaultBaseBranch([])).toBeUndefined();
    expect(defaultBaseBranch([{ name: "main", is_current: false, is_remote: false }])).toBeUndefined();
  });
});
