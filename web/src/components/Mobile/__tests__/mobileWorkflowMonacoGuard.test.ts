/**
 * Confirms Monaco stays unreachable from the mobile workflow viewer.
 *
 * `main.tsx` skips the Monaco preload on `/m/*` (see `lib/monacoPreload.ts`)
 * specifically because no mobile screen was expected to need an editor. The
 * desktop `WorkflowHub`/`WorkflowParamsPanel` pull Monaco in transitively via
 * `ProtoFieldRenderer` → `CELInput` → `MonacoCELEditor` for their *editable*
 * param fields — exactly the path these mobile components must not join.
 * `WorkflowViewer`/`PresetPicker` do NOT pull it in (confirmed empirically:
 * an esbuild bundle of each contains zero references to `monaco-editor`),
 * which is why this file reuses parts of both.
 *
 * A render/typecheck test can't catch a bad import — the module loads fine,
 * it just makes the bundle heavy. This walks the real `import ... from`
 * graph from each mobile workflow entry point and fails if it ever reaches
 * a file that imports `monaco-editor` or `@monaco-editor/*` as a VALUE
 * (type-only imports are erased by the bundler and don't count).
 */

import { describe, expect, it } from "vitest";
import { readFileSync, existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const MOBILE_DIR = join(dirname(fileURLToPath(import.meta.url)), "..");
const SRC_DIR = resolve(MOBILE_DIR, "..", "..");

const ENTRY_POINTS = [
  "MobileWorkflowStepList.tsx",
  "MobileWorkflowNodeSheet.tsx",
  "MobileWorkflowScreen.tsx",
  "MobileWorkflowCatalog.tsx",
  "MobileWorkflowParamsView.tsx",
  "MobileWorkflowExecutionEntry.tsx",
];

// Matches `import ... from '...'` / `import '...'` but not `import type ...`.
const VALUE_IMPORT_RE = /^import\s+(?!type\s)(?:[^;]*?from\s+)?['"]([^'"]+)['"]/gm;

function resolveImport(fromFile: string, spec: string): string | null {
  if (spec.startsWith("monaco-editor") || spec.startsWith("@monaco-editor")) {
    return "__MONACO__";
  }
  if (!spec.startsWith(".")) return null; // external package, not our source
  const base = resolve(dirname(fromFile), spec);
  const candidates = [
    base,
    `${base}.tsx`,
    `${base}.ts`,
    join(base, "index.tsx"),
    join(base, "index.ts"),
  ];
  return candidates.find((c) => existsSync(c) && c !== base) ?? null;
}

function scanForMonaco(entryFile: string): string[] | null {
  const visited = new Set<string>();
  const chain: string[] = [];

  function walk(file: string): string[] | null {
    if (visited.has(file)) return null;
    visited.add(file);
    const text = readFileSync(file, "utf8");
    for (const match of text.matchAll(VALUE_IMPORT_RE)) {
      const resolved = resolveImport(file, match[1]);
      if (resolved === "__MONACO__") return [...chain, file, match[1]];
      if (resolved) {
        chain.push(file);
        const hit = walk(resolved);
        chain.pop();
        if (hit) return hit;
      }
    }
    return null;
  }

  return walk(entryFile);
}

describe("mobile workflow viewer never reaches Monaco", () => {
  for (const entry of ENTRY_POINTS) {
    it(`${entry} has no transitive monaco-editor import`, () => {
      const path = join(MOBILE_DIR, entry);
      const hit = scanForMonaco(path);
      expect(hit, `Monaco import chain: ${hit?.join(" -> ")}`).toBeNull();
    });
  }

  it("checks a meaningful number of entry points", () => {
    expect(ENTRY_POINTS.length).toBeGreaterThan(3);
  });

  it("sanity-checks the scanner catches a real Monaco import", () => {
    // WorkflowHub is the known-dirty case (imports ProtoFieldRenderer
    // directly, which reaches CELInput -> MonacoCELEditor) — if the scanner
    // ever reports this clean, the guard above is not actually guarding.
    const hubPath = join(SRC_DIR, "components", "workflow", "WorkflowHub.tsx");
    const hit = scanForMonaco(hubPath);
    expect(hit).not.toBeNull();
  });
});
