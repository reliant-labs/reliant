// @vitest-environment node
// Copyright (c) 2026 Reliant Labs

/**
 * NO TWO MODULES IN ONE DIRECTORY DIFFER ONLY BY CASE.
 *
 * macOS's default file system is case-insensitive, Linux's is not. With
 * `OnboardingGate.tsx` beside `onboardingGatePolicy.ts`, `import "./OnboardingGate"`
 * resolves to the .tsx on Linux, but on a Mac the resolver tries `.ts` first
 * and finds `onboardingGatePolicy.ts`: `vite build`, `vite` dev and `tsc -b` all
 * fail there ("OnboardingGate" is not exported), while CI stays green. So this
 * fails on any two entries of one directory whose import specifiers differ
 * only by case: `Foo.tsx` beside `foo.ts`, `Foo.ts` beside `foo.ts`, or a
 * `Foo/` directory with an index module beside `foo.ts`.
 */

import { existsSync, readdirSync } from "node:fs";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";

const WEB = join(__dirname, "..", "..");
const REPO = join(WEB, "..", "..");

/** The trees whose imports a case-insensitive file system resolves. */
const ROOTS = [WEB, join(REPO, "electron")].filter((root) => existsSync(root));

const SKIPPED_DIRS = new Set(["node_modules", "dist", "out", "build", "coverage", "test-results", "playwright-report"]);

/** Extensions an extensionless import may resolve to, longest first. */
const MODULE_EXTENSIONS = [".d.ts", ".tsx", ".ts", ".jsx", ".js", ".mts", ".cts", ".mjs", ".cjs"];

/** The name `import "./<name>"` would spell for this entry. */
function specifierOf(name: string, isDirectory: boolean): string {
  if (isDirectory) return name;
  const extension = MODULE_EXTENSIONS.find((candidate) => name.endsWith(candidate));
  return extension ? name.slice(0, -extension.length) : name;
}

/**
 * Whether `import "./<dir>"` resolves to this directory: only through an index
 * module. A file tried as `<dir>.ts` wins over the directory, so `Sidebar.tsx`
 * beside an index-less `sidebar/` (imported as `./sidebar/navConfig`) is safe.
 */
function importable(dir: string): boolean {
  return MODULE_EXTENSIONS.some((extension) => existsSync(join(dir, `index${extension}`)));
}

/** Every group of entries in one directory whose specifiers differ only by case. */
export function caseCollisions(dir: string): string[][] {
  const entries = readdirSync(dir, { withFileTypes: true });
  const bySpecifier = new Map<string, Map<string, string[]>>();
  const found: string[][] = [];
  for (const entry of entries) {
    const isDirectory = entry.isDirectory();
    if (isDirectory && (entry.name.startsWith(".") || SKIPPED_DIRS.has(entry.name))) continue;
    if (isDirectory) found.push(...caseCollisions(join(dir, entry.name)));
    if (isDirectory && !importable(join(dir, entry.name))) continue;
    const specifier = specifierOf(entry.name, isDirectory);
    const spellings = bySpecifier.get(specifier.toLowerCase()) ?? new Map<string, string[]>();
    spellings.set(specifier, [...(spellings.get(specifier) ?? []), entry.name]);
    bySpecifier.set(specifier.toLowerCase(), spellings);
  }
  for (const spellings of bySpecifier.values()) {
    if (spellings.size < 2) continue;
    found.push([...spellings.values()].flat().map((name) => join(dir, name)));
  }
  return found;
}

describe("file names", () => {
  it("never differ only by case within one directory", () => {
    const collisions = ROOTS.flatMap(caseCollisions).map((group) =>
      group.map((path) => relative(REPO, path)).sort().join(" vs "),
    );
    expect(collisions, "rename one of each pair: a case-insensitive file system (macOS) resolves them as one").toEqual([]);
  });
});
