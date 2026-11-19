// @vitest-environment node

/**
 * MONACO REACHES THE APP ONE WAY: lazily, through lib/monacoManager.
 *
 *   1. Only lib/monacoBundle.ts imports `monaco-editor` as a value. It is the
 *      editor itself, megabytes of it.
 *   2. Only lib/monacoManager.ts imports lib/monacoBundle, and only with
 *      `import()`: a static import would put the editor in whatever chunk
 *      imported it, the entry included.
 *   3. `@monaco-editor/*` is imported as a value only with `import()`, and
 *      only by lib/monacoManager.ts and components/shared/MonacoEditor.tsx.
 *      @monaco-editor/react's `<Editor>` makes the loader fetch Monaco itself
 *      unless monacoManager has configured it first, and MonacoEditor is the
 *      component that waits for that.
 *
 * Type-only imports (`import type`, `import("monaco-editor").editor.X`) are
 * erased by the compiler and are fine anywhere. Tests are not scanned.
 */

import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";

const SRC = join(__dirname, "..", "..");

const BUNDLE = "lib/monacoBundle.ts";
const MANAGER = "lib/monacoManager.ts";
const EDITOR = "components/shared/MonacoEditor.tsx";

function sourceFiles(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry);
    if (statSync(path).isDirectory()) {
      if (entry !== "__tests__" && entry !== "test") sourceFiles(path, out);
    } else if (/\.[cm]?[jt]sx?$/.test(entry) && !/\.(test|spec)\.[cm]?[jt]sx?$/.test(entry) && !entry.endsWith(".d.ts")) {
      out.push(path);
    }
  }
  return out;
}

type ImportSite = { file: string; specifier: string; kind: "static" | "dynamic" };

// `import x from "s"`, `import "s"`, `export … from "s"`; not `import type`/`export type`.
const STATIC = /^\s*(?:import|export)\s+(?!type\s)(?:[^;]*?\sfrom\s+)?["']([^"']+)["']/gm;
// `import("s")` that is not a type position (`import("s").Name`, other than a promise method).
const DYNAMIC = /\bimport\(\s*["']([^"']+)["']\s*\)(?!\.(?!then\b|catch\b|finally\b)[A-Za-z_$])/g;

function importSites(): ImportSite[] {
  return sourceFiles(SRC).flatMap((path) => {
    const file = relative(SRC, path);
    const text = readFileSync(path, "utf8");
    return [
      ...[...text.matchAll(STATIC)].map((m) => ({ file, specifier: m[1], kind: "static" as const })),
      ...[...text.matchAll(DYNAMIC)].map((m) => ({ file, specifier: m[1], kind: "dynamic" as const })),
    ];
  });
}

const isBundle = (specifier: string) => /(^|\/)monacoBundle(\.ts)?$/.test(specifier);
const describeSite = (site: ImportSite) => `${site.file}: ${site.kind} import of ${site.specifier}`;

describe("Monaco imports", () => {
  const sites = importSites();

  it("finds the imports it guards (the scan is not vacuous)", () => {
    expect(sites.some((site) => site.file === BUNDLE && site.specifier === "monaco-editor")).toBe(true);
    expect(sites.some((site) => site.file === MANAGER && isBundle(site.specifier) && site.kind === "dynamic")).toBe(true);
    expect(sites.some((site) => site.file === EDITOR && site.specifier === "@monaco-editor/react" && site.kind === "dynamic")).toBe(true);
  });

  it("only lib/monacoBundle.ts imports monaco-editor as a value", () => {
    const offenders = sites.filter((site) => site.specifier.startsWith("monaco-editor") && site.file !== BUNDLE);
    expect(offenders.map(describeSite)).toEqual([]);
  });

  it("only lib/monacoManager.ts imports lib/monacoBundle, and lazily", () => {
    const offenders = sites.filter((site) => isBundle(site.specifier) && (site.file !== MANAGER || site.kind !== "dynamic"));
    expect(offenders.map(describeSite)).toEqual([]);
  });

  it("@monaco-editor/* is imported lazily, by the manager and the MonacoEditor component alone", () => {
    const offenders = sites.filter(
      (site) => site.specifier.startsWith("@monaco-editor/") && (site.kind !== "dynamic" || (site.file !== MANAGER && site.file !== EDITOR)),
    );
    expect(offenders.map(describeSite)).toEqual([]);
  });
});
