// Copyright (c) 2025 Reliant Labs

/**
 * NO REAL CUSTOMER IN EXAMPLE COPY.
 *
 * The Domains dialog once shipped a tenant's real domain as its placeholder
 * and help text. Every org that opened it saw another customer's hostname
 * offered as the example — a leak, and an odd suggestion to type someone
 * else's domain. It got there the ordinary way: a realistic name in a design
 * comment, copied into a placeholder because it was right there.
 *
 * Two rules, because a deny-list alone only ever catches the last incident:
 *
 *   1. GENERIC — in the Domains UI, every hostname-shaped token in a shipped
 *      string (literal, template text, JSX text or attribute) must be an
 *      RFC 2606 reserved name (example.com / .net / .org, or the .example /
 *      .test / .invalid / .localhost TLDs) or one of Reliant's own domains.
 *      Comments are not shipped and are not checked by this rule.
 *   2. DENY-LIST — known customer names appear nowhere in the Forge surface's
 *      source, comments included, since a comment is where the last one
 *      started. Test files are exempt: realistic fixtures are fine there,
 *      because a test never renders to a user.
 */

import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import ts from "typescript";
import { describe, expect, it } from "vitest";

const SRC_DIR = join(__dirname, "..", "..", "..");
const DOMAINS_DIR = join(SRC_DIR, "components", "Forge", "Domains");

/** The Forge surface: its components, its service layer and its hooks. */
const FORGE_SURFACE = [
  join(SRC_DIR, "components", "Forge"),
  join(SRC_DIR, "services", "forge"),
  join(SRC_DIR, "hooks"),
];

/** Customers whose names have leaked into copy before. Add to it; never remove. */
const CUSTOMER_NAMES = ["hounders"];

/** Reliant's own domains — a docs link is not example copy. */
const OWN_DOMAINS = ["reliantlabs.io"];

const RESERVED_SECOND_LEVEL = ["example.com", "example.net", "example.org"];
const RESERVED_TLDS = ["example", "test", "invalid", "localhost"];

/** A dotted name ending in an alphabetic label: app.example.com, hounders.club. */
const HOSTNAME_TOKEN = /(?<![\w.-])(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}(?![\w-])/gi;

function sourceFiles(dir: string, accept: (name: string) => boolean = () => true): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      if (entry === "__tests__") continue;
      out.push(...sourceFiles(full, accept));
    } else if (/\.(ts|tsx)$/.test(entry) && !/\.test\.(ts|tsx)$/.test(entry) && accept(entry)) {
      out.push(full);
    }
  }
  return out;
}

export function isExampleHostname(host: string): boolean {
  const name = host.toLowerCase();
  const matchesZone = (zone: string) => name === zone || name.endsWith(`.${zone}`);
  if (RESERVED_SECOND_LEVEL.some(matchesZone)) return true;
  if (OWN_DOMAINS.some(matchesZone)) return true;
  return RESERVED_TLDS.includes(name.slice(name.lastIndexOf(".") + 1));
}

/**
 * Every piece of text a component can put on screen: string literals,
 * template text, and JSX text. Module specifiers are skipped — an import
 * path is not copy.
 */
export function shippedStrings(fileName: string, source: string): string[] {
  const file = ts.createSourceFile(fileName, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const out: string[] = [];
  const visit = (node: ts.Node) => {
    if (ts.isImportDeclaration(node) || ts.isExportDeclaration(node)) return;
    if (
      ts.isStringLiteral(node) ||
      ts.isNoSubstitutionTemplateLiteral(node) ||
      ts.isTemplateHead(node) ||
      ts.isTemplateMiddle(node) ||
      ts.isTemplateTail(node)
    ) {
      out.push(node.text);
    } else if (ts.isJsxText(node)) {
      out.push(node.getText());
    }
    ts.forEachChild(node, visit);
  };
  visit(file);
  return out;
}

export function nonExampleHostnames(fileName: string, source: string): string[] {
  return shippedStrings(fileName, source).flatMap((text) =>
    (text.match(HOSTNAME_TOKEN) ?? []).filter((host) => !isExampleHostname(host))
  );
}

describe("example copy in the Domains UI", () => {
  it("uses only RFC 2606 example hostnames in shipped strings", () => {
    const hits = sourceFiles(DOMAINS_DIR).flatMap((file) =>
      nonExampleHostnames(file, readFileSync(file, "utf8")).map(
        (host) => `${relative(SRC_DIR, file)}: ${host}`
      )
    );
    expect(hits).toEqual([]);
  });

  it("the rule catches a real domain in a placeholder and spares the reserved ones", () => {
    const leaked = `<input placeholder="hounders.club" /><p>An apex (shop.co.uk) and www</p>`;
    expect(nonExampleHostnames("x.tsx", leaked)).toEqual(["hounders.club", "shop.co.uk"]);

    const clean = [
      `<input placeholder="app.example.com" />`,
      `const a = "www.example.org";`,
      "const b = `redirects to ${target} at example.net`;",
      `const c = "https://docs.reliantlabs.io/features/custom-domains";`,
      `const d = "api.internal.test";`,
      `import x from "./not.a.host";`,
      `// a comment naming hounders.club is not shipped`,
    ].join("\n");
    expect(nonExampleHostnames("x.tsx", clean)).toEqual([]);
  });
});

describe("customer names in the Forge surface", () => {
  it("names no known customer outside test files", () => {
    const files = FORGE_SURFACE.flatMap((dir) =>
      // hooks/ is shared with the rest of the app; only the forge hooks are this surface.
      dir.endsWith("hooks") ? sourceFiles(dir, (name) => name.startsWith("forge")) : sourceFiles(dir)
    );
    expect(files.length).toBeGreaterThan(10);

    const hits = files.flatMap((file) =>
      readFileSync(file, "utf8")
        .split("\n")
        .flatMap((line, index) =>
          CUSTOMER_NAMES.some((name) => line.toLowerCase().includes(name))
            ? [`${relative(SRC_DIR, file)}:${index + 1}: ${line.trim()}`]
            : []
        )
    );
    expect(hits).toEqual([]);
  });
});
