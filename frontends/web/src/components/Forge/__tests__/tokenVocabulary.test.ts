// Copyright (c) 2025 Reliant Labs

/**
 * ONE COLOUR VOCABULARY IN components/Forge: forge-ui's.
 *
 * The console is drawn with forge's component library (forge-ui wraps the
 * shell), so its own components speak forge's tokens — `text-ink`,
 * `text-ink-muted`, `bg-surface`, `bg-surface-sunken`, `*-danger-*` — and not
 * reliant's (`text-foreground`, `bg-card`, `bg-background`, `*-destructive*`).
 * Both resolve to the same theme variables (the bridge in src/index.css), so
 * mixing them never showed; it only stopped the code saying which system owns
 * the surface, and it let `bg-muted` — the elevation trap that recesses in
 * light mode and lifts in dark — back in as structure.
 *
 * NOT IN THE LIST, deliberately: `primary`, `ring` and `accent`. A Forge
 * dialog portals out of the `.forge-ui` scope that remaps `accent` to the
 * primary action colour, so swapping `bg-primary` for `bg-accent` would paint
 * every portalled button in reliant's muted hover tint.
 */

import { readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");

/** Reliant tokens with a forge-ui equivalent, and the equivalent. */
const BANNED: { pattern: RegExp; use: string }[] = [
  { pattern: /(?<![\w-])(?:[a-z-]+:)*text-foreground\b/, use: "text-ink" },
  { pattern: /(?<![\w-])(?:[a-z-]+:)*text-muted-foreground\b/, use: "text-ink-muted (or text-ink-subtle)" },
  { pattern: /(?<![\w-])(?:[a-z-]+:)*bg-background\b/, use: "bg-surface-sunken" },
  { pattern: /(?<![\w-])(?:[a-z-]+:)*bg-card\b/, use: "bg-surface" },
  { pattern: /(?<![\w-])(?:[a-z-]+:)*bg-muted\b/, use: "bg-surface-sunken for structure, hover:bg-ink/5 for interaction" },
  { pattern: /(?<![\w-])(?:[a-z-]+:)*(?:text|bg|border|ring)-destructive\b/, use: "the danger tokens (text-danger-ink, bg-danger-surface, border-danger-border)" },
];

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return name === "__tests__" ? [] : sources(path);
    return /\.(ts|tsx)$/.test(name) && !/\.test\.tsx?$/.test(name) ? [path] : [];
  });
}

describe("components/Forge colour tokens", () => {
  it("uses forge-ui's vocabulary, not reliant's", () => {
    const offences: string[] = [];
    for (const file of sources(ROOT)) {
      readFileSync(file, "utf8")
        .split("\n")
        .forEach((line, index) => {
          // Comments may name the old tokens (to say why they are not used).
          if (/^\s*(\/\/|\*|\/\*|\{\/\*)/.test(line)) return;
          for (const { pattern, use } of BANNED) {
            const match = pattern.exec(line);
            if (match) offences.push(`${relative(ROOT, file)}:${index + 1}: ${match[0]} → ${use}`);
          }
        });
    }
    expect(offences).toEqual([]);
  });
});
