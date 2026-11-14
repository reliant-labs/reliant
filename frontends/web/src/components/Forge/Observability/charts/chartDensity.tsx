// Copyright (c) 2026 Reliant Labs

/**
 * How much a dashboard chart says by default. The console draws every series
 * it has room for (topNOf) and lets the legend carry the numbers; a host may
 * ask for a calmer chart instead: at most `maxSeries` lines before "Show all",
 * and a headline (the figure that matters, in large type, and one sentence)
 * above the plot. Nothing provides it by default, so the console's charts
 * are unchanged; the led console does (Console/ledConsole.tsx).
 */

import { createContext, useContext, type ReactNode } from "react";

export interface ChartDensity {
  /** The most series drawn before "Show all" (the rest fold into Other). Unset keeps the panel's own. */
  maxSeries?: number;
  /** A headline figure and one sentence above each time-series chart. */
  headline?: boolean;
}

const ChartDensityContext = createContext<ChartDensity>({});

export function ChartDensityProvider({ value, children }: { value: ChartDensity; children: ReactNode }) {
  return <ChartDensityContext.Provider value={value}>{children}</ChartDensityContext.Provider>;
}

export function useChartDensity(): ChartDensity {
  return useContext(ChartDensityContext);
}
