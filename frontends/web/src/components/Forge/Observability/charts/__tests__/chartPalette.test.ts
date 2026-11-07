// @vitest-environment node
// Copyright (c) 2025 Reliant Labs

/**
 * The categorical chart palette, read straight from the CSS.
 *
 *   - every --chart-N is >= 3:1 (WCAG 1.4.11) against --card in every scheme,
 *     light and dark: series 2 was --secondary, 1.1-1.3:1, and vanished;
 *   - none is green or red, which are the console's "healthy" and "failing";
 *   - brand blue leads and brand violet follows;
 *   - it is the one palette: the module lists exactly the tokens the CSS
 *     declares, and the old semantic copies are gone from the three charts.
 */

import { describe, expect, it } from "vitest";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { CHART_PALETTE, chartColor, chartToken, DANGER_PALETTE } from "../chartPalette";

const here = path.dirname(fileURLToPath(import.meta.url));
const SRC = path.resolve(here, "../../../../..");
const read = (relative: string) => fs.readFileSync(path.resolve(SRC, relative), "utf-8");

const PALETTE_CSS = read("themes/chart-palette.css");
const THEMES_CSS = read("themes/professional-themes.css");
const INDEX_CSS = read("index.css");

type Hsl = [number, number, number];
type Rgb = [number, number, number];

function block(css: string, selector: string, from = 0): string {
  const start = css.indexOf(selector, from);
  expect(start, `missing ${selector}`).toBeGreaterThanOrEqual(0);
  const open = css.indexOf("{", start);
  let depth = 0;
  for (let i = open; i < css.length; i += 1) {
    if (css[i] === "{") depth += 1;
    if (css[i] === "}") depth -= 1;
    if (depth === 0) return css.slice(open + 1, i);
  }
  throw new Error(`unclosed ${selector}`);
}

function hsl(body: string, name: string): Hsl {
  const match = body.match(new RegExp(`--${name}:\\s*([^;]+);`));
  expect(match, `--${name} not declared`).not.toBeNull();
  const value = match![1]!.trim();
  const ref = value.match(/^var\(--([\w-]+)\)$/);
  if (ref) return hsl(body, ref[1]!);
  const [h, s, l] = value.split(/\s+/).map((part) => parseFloat(part));
  return [h!, s!, l!];
}

function toRgb([h, s, l]: Hsl): Rgb {
  const sat = s / 100;
  const light = l / 100;
  const k = (n: number) => (n + h / 30) % 12;
  const a = sat * Math.min(light, 1 - light);
  const f = (n: number) => light - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1)));
  return [f(0) * 255, f(8) * 255, f(4) * 255];
}

function luminance(rgb: Rgb): number {
  const [r, g, b] = rgb.map((v) => {
    const c = v / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r! + 0.7152 * g! + 0.0722 * b!;
}

function contrast(a: Rgb, b: Rgb): number {
  const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p);
  return (x! + 0.05) / (y! + 0.05);
}

const hex = (rgb: Rgb) => `#${rgb.map((v) => Math.round(v).toString(16).padStart(2, "0")).join("")}`;

const lightPalette = block(PALETTE_CSS, ":root");
const darkPalette = block(PALETTE_CSS, ".dark");

// index.css's base layer: the first `:root {` / `.dark {` that carry the semantic tokens.
const semanticsAt = INDEX_CSS.indexOf("--success:");
const lightBase = block(INDEX_CSS, ":root", INDEX_CSS.lastIndexOf(":root", semanticsAt));
const darkBase = block(INDEX_CSS, ".dark", semanticsAt);

/** Every card a chart is drawn on: the base one and each scheme's. */
function cards(mode: "light" | "dark"): Array<[string, Rgb]> {
  const out: Array<[string, Rgb]> = [["base card", toRgb(hsl(mode === "light" ? lightBase : darkBase, "card"))]];
  const prefix = mode === "light" ? ':root[data-color-scheme="' : '.dark[data-color-scheme="';
  for (let at = THEMES_CSS.indexOf(prefix); at >= 0; at = THEMES_CSS.indexOf(prefix, at + 1)) {
    const scheme = THEMES_CSS.slice(at + prefix.length, THEMES_CSS.indexOf('"', at + prefix.length));
    const body = block(THEMES_CSS, prefix + scheme, at);
    if (/--card:/.test(body)) out.push([`${scheme} card`, toRgb(hsl(body, "card"))]);
  }
  return out;
}

const names = CHART_PALETTE.map((token) => token.slice(2));

describe("the chart palette", () => {
  it("declares exactly the tokens the module deals, in both modes", () => {
    for (const body of [lightPalette, darkPalette]) {
      const declared = [...body.matchAll(/--(chart-\d+):/g)].map((m) => m[1]);
      expect(declared).toEqual(names);
    }
    expect(CHART_PALETTE).toHaveLength(6);
  });

  it("is declared once: index.css and the scheme themes do not redeclare it", () => {
    expect(INDEX_CSS).not.toMatch(/--chart-\d+:/);
    expect(THEMES_CSS).not.toMatch(/--chart-\d+:/);
    expect(INDEX_CSS).toContain('@import "./themes/chart-palette.css";');
  });

  it("leads with brand blue (#0d8cff), then brand violet (#6e39ff)", () => {
    expect(hex(toRgb(hsl(lightPalette, "chart-1")))).toBe("#0d8cff");
    expect(hex(toRgb(hsl(lightPalette, "chart-2")))).toBe("#6e39ff");
    expect(hex(toRgb(hsl(darkPalette, "chart-1")))).toBe("#0d8cff");
  });

  describe.each(["light", "dark"] as const)("in %s", (mode) => {
    const body = mode === "light" ? lightPalette : darkPalette;

    it.each(names)("%s is at least 3:1 against every scheme's card", (name) => {
      const color = toRgb(hsl(body, name));
      for (const [card, surface] of cards(mode)) {
        expect(contrast(color, surface), `${name} on ${card}`).toBeGreaterThanOrEqual(3);
      }
    });

    it.each(names)("%s is neither green nor red", (name) => {
      const [h, s] = hsl(body, name);
      // A desaturated neutral (slate) carries no hue to misread.
      if (s < 25) return;
      expect(h >= 75 && h <= 165, `${name} hue ${h} reads as green`).toBe(false);
      expect(h <= 15 || h >= 345, `${name} hue ${h} reads as red`).toBe(false);
    });
  });
});

describe("chartToken / chartColor", () => {
  it("deals the palette in order and cycles past its end", () => {
    expect(CHART_PALETTE.map((_, i) => chartToken(i))).toEqual([...CHART_PALETTE]);
    expect(chartToken(CHART_PALETTE.length)).toBe("--chart-1");
    expect(chartToken(-1)).toBe("--chart-6");
    expect(chartColor(1)).toBe("hsl(var(--chart-2))");
    expect(chartColor(0, 0.5)).toBe("hsl(var(--chart-1) / 0.5)");
  });

  it("keeps red out of the categorical palette: it is the error series' alone", () => {
    for (const token of DANGER_PALETTE) expect(CHART_PALETTE).not.toContain(token as never);
  });
});

describe("one palette, not three", () => {
  it.each([
    "components/Forge/Observability/charts/UPlotChart.tsx",
    "components/Forge/Observability/Metrics/ExplorerChart.tsx",
    "components/Forge/Observability/waterfall/TraceWaterfall.tsx",
  ])("%s deals no categorical colour from a semantic token", (file) => {
    const source = read(file);
    expect(source).not.toMatch(/CATEGORICAL|SERVICE_TOKENS|TONE_SWATCH|\bTONES\b/);
    expect(source).not.toMatch(/"--secondary"|"--primary"|"--accent-foreground"/);
  });
});
