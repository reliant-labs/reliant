// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The "@reliantlabs/forge-web-runtime/interceptors" entry point promises ONE
// thing the barrel cannot: it drags no React into the consumer's program at
// all. A React Native app renders none of this package's components — they
// emit <table>, <input>, <div> — so pulling their module graph in would cost
// a bundle's worth of dead code and bind React on a platform whose renderer
// is not react-dom.
//
// The promise is transitive, which makes it easy to break by accident: an
// innocent `import { something } from "./session.js"` three modules deep is
// enough. This test walks the entry point's real import graph and fails the
// moment React appears anywhere in it. It also pins the two shapes a
// consumer resolves the subpath through — the `exports` map for modern
// resolvers, and the interceptors/ directory shim for the node10-era
// resolvers React Native still ships (Metro with package exports off,
// expo/tsconfig.base's moduleResolution: node10). Both must land in dist/,
// because dist/ is the whole of what this package publishes; see
// published-surface.test.ts.
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

const srcDir = dirname(fileURLToPath(import.meta.url));
const pkgDir = resolve(srcDir, "..");

/** The source module behind the subpath, and what it is published as. */
const ENTRY_SRC = "src/interceptors.ts";
const ENTRY_TYPES = "./dist/interceptors.d.ts";
const ENTRY_JS = "./dist/interceptors.js";

/**
 * localImports returns the package-relative paths this file imports from
 * within the package, plus the bare specifiers it pulls from node_modules.
 */
function readModule(relPath: string): { source: string; specifiers: string[] } {
  const source = readFileSync(join(pkgDir, relPath), "utf8");
  const specifiers: string[] = [];
  // Matches `from "x"` in static imports/re-exports and `import("x")`.
  const re = /(?:from|import)\s*\(?\s*["']([^"']+)["']/g;
  for (const m of source.matchAll(re)) {
    if (m[1]) {
      specifiers.push(m[1]);
    }
  }
  return { source, specifiers };
}

/**
 * resolveLocal maps a relative specifier onto the source file it names.
 *
 * Sources spell relative imports with the ".js" extension the EMITTED module
 * will need (the package is "type": "module" and Node does not guess), so
 * resolution here does what TypeScript and every bundler do: try the source
 * extensions behind that ".js" first, then the literal path.
 */
function resolveLocal(fromRel: string, spec: string): string | null {
  if (!spec.startsWith(".")) {
    return null;
  }
  const base = resolve(dirname(join(pkgDir, fromRel)), spec);
  const stem = base.endsWith(".js") ? base.slice(0, -".js".length) : base;
  for (const candidate of [
    `${stem}.ts`,
    `${stem}.tsx`,
    base,
    join(stem, "index.ts"),
    join(stem, "index.tsx"),
  ]) {
    try {
      readFileSync(candidate);
      return candidate.slice(pkgDir.length + 1);
    } catch {
      // try the next candidate shape
    }
  }
  throw new Error(`unresolvable local import ${spec} from ${fromRel}`);
}

/** Every in-package module reachable from `entry`, including `entry`. */
function moduleGraph(entry: string): { files: string[]; bare: string[] } {
  const files: string[] = [];
  const bare = new Set<string>();
  const queue = [entry];
  while (queue.length > 0) {
    const current = queue.shift();
    if (current === undefined || files.includes(current)) {
      continue;
    }
    files.push(current);
    for (const spec of readModule(current).specifiers) {
      const local = resolveLocal(current, spec);
      if (local === null) {
        bare.add(spec);
      } else {
        queue.push(local);
      }
    }
  }
  return { files, bare: [...bare].sort() };
}

describe("the ./interceptors entry point", () => {
  it("reaches no React anywhere in its import graph", () => {
    const { files, bare } = moduleGraph(ENTRY_SRC);

    // No .tsx in the graph: JSX is the surest proxy for a React component.
    expect(files.filter((f) => f.endsWith(".tsx"))).toEqual([]);

    // No React package, direct or transitive.
    const reactish = bare.filter((s) =>
      /^react(-dom)?(\/|$)|^@types\/react/.test(s),
    );
    expect(reactish).toEqual([]);

    // Pin the exact graph. A new module showing up here is a decision that
    // should be made deliberately, not inherited from a stray import.
    expect(files.sort()).toEqual([
      "src/errors.ts",
      "src/interceptors.ts",
      "src/telemetry-gate.ts",
      "src/trace.ts",
    ]);
    expect(bare).toEqual([
      "@bufbuild/protobuf/wkt",
      "@connectrpc/connect",
      "@opentelemetry/api",
    ]);
  });

  it("is declared for exports-aware and node10-era resolvers alike", () => {
    const pkg = JSON.parse(
      readFileSync(join(pkgDir, "package.json"), "utf8"),
    ) as {
      files: string[];
      exports: Record<string, { types: string; default: string }>;
    };

    // Modern resolvers (Next.js, Vite, TypeScript bundler/node16).
    expect(pkg.exports["./interceptors"]).toEqual({
      types: ENTRY_TYPES,
      default: ENTRY_JS,
    });

    // node10-era resolvers (Metro without package exports, expo tsconfig)
    // never read `exports`; they resolve the subpath as a directory and read
    // the fields in interceptors/package.json. Same build output, reached the
    // old way — delete the shim and mobile breaks while web keeps working.
    const shim = JSON.parse(
      readFileSync(join(pkgDir, "interceptors", "package.json"), "utf8"),
    ) as Record<string, string>;
    expect(shim["types"]).toBe(`..${ENTRY_TYPES.slice(1)}`);
    for (const field of ["main", "react-native"]) {
      expect(shim[field]).toBe(`..${ENTRY_JS.slice(1)}`);
    }

    // Both routes have to survive `npm pack`.
    expect(pkg.files).toContain("dist");
    expect(pkg.files).toContain("interceptors");
  });
});
