// Copyright (c) 2025 Reliant Labs

/**
 * A static map of every app path spelled as a string in `src/`, for the guard
 * in `__tests__/navPaths.guard.test.ts`.
 *
 * Every destination's URL is built by `lib/paths` (and the console's own
 * `components/Forge/Console/paths.ts`), so that the org segment of the
 * unified-shell design (U3: `/o/<org>/…`) is added in one place. A path
 * literal anywhere else is a link U3 would miss. Two kinds of site are found,
 * by syntax alone (no type checker, so the guard stays in the fast tier):
 *
 *   - `navigation`: any string starting with `/` that reaches a navigation:
 *     a `to` or `href` (prop or option), `navigate(…)`, `redirect(…)`,
 *     `hardNavigate(…)`, `location.assign/replace(…)`, `history.pushState/
 *     replaceState(…)`, `window.open(…)`, an assignment to `location` or
 *     `….href`, or a `returnTo` / `redirect` search value. Conditionals,
 *     `??`, `||`, concatenation, template literals, `encodeURIComponent(…)`
 *     and `as` casts are seen through.
 *   - `route`: a string naming one of the app's routes — its first segment is
 *     the first segment of a `createRoute({ path })` — anywhere else: a
 *     constant, a return value, a nav item, a map. Except where it MATCHES a
 *     path rather than builds one: a comparison, a `case`, `startsWith` and
 *     friends, an `is…`/`…match…`/`…Prefix` predicate's argument, a route's
 *     own `path` (or a `…Route(path)` factory's),
 *     `from` or `id`, and types.
 */

import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import ts from "typescript";

export interface PathSite {
  kind: "navigation" | "route";
  /** `path:line`, path relative to src/. */
  where: string;
  /** The file, relative to src/. */
  file: string;
  /** The literal's text as written (template literals with their `${…}`). */
  text: string;
}

/** Property, attribute and search-key names whose value is a destination. */
const NAV_KEYS = new Set(["to", "href", "returnTo", "redirect"]);

/** Call names whose arguments are destinations. */
const NAV_CALLS = new Set(["navigate", "redirect", "hardNavigate", "pushState", "replaceState"]);

/** String methods (and predicates) whose argument is matched against, not navigated to. */
const MATCH_METHODS = new Set(["startsWith", "endsWith", "includes", "indexOf", "lastIndexOf", "match", "test", "split", "replace", "replaceAll", "has", "get"]);

const SKIPPED_DIRS = new Set(["gen", "__tests__", "__preview__", "__fixtures__", "demoData", "test", "mocks", "node_modules"]);

/** Properties whose value is a route's own declaration or a route id to read from. */
const DECLARATION_KEYS = new Set(["path", "from", "id"]);

export function listSources(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) {
      // Tests, dev-only previews and fixture data (a demo app's own page paths) are not this app's links.
      if (SKIPPED_DIRS.has(name)) continue;
      listSources(p, out);
    } else if (/\.tsx?$/.test(name) && !/\.(test|spec)\.tsx?$/.test(name) && !name.endsWith("_gen.ts") && !name.endsWith(".d.ts")) {
      out.push(p);
    }
  }
  return out;
}

function parse(path: string): ts.SourceFile {
  const text = readFileSync(path, "utf8");
  return ts.createSourceFile(path, text, ts.ScriptTarget.Latest, true, path.endsWith("x") ? ts.ScriptKind.TSX : ts.ScriptKind.TS);
}

function nameOf(node: ts.PropertyName | ts.JsxAttributeName | ts.Expression): string | null {
  if (ts.isIdentifier(node) || ts.isStringLiteral(node) || ts.isPrivateIdentifier(node)) return node.text;
  if (ts.isPropertyAccessExpression(node)) return node.name.text;
  return null;
}

/** The literal text of a string-ish node: a string, a no-substitution template, or a template's head. */
function literalText(node: ts.Node): string | null {
  if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) return node.text;
  if (ts.isTemplateExpression(node)) return node.head.text;
  return null;
}

function isInType(node: ts.Node): boolean {
  for (let n: ts.Node | undefined = node.parent; n; n = n.parent) {
    if (ts.isTypeNode(n)) return true;
    if (ts.isStatement(n) || ts.isSourceFile(n)) return false;
  }
  return false;
}

/**
 * Walk up from a literal through the expressions that pass its value on
 * unchanged (or as part of a bigger string) and return the first that does
 * not, with the child it was reached from.
 */
function consumer(node: ts.Node): { parent: ts.Node; child: ts.Node } {
  let child = node;
  let parent = node.parent;
  for (;;) {
    if (
      ts.isParenthesizedExpression(parent) ||
      ts.isAsExpression(parent) ||
      ts.isSatisfiesExpression(parent) ||
      ts.isTypeAssertionExpression(parent) ||
      ts.isNonNullExpression(parent) ||
      ts.isTemplateSpan(parent) ||
      ts.isTemplateExpression(parent) ||
      ts.isJsxExpression(parent) ||
      (ts.isConditionalExpression(parent) && child !== parent.condition) ||
      (ts.isBinaryExpression(parent) &&
        [ts.SyntaxKind.QuestionQuestionToken, ts.SyntaxKind.BarBarToken, ts.SyntaxKind.AmpersandAmpersandToken, ts.SyntaxKind.PlusToken].includes(parent.operatorToken.kind)) ||
      (ts.isCallExpression(parent) && ts.isIdentifier(parent.expression) && (parent.expression.text === "encodeURIComponent" || parent.expression.text === "String"))
    ) {
      child = parent;
      parent = parent.parent;
      continue;
    }
    return { parent, child };
  }
}

function calleeName(call: ts.CallExpression): string | null {
  return nameOf(call.expression);
}

function isNavigation(parent: ts.Node, child: ts.Node): boolean {
  if (ts.isPropertyAssignment(parent) && parent.initializer === child) return NAV_KEYS.has(nameOf(parent.name) ?? "");
  if (ts.isJsxAttribute(parent)) return NAV_KEYS.has(nameOf(parent.name) ?? "");
  if (ts.isCallExpression(parent) && parent.arguments.includes(child as ts.Expression)) {
    const name = calleeName(parent);
    if (name && NAV_CALLS.has(name)) return true;
    const target = ts.isPropertyAccessExpression(parent.expression) ? parent.expression.expression.getText() : "";
    if ((name === "assign" || name === "replace") && /(^|\.)location$/.test(target)) return true;
    if (name === "open" && /(^|\.)window$/.test(target)) return true;
    return false;
  }
  if (ts.isBinaryExpression(parent) && parent.operatorToken.kind === ts.SyntaxKind.EqualsToken && parent.right === child) {
    const left = parent.left.getText();
    return /(^|\.)location$/.test(left) || /\.href$/.test(left);
  }
  return false;
}

function isMatching(parent: ts.Node, child: ts.Node): boolean {
  if (ts.isBinaryExpression(parent)) {
    return [ts.SyntaxKind.EqualsEqualsEqualsToken, ts.SyntaxKind.ExclamationEqualsEqualsToken, ts.SyntaxKind.EqualsEqualsToken, ts.SyntaxKind.ExclamationEqualsToken].includes(
      parent.operatorToken.kind,
    );
  }
  if (ts.isCaseClause(parent)) return true;
  if (ts.isCallExpression(parent) && parent.arguments.includes(child as ts.Expression)) {
    const name = calleeName(parent) ?? "";
    // …and a route factory's path (`legacyRoute("/runs")`) is a declaration.
    return MATCH_METHODS.has(name) || /^(is|has|under)[A-Z]/.test(name) || /[mM]atch|Prefix/.test(name) || /Route$/.test(name);
  }
  if (ts.isPropertyAssignment(parent) && parent.initializer === child) return DECLARATION_KEYS.has(nameOf(parent.name) ?? "");
  return false;
}

/** The first segments of every absolute route path declared with `createRoute({ path })`. */
export function routeRoots(files: string[]): Set<string> {
  const roots = new Set<string>();
  for (const file of files) {
    const sf = parse(file);
    const visit = (node: ts.Node) => {
      if (ts.isCallExpression(node) && calleeName(node) === "createRoute") {
        const opts = node.arguments[0];
        if (opts && ts.isObjectLiteralExpression(opts)) {
          for (const p of opts.properties) {
            if (ts.isPropertyAssignment(p) && nameOf(p.name) === "path") {
              const text = literalText(p.initializer);
              const root = text?.match(/^\/([^/$?#]+)/)?.[1];
              if (root) roots.add(root);
            }
          }
        }
      }
      ts.forEachChild(node, visit);
    };
    visit(sf);
  }
  return roots;
}

/** The path sites in one source file. `file` is its name relative to src/. */
export function scanSource(file: string, text: string, roots: ReadonlySet<string>): PathSite[] {
  const sf = ts.createSourceFile(file, text, ts.ScriptTarget.Latest, true, file.endsWith("x") ? ts.ScriptKind.TSX : ts.ScriptKind.TS);
  const sites: PathSite[] = [];
  const visit = (node: ts.Node) => {
    const literal = literalText(node);
    if (literal !== null && literal.startsWith("/") && !literal.startsWith("//") && !isInType(node)) {
      // A literal inside a bigger string (`/auth?redirect=${encodeURIComponent("/claim")}`)
      // is judged by where the whole string goes.
      const { parent, child } = consumer(node);
      const where = `${file}:${sf.getLineAndCharacterOfPosition(node.getStart()).line + 1}`;
      const written = node.getText().slice(1, -1);
      if (isNavigation(parent, child)) {
        sites.push({ kind: "navigation", where, file, text: written });
      } else if (!isMatching(parent, child)) {
        const root = literal.match(/^\/([^/$?#{]+)/)?.[1];
        if (root && roots.has(root)) sites.push({ kind: "route", where, file, text: written });
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
  return sites;
}

/** Every path site in `srcDir`, and the route roots they were judged against. */
export function findPathSites(srcDir: string): { sites: PathSite[]; roots: Set<string> } {
  const files = listSources(srcDir);
  const roots = routeRoots(files);
  const sites = files.flatMap((file) => scanSource(relative(srcDir, file), readFileSync(file, "utf8"), roots));
  return { sites, roots };
}
