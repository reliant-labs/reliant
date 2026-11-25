/**
 * The visual harness's clock: the page's Date is frozen here (page.clock), and
 * the shop pack's clock is pinned here (window.__forgeFixtures.now), so every
 * "5m ago" in the shop world reads the same on every run. It is the pack's
 * DATA_ANCHOR (src/fixtures/shop/packJson.ts), so its data files shift by zero.
 */

/** 2026-11-18 15:30 UTC, a Wednesday. */
export const FIXED_NOW = Date.UTC(2026, 10, 18, 15, 30, 0)
