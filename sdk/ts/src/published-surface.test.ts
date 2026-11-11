// @vitest-environment node
// The publish contract: what a consumer resolves, and what it must never reach.
// `pretest` is not used (a build per test run is waste); the dist-reading checks
// build once here, into a scratch directory, so they always judge current sources.
import { spawnSync } from "node:child_process";
import { mkdtempSync, readdirSync, readFileSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { beforeAll, describe, expect, it } from "vitest";

const pkgDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const pkg = JSON.parse(readFileSync(join(pkgDir, "package.json"), "utf8")) as {
  name: string;
  license: string;
  files: string[];
  exports: Record<string, { types: string; default: string } | string>;
  imports: Record<string, string>;
  peerDependencies: Record<string, string>;
  peerDependenciesMeta?: Record<string, { optional?: boolean }>;
  dependencies?: Record<string, string>;
  scripts: Record<string, string>;
};

let outDir = "";
const jsFiles = (dir: string): string[] =>
  readdirSync(dir).flatMap((name) => {
    const p = join(dir, name);
    return statSync(p).isDirectory() ? jsFiles(p) : p.endsWith(".js") ? [p] : [];
  });

beforeAll(() => {
  outDir = mkdtempSync(join(tmpdir(), "reliant-sdk-dist-"));
  const tsc = join(pkgDir, "node_modules", "typescript", "bin", "tsc");
  const r = spawnSync(process.execPath, [tsc, "-p", "tsconfig.build.json", "--outDir", outDir], { cwd: pkgDir, encoding: "utf8" });
  if (r.status !== 0) throw new Error(`build failed:\n${r.stdout}${r.stderr}`);
}, 120_000);

describe("the manifest", () => {
  it("is Apache-2.0 and ships a LICENSE and NOTICE of its own", () => {
    expect(pkg.license).toBe("Apache-2.0");
    expect(readFileSync(join(pkgDir, "LICENSE"), "utf8")).toContain("Apache License");
    expect(readFileSync(join(pkgDir, "NOTICE"), "utf8")).toContain("Reliant Labs");
    expect(pkg.files).toEqual(["dist", "README.md", "NOTICE"]);
  });

  it("resolves only into dist/, and publishes exactly the /client and /react subpaths", () => {
    expect(Object.keys(pkg.exports).sort()).toEqual(["./client", "./package.json", "./react"]);
    for (const e of Object.values(pkg.exports)) {
      for (const target of typeof e === "string" ? [e] : [e.types, e.default]) {
        expect(target.startsWith("./src/"), `${target} points into src/`).toBe(false);
      }
    }
    expect(pkg.imports["#gen/*"]).toBe("./dist/gen/*");
  });

  it("builds on prepack, not on install", () => {
    expect(pkg.scripts.prepack).toBeDefined();
    expect(pkg.scripts.prepare).toBeUndefined();
    expect(pkg.scripts.postinstall).toBeUndefined();
  });

  it("brings its own runtime dependencies as peers, and has none of its own", () => {
    expect(pkg.dependencies).toBeUndefined();
    expect(Object.keys(pkg.peerDependencies).sort()).toEqual([
      "@bufbuild/protobuf",
      "@connectrpc/connect",
      "@opentelemetry/api",
      "@reliantlabs/forge-web-runtime",
      "@tanstack/react-query",
      "react",
    ]);
  });

  it("makes React and React Query optional: only /react reaches them", () => {
    expect(pkg.peerDependenciesMeta).toEqual({ react: { optional: true }, "@tanstack/react-query": { optional: true } });
  });
});

describe("the built /client", () => {
  const allowedBare = new Set([
    "@bufbuild/protobuf",
    "@bufbuild/protobuf/codegenv2",
    "@bufbuild/protobuf/wkt",
    "@connectrpc/connect",
    "@reliantlabs/forge-web-runtime/interceptors",
  ]);
  // Only real import/export-from statements and dynamic imports, never prose in a comment.
  const specifiers = (file: string): string[] =>
    [
      ...readFileSync(file, "utf8").matchAll(/^\s*(?:import|export)\b[^;"']*?\bfrom\s*["']([^"']+)["']|^\s*import\s*["']([^"']+)["']|\bimport\(\s*["']([^"']+)["']/gm),
    ].map((m) => (m[1] ?? m[2] ?? m[3])!);

  it("imports no Node built-in, no React and no DOM-only package", () => {
    const offenders: string[] = [];
    for (const file of [...jsFiles(join(outDir, "client")), ...jsFiles(join(outDir, "gen"))]) {
      for (const spec of specifiers(file)) {
        if (spec.startsWith(".") || spec.startsWith("#")) continue;
        if (!allowedBare.has(spec)) offenders.push(`${relative(outDir, file)}: ${spec}`);
      }
    }
    expect(offenders).toEqual([]);
  });

  it("never reaches into /react", () => {
    const offenders: string[] = [];
    for (const file of jsFiles(join(outDir, "client"))) {
      for (const spec of specifiers(file)) if (/(^|\/)react(\/|$)/.test(spec) && spec.startsWith(".")) offenders.push(`${relative(outDir, file)}: ${spec}`);
    }
    expect(offenders).toEqual([]);
  });

  it("uses only imports that carry a .js extension, as Node ESM requires", () => {
    const bad: string[] = [];
    for (const file of jsFiles(outDir)) {
      for (const spec of specifiers(file)) {
        if ((spec.startsWith(".") || spec.startsWith("#")) && !spec.endsWith(".js")) bad.push(`${relative(outDir, file)}: ${spec}`);
      }
    }
    expect(bad).toEqual([]);
  });

  it("touches no process, fs, Buffer or other Node global", () => {
    const hits: string[] = [];
    for (const file of jsFiles(join(outDir, "client"))) {
      const src = readFileSync(file, "utf8");
      for (const needle of [/\bprocess\./, /\brequire\(/, /\bBuffer\b/, /__dirname/]) {
        if (needle.test(src)) hits.push(`${relative(outDir, file)}: ${needle}`);
      }
    }
    expect(hits).toEqual([]);
  });
});

describe("the built /react", () => {
  const allowedBare = new Set([
    "@bufbuild/protobuf",
    "@connectrpc/connect",
    "@reliantlabs/forge-web-runtime/service-hooks",
    "@tanstack/react-query",
    "react",
  ]);
  const specifiers = (file: string): string[] =>
    [
      ...readFileSync(file, "utf8").matchAll(/^\s*(?:import|export)\b[^;"']*?\bfrom\s*[\"']([^\"']+)[\"']|^\s*import\s*[\"']([^\"']+)[\"']|\bimport\(\s*[\"']([^\"']+)[\"']/gm),
    ].map((m) => (m[1] ?? m[2] ?? m[3])!);

  it("reaches only React, React Query, forge's service-hooks and the client", () => {
    const offenders: string[] = [];
    for (const file of jsFiles(join(outDir, "react"))) {
      for (const spec of specifiers(file)) {
        if (spec.startsWith(".") || spec.startsWith("#")) continue;
        if (!allowedBare.has(spec)) offenders.push(`${relative(outDir, file)}: ${spec}`);
      }
    }
    expect(offenders).toEqual([]);
  });

  it("marks every module that uses React as a client module", () => {
    const missing: string[] = [];
    for (const file of jsFiles(join(outDir, "react"))) {
      const src = readFileSync(file, "utf8");
      const usesReact = specifiers(file).some((s) => s === "react" || s === "@tanstack/react-query");
      if (usesReact && !/^\s*(?:\/\/[^\n]*\n|\/\*[\s\S]*?\*\/\s*)*["']use client["']/.test(src)) missing.push(relative(outDir, file));
    }
    expect(missing).toEqual([]);
  });

  it("touches no process, fs, Buffer or other Node global", () => {
    const hits: string[] = [];
    for (const file of jsFiles(join(outDir, "react"))) {
      const src = readFileSync(file, "utf8");
      for (const needle of [/\bprocess\./, /\brequire\(/, /\bBuffer\b/, /__dirname/]) {
        if (needle.test(src)) hits.push(`${relative(outDir, file)}: ${needle}`);
      }
    }
    expect(hits).toEqual([]);
  });
});
