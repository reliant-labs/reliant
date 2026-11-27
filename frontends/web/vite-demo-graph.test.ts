import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

import { demoForbiddenRule, twinOf } from "./vite-plugin-demo-bundle";

/**
 * The forge demo bundle's guard, before the build: walks the demo's import
 * graph from its entry the way the demo build resolves it (each module with a
 * `.demo` twin replaced by it, dynamic imports included) and fails on any
 * module vite-plugin-demo-bundle.ts's DEMO_FORBIDDEN rules forbid (sign-in
 * and account code), naming the import that brings it in.
 *
 * The demo build (`WEB_BUILD_TARGET=demo vite build`) is the authority and
 * runs only when the merge gate selects its lane; this catches the common
 * break (a chat or console module that starts importing the auth store,
 * Supabase, billing or a Settings account page) in the unit lane of any web
 * change. The import scan is textual, so it can only miss an import, never
 * invent one.
 */

const root = __dirname;
const ENTRY = path.join(root, "src/demoBundle/main.tsx");
const EXTENSIONS = [".ts", ".tsx", ".js", ".jsx", "/index.ts", "/index.tsx"];
// Value imports and re-exports, side-effect imports, and dynamic import(): never `import type`/`export type`.
const IMPORT = /(?:^|\n)\s*(?:import|export)\s+(?!type\b)(?:[^'"`;]*?\sfrom\s*)?["']([^"']+)["']|import\(\s*["']([^"']+)["']\s*\)/g;

function resolve(spec: string, importer: string): string | null {
  let base: string;
  if (spec.startsWith("@/")) base = path.join(root, "src", spec.slice(2));
  else if (spec.startsWith(".")) base = path.resolve(path.dirname(importer), spec);
  else return null;
  const candidates = fs.existsSync(base) && fs.statSync(base).isFile() ? [base] : EXTENSIONS.map((ext) => base + ext);
  const file = candidates.find((candidate) => fs.existsSync(candidate) && fs.statSync(candidate).isFile());
  if (!file || !/\.[cm]?[jt]sx?$/.test(file)) return null;
  return twinOf(file) ?? file;
}

/** Every forbidden module the demo reaches, with the import that reaches it. */
function forbiddenEdges(): string[] {
  const seen = new Set([ENTRY]);
  const queue = [ENTRY];
  const edges: string[] = [];
  while (queue.length > 0) {
    const file = queue.shift()!;
    for (const match of fs.readFileSync(file, "utf8").matchAll(IMPORT)) {
      const target = resolve(match[1] ?? match[2], file);
      if (!target || seen.has(target)) continue;
      seen.add(target);
      const rule = demoForbiddenRule(target, root);
      if (rule) {
        edges.push(`${path.relative(root, file)} → ${path.relative(root, target)} (${rule.why})`);
        continue;
      }
      queue.push(target);
    }
  }
  return edges;
}

describe("the forge demo's import graph", () => {
  it("reaches no sign-in or account module: read the session port, or add a demo twin", () => {
    expect(forbiddenEdges()).toEqual([]);
  });

  it("is walked through twins: the interceptor chain and the session are behind them", () => {
    // Guards the walker itself: these twins are what keep the chat stream's
    // context helpers (api/transport.ts) and the session out of the demo.
    expect(twinOf(path.join(root, "src/api/transport.ts"))).toMatch(/transport\.demo\.ts$/);
    expect(twinOf(path.join(root, "src/lib/auth/sessionOwner.ts"))).toMatch(/sessionOwner\.demo\.ts$/);
  });
});
