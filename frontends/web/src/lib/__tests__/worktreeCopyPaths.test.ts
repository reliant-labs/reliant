import { describe, expect, it } from "vitest";
import {
  copyPathError,
  parseCopyPathsInput,
  repoPathToWorkspacePath,
} from "../worktreeCopyPaths";

describe("parseCopyPathsInput", () => {
  it("splits on commas and drops blanks", () => {
    expect(parseCopyPathsInput(" .env, reliant/.env ,, web/node_modules/ ")).toEqual([
      ".env",
      "reliant/.env",
      "web/node_modules/",
    ]);
    expect(parseCopyPathsInput("")).toEqual([]);
  });
});

describe("copyPathError", () => {
  it.each([".env", "reliant/.env", "web/node_modules/", "./a/../b"])("accepts %s", (entry) => {
    expect(copyPathError(entry)).toBeNull();
  });

  it.each(["/etc/passwd", "C:\\secrets", "../x", "a/../../x", ".", "./", "  "])("rejects %s", (entry) => {
    expect(copyPathError(entry)).not.toBeNull();
  });
});

describe("repoPathToWorkspacePath", () => {
  it("prefixes a nested repo's path", () => {
    expect(repoPathToWorkspacePath("reliant", "web/src/a.ts")).toBe("reliant/web/src/a.ts");
    expect(repoPathToWorkspacePath("apps/web/", ".env")).toBe("apps/web/.env");
  });

  it("leaves a root repo's path alone", () => {
    expect(repoPathToWorkspacePath("", ".env")).toBe(".env");
    expect(repoPathToWorkspacePath(".", ".env")).toBe(".env");
  });
});
