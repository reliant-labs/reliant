// @vitest-environment node

/**
 * NOTHING THE APP RUNS NAMES A jsDelivr HOST.
 *
 * Monaco used to come from cdn.jsdelivr.net at runtime, so a blocked or slow
 * CDN meant no editor (and once, a failed release gate). It is now bundled
 * (lib/monacoBundle.ts); the two MCP avatar icons that came from the same CDN
 * are files of this build too. This scans everything that ships in, or
 * shapes, the bundle — the app's source, its HTML documents, public/, the
 * Vite config and plugins (the CSP), and the third-party Monaco wrappers that
 * are bundled with it — and fails on any jsdelivr.net URL.
 *
 * Tests, patches and docs may name it: they describe the history, and none of
 * them is in the bundle. If a feature truly needs a third-party host, serve
 * the file from this build instead.
 */

import { readdirSync, readFileSync, statSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join, relative } from "node:path";
import { describe, expect, it } from "vitest";

const WEB = join(__dirname, "..", "..", "..");
/** Any jsDelivr host: cdn.jsdelivr.net, fastly.jsdelivr.net, … */
const CDN = /jsdelivr\.net/i;

/** Directories and files whose contents are runtime code or shape it. */
const ROOTS = ["src", "public", "index.html", "find-bar.html", "demo.html", "vite.config.ts"];

/** The bundled dependencies that once defaulted to the CDN (the loader is patched). */
const DEPENDENCIES = ["@monaco-editor/loader/lib/es", "@monaco-editor/react/dist/index.mjs"];

function isTestFile(path: string): boolean {
  return /(^|\/)__tests__\//.test(path) || /\.(test|spec)\.[cm]?[jt]sx?$/.test(path) || path.startsWith("src/test/");
}

function walk(path: string, out: string[]): void {
  const stat = statSync(path);
  if (stat.isDirectory()) {
    for (const entry of readdirSync(path)) walk(join(path, entry), out);
  } else {
    out.push(path);
  }
}

function filesUnder(root: string): string[] {
  const out: string[] = [];
  walk(root, out);
  return out;
}

function offenders(files: string[], base: string): string[] {
  return files
    .filter((file) => CDN.test(readFileSync(file, "utf8")))
    .map((file) => relative(base, file));
}

describe("no jsDelivr in runtime code", () => {
  it("the app's source, documents, public files and build config name no jsDelivr", () => {
    const files = [
      ...ROOTS.flatMap((root) => filesUnder(join(WEB, root))),
      ...readdirSync(WEB)
        .filter((name) => /^vite-plugin-.*\.ts$/.test(name))
        .map((name) => join(WEB, name)),
    ].filter((file) => !isTestFile(relative(WEB, file)));

    expect(files.length).toBeGreaterThan(100);
    expect(offenders(files, WEB)).toEqual([]);
  });

  it("the bundled Monaco wrappers name no jsDelivr (patches/@monaco-editor__loader@1.7.0.patch)", () => {
    const nodeModules = join(dirname(createRequire(join(WEB, "package.json")).resolve("@monaco-editor/react/package.json")), "..", "..");
    const files = DEPENDENCIES.flatMap((dep) => filesUnder(join(nodeModules, dep)));

    expect(files.length).toBeGreaterThan(0);
    expect(offenders(files, nodeModules)).toEqual([]);
  });
});
