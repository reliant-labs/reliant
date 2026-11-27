// Copyright (c) 2026 Reliant Labs

/**
 * What jsdom lacks and the ML kit's chart (uPlot, through
 * Observability/charts/UPlotChart) touches when its module LOADS:
 * `matchMedia` and `ResizeObserver`. Import this FIRST in any ML test that
 * imports a view or kit/charts (`import "@/components/Forge/Ml/__tests__/jsdomShims";`):
 * imports run in order, so it is in place before uPlot loads.
 */

const g = globalThis as unknown as Record<string, unknown>;

if (typeof g.matchMedia !== "function") {
  g.matchMedia = (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  });
}

if (typeof g.ResizeObserver !== "function") {
  g.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
}

export {};
