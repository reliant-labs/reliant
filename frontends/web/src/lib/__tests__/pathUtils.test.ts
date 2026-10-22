/**
 * Separator-aware path helpers.
 *
 * The invariant that matters most here is round-tripping: a Windows path
 * belongs to a Windows daemon and must come back out in the same form it went
 * in. Several cases below exist purely to pin that no helper silently
 * normalizes `\` to `/`.
 */
import { describe, expect, it } from "vitest";

import {
  basename,
  collapseHomePath,
  containsSeparator,
  dirname,
  examplePathForPlatform,
  isAbsolutePath,
  isPathRoot,
  isWindowsPath,
  joinPath,
  pathCrumbs,
  pathRoot,
  pathSeparator,
  splitPathForDisplay,
} from "../pathUtils";

describe("isAbsolutePath", () => {
  it.each([
    ["/Users/sean/projects/app", true],
    ["/", true],
    ["C:\\Users\\sean\\projects\\app", true],
    ["C:/Users/sean/projects/app", true],
    ["c:\\projects", true],
    ["D:\\", true],
    ["\\\\build-server\\share\\app", true],
    ["\\\\build-server\\share", true],
    ["projects/app", false],
    ["./projects/app", false],
    ["../app", false],
    ["", false],
    // Drive-relative: on Windows `C:foo` means "foo under the current
    // directory of C:", which is not a root.
    ["C:foo", false],
    ["C:", false],
    // A single backslash is not a UNC root.
    ["\\projects", false],
  ])("%s -> %s", (path, expected) => {
    expect(isAbsolutePath(path)).toBe(expected);
  });
});

describe("isWindowsPath", () => {
  it.each([
    ["C:\\Users\\sean", true],
    ["C:/Users/sean", true],
    ["\\\\srv\\share\\a", true],
    ["/Users/sean", false],
    ["projects/app", false],
  ])("%s -> %s", (path, expected) => {
    expect(isWindowsPath(path)).toBe(expected);
  });
});

describe("pathSeparator", () => {
  it.each([
    ["/Users/sean", "/"],
    ["C:\\Users\\sean", "\\"],
    ["C:/Users/sean", "/"],
    ["\\\\srv\\share\\a", "\\"],
  ])("%s -> %s", (path, expected) => {
    expect(pathSeparator(path)).toBe(expected);
  });
});

describe("pathRoot / isPathRoot", () => {
  it.each([
    ["/Users/sean", "/"],
    ["/", "/"],
    ["C:\\Users\\sean", "C:\\"],
    ["C:/Users/sean", "C:/"],
    ["C:\\", "C:\\"],
    ["\\\\srv\\share\\a\\b", "\\\\srv\\share\\"],
    ["projects/app", ""],
  ])("root of %s is %s", (path, expected) => {
    expect(pathRoot(path)).toBe(expected);
  });

  it.each([
    ["/", true],
    ["C:\\", true],
    ["C:/", true],
    ["\\\\srv\\share\\", true],
    ["\\\\srv\\share", true],
    ["/Users", false],
    ["C:\\Users", false],
    ["projects", false],
  ])("isPathRoot(%s) -> %s", (path, expected) => {
    expect(isPathRoot(path)).toBe(expected);
  });
});

describe("basename", () => {
  it.each([
    ["/Users/sean/projects/app", "app"],
    ["/Users/sean/projects/app/", "app"],
    ["/Users/sean/projects/app///", "app"],
    ["C:\\Users\\sean\\projects\\app", "app"],
    ["C:\\Users\\sean\\projects\\app\\", "app"],
    ["C:/Users/sean/projects/app", "app"],
    ["\\\\srv\\share\\team\\app", "app"],
    // A root has no name of its own.
    ["/", ""],
    ["C:\\", ""],
    ["\\\\srv\\share", ""],
    ["", ""],
    ["app", "app"],
  ])("%s -> %s", (path, expected) => {
    expect(basename(path)).toBe(expected);
  });
});

describe("dirname", () => {
  it.each([
    ["/Users/sean/projects/app", "/Users/sean/projects"],
    ["/Users", "/"],
    ["C:\\Users\\sean\\projects\\app", "C:\\Users\\sean\\projects"],
    ["C:\\Users", "C:\\"],
    ["C:/Users", "C:/"],
    ["\\\\srv\\share\\team\\app", "\\\\srv\\share\\team"],
    ["\\\\srv\\share\\team", "\\\\srv\\share\\"],
    // A root is its own parent, so an "up" control clamps at the volume top.
    ["/", "/"],
    ["C:\\", "C:\\"],
    ["\\\\srv\\share\\", "\\\\srv\\share\\"],
  ])("%s -> %s", (path, expected) => {
    expect(dirname(path)).toBe(expected);
  });
});

describe("joinPath", () => {
  it.each([
    ["/Users/sean", "app", "/Users/sean/app"],
    ["/Users/sean/", "app", "/Users/sean/app"],
    ["/", "Users", "/Users"],
    ["C:\\Users\\sean", "app", "C:\\Users\\sean\\app"],
    ["C:\\", "Users", "C:\\Users"],
    ["C:/Users", "sean", "C:/Users/sean"],
    ["\\\\srv\\share", "team", "\\\\srv\\share\\team"],
    ["\\\\srv\\share\\", "team", "\\\\srv\\share\\team"],
  ])("%s + %s -> %s", (parent, segment, expected) => {
    expect(joinPath(parent, segment)).toBe(expected);
  });

  it("never mixes separators", () => {
    expect(joinPath("C:\\Users\\sean", "app")).not.toContain("/");
  });
});

describe("pathCrumbs", () => {
  it("labels the POSIX root as /", () => {
    expect(pathCrumbs("/Users/sean")).toEqual([
      { name: "/", path: "/" },
      { name: "Users", path: "/Users" },
      { name: "sean", path: "/Users/sean" },
    ]);
  });

  it("labels the Windows root as the drive, which is the top of that volume", () => {
    expect(pathCrumbs("C:\\Users\\sean")).toEqual([
      { name: "C:\\", path: "C:\\" },
      { name: "Users", path: "C:\\Users" },
      { name: "sean", path: "C:\\Users\\sean" },
    ]);
  });

  it("treats the UNC share as the root", () => {
    expect(pathCrumbs("\\\\srv\\share\\team")).toEqual([
      { name: "\\\\srv\\share\\", path: "\\\\srv\\share\\" },
      { name: "team", path: "\\\\srv\\share\\team" },
    ]);
  });

  it("has no crumbs for a relative or empty path", () => {
    expect(pathCrumbs("projects/app")).toEqual([]);
    expect(pathCrumbs("")).toEqual([]);
  });
});

describe("collapseHomePath", () => {
  it.each([
    ["/Users/sean/projects/app", "~/projects/app"],
    ["/home/sean/projects/app", "~/projects/app"],
    ["C:\\Users\\sean\\projects\\app", "~\\projects\\app"],
    ["C:/Users/sean/projects/app", "~/projects/app"],
    ["D:\\Users\\sean\\code", "~\\code"],
    ["C:\\Documents and Settings\\sean\\code", "~\\code"],
    // Not a home directory: left alone.
    ["/opt/src/app", "/opt/src/app"],
    ["C:\\projects\\app", "C:\\projects\\app"],
  ])("%s -> %s", (path, expected) => {
    expect(collapseHomePath(path)).toBe(expected);
  });
});

describe("splitPathForDisplay", () => {
  it("splits a POSIX path before the last segment", () => {
    expect(splitPathForDisplay("~/src/deep/app")).toEqual({
      head: "~/src/deep/",
      tail: "app",
    });
  });

  it("splits a Windows path before the last segment", () => {
    expect(splitPathForDisplay("~\\src\\deep\\app")).toEqual({
      head: "~\\src\\deep\\",
      tail: "app",
    });
  });

  it("puts a bare name entirely in the tail", () => {
    expect(splitPathForDisplay("app")).toEqual({ head: "", tail: "app" });
  });
});

describe("containsSeparator", () => {
  it.each([
    ["app", false],
    ["my app", false],
    ["a/b", true],
    ["a\\b", true],
  ])("%s -> %s", (name, expected) => {
    expect(containsSeparator(name)).toBe(expected);
  });
});

describe("examplePathForPlatform", () => {
  it("shows a drive path to Windows users", () => {
    expect(examplePathForPlatform("windows")).toBe("C:\\Users\\you\\projects\\my-app");
  });

  it("shows a POSIX path everywhere else", () => {
    for (const os of ["mac-arm64", "mac-x64", "linux", "unknown"]) {
      expect(examplePathForPlatform(os)).toBe("/Users/you/projects/my-app");
    }
  });
});
