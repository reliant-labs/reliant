// Copyright (c) 2025 Reliant Labs

/**
 * THE CATEGORICAL PALETTE, ONCE.
 *
 * Every chart that colours things by "which one" — a line per series in
 * UPlotChart and the metrics explorer, a slice or bar on a dashboard, a
 * service in a trace waterfall — deals its colours from here, in this order.
 * There used to be three copies, each built from SEMANTIC tokens: series 2
 * was `--secondary` (a surface tint, 1.1-1.3:1 against the card, so the line
 * vanished), series 3 was `--success` (read as "healthy") and series 5 was
 * `--destructive` (read as "failing"), and in pure-black `--primary` made
 * series 1 white.
 *
 * The tokens are themes/chart-palette.css: brand blue, brand violet, cyan,
 * amber, fuchsia and slate, each >= 3:1 against every scheme's card, with no
 * green and no red. Those two stay status colours: a series that IS errors is
 * drawn in DANGER_PALETTE, deliberately, by the caller that knows it is one.
 */

/** The categorical data-viz tokens, in the order they are dealt. */
export const CHART_PALETTE = ["--chart-1", "--chart-2", "--chart-3", "--chart-4", "--chart-5", "--chart-6"] as const;
export type ChartPaletteToken = (typeof CHART_PALETTE)[number];

/** Error series: the danger token first, then its alternate. */
export const DANGER_PALETTE = ["--destructive", "--chart-danger-alt"] as const;

/** "Other" (a folded tail): neutral, so it never reads as one more series. */
export const OTHER_TOKEN = "--muted-foreground";

/** The token for the n-th thing (0-based), cycling past the palette's end. */
export function chartToken(index: number): ChartPaletteToken {
  const n = CHART_PALETTE.length;
  return CHART_PALETTE[((Math.trunc(index) % n) + n) % n]!;
}

/** A palette token as a CSS colour, optionally translucent (the tokens are bare HSL triplets). */
export function tokenColor(token: string, alpha?: number): string {
  return alpha === undefined ? `hsl(var(${token}))` : `hsl(var(${token}) / ${alpha})`;
}

/** The n-th categorical colour, as CSS. */
export function chartColor(index: number, alpha?: number): string {
  return tokenColor(chartToken(index), alpha);
}
