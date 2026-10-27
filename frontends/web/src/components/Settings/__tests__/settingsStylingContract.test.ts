/**
 * The web-styling contract, held for Settings' own sections (cloud/** is the
 * billing and machines surfaces, held by their own tests):
 *
 *   - colours are semantic tokens (success / warning / destructive / info),
 *     never a palette colour that ignores the ten color schemes;
 *   - `bg-muted` is interaction state only (hover, a loading skeleton), never
 *     a panel: it lifts in dark mode and recesses in light;
 *   - panels cast no shadow, so no `elevation-*` surface and no 3%-white inset
 *     shadow faking an edge on a card inside a card.
 *
 * A source scan rather than a render, because the failure it prevents is a
 * class string, and a render would only see the themes the test happens to run.
 */

import { readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");

// mcpAvatar's gradients are per-server identity colours, chosen as data.
const EXEMPT = new Set(["mcpAvatar.tsx"]);

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) {
      return name === "__tests__" || name === "cloud" ? [] : sources(path);
    }
    return name.endsWith(".tsx") && !EXEMPT.has(name) ? [path] : [];
  });
}

function offenders(pattern: RegExp, allow?: RegExp): string[] {
  return sources(ROOT).flatMap((path) =>
    readFileSync(path, "utf8")
      .split("\n")
      .map((line, i) => ({ line, n: i + 1 }))
      .filter(({ line }) => pattern.test(line) && !(allow && allow.test(line)) && !line.trim().startsWith("//"))
      .map(({ line, n }) => `${relative(ROOT, path)}:${n}: ${line.trim()}`),
  );
}

describe("Settings styling contract", () => {
  it("uses semantic colour tokens, not palette colours", () => {
    expect(offenders(/\b(?:bg|text|border|from|to|via)-(?:red|green|emerald|amber|yellow|sky|zinc|blue|orange)-\d{2,3}\b/)).toEqual([]);
  });

  it("never builds a panel out of bg-muted", () => {
    // hover:/active:/disabled: washes and animate-pulse skeletons are state, not structure.
    expect(
      offenders(/(?<![:\w-])bg-muted(?:\/\d+)?\b/, /animate-pulse|appearance-none|\[[^\]]*bg-muted|bg-muted text-muted-foreground"|bg-muted\/\d+ rounded-full h-4/),
    ).toEqual([]);
  });

  it("gives panels no shadow", () => {
    expect(offenders(/shadow-\[inset_0_1px_0_0_rgba\(255,255,255,0\.03\)\]|\belevation-[1-3]\b(?!.*w-\d)/)).toEqual([]);
  });
});
