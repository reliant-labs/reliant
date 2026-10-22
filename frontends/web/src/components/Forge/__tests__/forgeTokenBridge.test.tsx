// Copyright (c) 2025 Reliant Labs

/**
 * The forge → reliant token bridge (the @theme block in src/index.css).
 *
 * Forge's component library is installed as SOURCE via
 * `forge component install`, and is written against forge's own semantic
 * token vocabulary (bg-surface, text-ink, bg-danger-surface, …). Reliant's
 * vocabulary is different (bg-card, text-foreground, bg-destructive, …).
 * The bridge maps one onto the other so every installed component inherits
 * reliant's ten color schemes plus light/dark.
 *
 * WHAT THESE TESTS CAN AND CANNOT DO. jsdom applies no stylesheet, so a
 * computed-color assertion here would pass against a completely broken
 * bridge — it would compare "" to "". These tests therefore pin the two
 * things that ARE checkable without a renderer, and which are what actually
 * rot over time:
 *
 *   1. Every token the installed components reference is declared in the
 *      bridge. A forge upgrade that introduces a new token name is the
 *      realistic way this breaks, and it fails HERE rather than as an
 *      invisible transparent fill in the UI.
 *   2. The bridge does not map a structural token onto reliant's --muted,
 *      which inverts direction between light and dark (see the long comment
 *      in index.css). That is the single most likely wrong "fix".
 *
 * Actual color resolution was verified in a real browser across
 * professional-blue / forest / pure-black / vibrant-pink in both modes:
 * all 32 utilities resolved to opaque colors, and `.forge-ui` was confirmed
 * to flip `accent` from reliant's muted tint to its primary action color.
 */

import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

const WEB_ROOT = join(__dirname, "..", "..", "..", "..");
const INDEX_CSS = join(WEB_ROOT, "src", "index.css");
const FORGE_UI_DIR = join(WEB_ROOT, "src", "components", "forge-ui");

/** Utility prefixes Tailwind generates from a `--color-*` theme entry. */
const COLOR_PREFIXES = [
  "bg",
  "text",
  "border",
  "ring",
  "divide",
  "fill",
  "stroke",
  "shadow",
  "outline",
];

/**
 * Token names that belong to Tailwind's own scale rather than to a color —
 * `text-sm` is a font size, `border-b` an edge. Screening these out keeps
 * the extraction honest without hand-listing every forge token.
 */
const NON_COLOR_SUFFIXES = new Set([
  "xs", "sm", "base", "lg", "xl", "2xl", "3xl", "4xl", "5xl", "6xl",
  "left", "center", "right", "justify", "start", "end", "wrap", "nowrap",
  "b", "t", "l", "r", "x", "y", "0", "2", "4", "8",
  "collapse", "separate", "dashed", "dotted", "solid", "none", "inset",
  "clip", "transparent", "current", "inherit", "white", "black", "auto",
  "md", "full", "color",
]);

function readCss(): string {
  return readFileSync(INDEX_CSS, "utf8");
}

/** The `--color-<name>` entries the bridge declares. */
function declaredTokens(css: string): Set<string> {
  const names = new Set<string>();
  for (const m of css.matchAll(/--color-([a-z][a-z0-9-]*)\s*:/g)) {
    names.add(m[1]);
  }
  return names;
}

/**
 * Files authored by forge — i.e. the ones `forge component install` writes
 * and overwrites. Anything hand-written alongside them (the sandbox, the
 * probe) legitimately uses RELIANT's vocabulary, which is declared in
 * tailwind.config.js rather than in the bridge, so scanning it would report
 * `bg-card` as an undeclared forge token.
 */
const HAND_WRITTEN = new Set(["ForgeTokenSandbox.tsx", "__tokenprobe.tsx"]);

/** Color-token names referenced by the installed forge components. */
function referencedTokens(): Map<string, string[]> {
  const refs = new Map<string, string[]>();
  const files = readdirSync(FORGE_UI_DIR).filter(
    (f) => f.endsWith(".tsx") && !HAND_WRITTEN.has(f),
  );
  const pattern = new RegExp(
    `\\b(?:${COLOR_PREFIXES.join("|")})-([a-z][a-z0-9-]*)`,
    "g",
  );
  for (const file of files) {
    const src = readFileSync(join(FORGE_UI_DIR, file), "utf8");
    for (const m of src.matchAll(pattern)) {
      const token = m[1];
      if (NON_COLOR_SUFFIXES.has(token)) continue;
      // Tailwind palette colors (gray-500) are not semantic tokens.
      if (/-\d+$/.test(token)) continue;
      const seen = refs.get(token) ?? [];
      if (!seen.includes(file)) seen.push(file);
      refs.set(token, seen);
    }
  }
  return refs;
}

describe("forge token bridge", () => {
  it("declares every color token the installed forge components reference", () => {
    const declared = declaredTokens(readCss());
    const referenced = referencedTokens();

    // Sanity: if extraction found nothing, the assertion below is vacuous.
    expect(referenced.size).toBeGreaterThan(5);

    const missing: string[] = [];
    for (const [token, files] of referenced) {
      if (!declared.has(token)) missing.push(`${token} (used in ${files.join(", ")})`);
    }

    expect(
      missing,
      "forge components reference color tokens the @theme bridge in " +
        "src/index.css does not declare. An undeclared token renders as a " +
        "transparent fill or inherited text. Add it to the bridge.",
    ).toEqual([]);
  });

  it("maps forge's structural surface tokens away from reliant's --muted", () => {
    const css = readCss();

    // --muted inverts direction between light and dark across the ten
    // schemes (lighter than --card in dark, darker than --background in
    // light), so a structural token pointed at it recesses in one mode and
    // lifts in the other. See the comment block in index.css.
    for (const token of ["surface", "surface-muted", "surface-sunken"]) {
      const decl = new RegExp(`--color-${token}\\s*:\\s*([^;]+);`).exec(css);
      expect(decl, `--color-${token} should be declared`).not.toBeNull();
      expect(
        decl![1],
        `--color-${token} must not resolve through --muted (it inverts ` +
          `between light and dark). Use --card or --background.`,
      ).not.toMatch(/var\(--muted\)/);
    }
  });

  it("scopes forge's accent to .forge-ui so reliant's bg-accent is untouched", () => {
    const css = readCss();

    // forge `accent` = primary action color; reliant `--accent` = a muted
    // hover tint used at ~159 existing call sites. @theme overrides the JS
    // config, so an unscoped mapping would repaint all of them.
    const scope = /\.forge-ui\s*\{([^}]+)\}/.exec(css);
    expect(scope, ".forge-ui scope block should exist").not.toBeNull();
    expect(
      scope![1],
      ".forge-ui must re-declare --color-accent directly: a var() inside a " +
        "custom property is substituted where it is DECLARED (:root), so " +
        "overriding only the input variable has no effect.",
    ).toMatch(/--color-accent\s*:/);

    // Outside the scope, accent must still resolve to reliant's --accent.
    const root = /:root\s*\{([^}]*--forge-accent-src[^}]*)\}/.exec(css);
    expect(root, ":root default for --forge-accent-src should exist").not.toBeNull();
    expect(root![1]).toMatch(/--forge-accent-src\s*:\s*var\(--accent\)/);
  });
});
