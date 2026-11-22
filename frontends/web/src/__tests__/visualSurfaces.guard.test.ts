// @vitest-environment node
// Copyright (c) 2026 Reliant Labs

/**
 * THE VISUAL BASELINE'S SURFACE LIST IS WELL-FORMED.
 *
 * e2e/visual is outside every tsconfig project (`tsc -b` never checks it) and
 * outside vitest, and Playwright strips types without checking them. So a bad
 * merge of `SURFACES` in surfaces.visual.ts fails nowhere: two surfaces fused
 * into one object keep only the last `name`, `path` and `ready`, and the first
 * surface silently stops being captured (U6 15/16 lost `shell-agents-no-sandbox`
 * that way). This reads the e2e/visual sources with the TypeScript parser and
 * fails on:
 *
 *   1. an object literal that spells the same key twice, anywhere in them;
 *   2. two surfaces in surfaces.visual.ts's `SURFACES` with the same `name` on
 *      the same viewport (one name may serve desktop and mobile as two
 *      entries: the capture is per project, `<project>/<name>.png`).
 */

import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import ts from "typescript";
import { describe, expect, it } from "vitest";

const VISUAL = join(__dirname, "..", "..", "e2e", "visual");

function sourcesIn(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) return entry.name === "__screenshots__" ? [] : sourcesIn(path);
    return entry.name.endsWith(".ts") ? [path] : [];
  });
}

function parse(path: string): ts.SourceFile {
  return ts.createSourceFile(path, readFileSync(path, "utf8"), ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
}

/** A property's key as written, when it is a plain one (not computed, not a spread). */
function keyOf(property: ts.ObjectLiteralElementLike): string | null {
  if (ts.isSpreadAssignment(property)) return null;
  const name = property.name;
  if (ts.isIdentifier(name) || ts.isStringLiteral(name) || ts.isNumericLiteral(name) || ts.isNoSubstitutionTemplateLiteral(name)) return name.text;
  return null;
}

function where(source: ts.SourceFile, node: ts.Node): string {
  const { line } = source.getLineAndCharacterOfPosition(node.getStart(source));
  return `${source.fileName.slice(VISUAL.length + 1)}:${line + 1}`;
}

/** Every object literal in `source` that spells a key twice, as "file:line key". */
export function duplicateKeys(source: ts.SourceFile): string[] {
  const found: string[] = [];
  const visit = (node: ts.Node) => {
    if (ts.isObjectLiteralExpression(node)) {
      const seen = new Set<string>();
      for (const property of node.properties) {
        const key = keyOf(property);
        if (key === null) continue;
        if (seen.has(key)) found.push(`${where(source, property)} ${key}`);
        seen.add(key);
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return found;
}

/** Each capture `SURFACES` declares, as "<name> (<viewport>)", the title of its test. */
function surfaceCaptures(source: ts.SourceFile): string[] {
  let list: ts.ArrayLiteralExpression | undefined;
  const visit = (node: ts.Node) => {
    if (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name) && node.name.text === "SURFACES" && node.initializer && ts.isArrayLiteralExpression(node.initializer)) {
      list = node.initializer;
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  if (!list) throw new Error("surfaces.visual.ts has no `const SURFACES = [...]`");
  return list.elements.flatMap((element) => {
    if (!ts.isObjectLiteralExpression(element)) throw new Error(`${where(source, element)}: a SURFACES entry is not an object literal`);
    const name = element.properties.find((property) => keyOf(property) === "name");
    if (!name || !ts.isPropertyAssignment(name) || !ts.isStringLiteralLike(name.initializer)) {
      throw new Error(`${where(source, element)}: a SURFACES entry has no literal name`);
    }
    const on = element.properties.find((property) => keyOf(property) === "on");
    if (!on || !ts.isPropertyAssignment(on) || !ts.isArrayLiteralExpression(on.initializer)) {
      throw new Error(`${where(source, element)}: a SURFACES entry has no literal \`on\` list`);
    }
    const surface = name.initializer.text;
    return on.initializer.elements.map((viewport) => `${surface} (${ts.isStringLiteralLike(viewport) ? viewport.text : viewport.getText(source)})`);
  });
}

describe("the visual baseline's sources", () => {
  it("spell no object key twice", () => {
    const files = sourcesIn(VISUAL);
    expect(files.length).toBeGreaterThan(0);
    expect(files.flatMap((file) => duplicateKeys(parse(file)))).toEqual([]);
  });

  it("declare each surface once per viewport", () => {
    const captures = surfaceCaptures(parse(join(VISUAL, "surfaces.visual.ts")));
    expect(captures.length).toBeGreaterThan(0);
    expect(captures.filter((capture, i) => captures.indexOf(capture) !== i)).toEqual([]);
  });
});
