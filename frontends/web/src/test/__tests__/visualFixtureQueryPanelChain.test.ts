// Copyright (c) 2026 Reliant Labs

/**
 * The visual harness's ONE QueryPanel chain (e2e/visual/fixtures/index.ts):
 * several fixture files answer QueryPanel, and a spread keeps only the last
 * table's, so index.ts composes them. Each file must answer its own panels and
 * hand every other down — overview → speed insights → u6c → services →
 * console — so no page in the preview loses its data to
 * another file's fixture.
 */

import { describe, expect, it } from "vitest";

// Imported by a non-literal specifier so the app's typecheck (tsconfig.test)
// does not take in the harness's sources, which Playwright compiles itself.
const FIXTURES = "../../../e2e/visual/fixtures";
const { allFixtures } = (await import(/* @vite-ignore */ FIXTURES)) as { allFixtures: Record<string, unknown> };

const queryPanel = allFixtures["controlplane.v1.ObservabilityService/QueryPanel"] as (req: Record<string, unknown>) => Record<string, unknown>;
const ask = (id: string, kind = "line") => queryPanel({ panel: { id, kind }, scope: {} });

describe("the visual harness's QueryPanel chain", () => {
  it.each([
    ["overview-analytics-totals", "overview"],
    ["overview-analytics-daily", "overview"],
    ["speed-insights-summary", "speed insights"],
    ["speed-insights-routes", "speed insights"],
    ["speed-insights-series", "speed insights"],
    // u6c.ts owns no panel since Speed insights moved out of it: it only passes them down.
    ["product-events-catalog", "services"],
    ["custom-metric-checkout_conversion", "services"],
    ["observability-home-requests", "console"],
  ])("answers %s (%s)", (id) => {
    const answer = ask(id);
    expect(answer).toBeTypeOf("object");
    expect(answer.panelId).toBe(id);
  });

  it("answers an unknown panel with the console's empty answer, not a throw", () => {
    expect(ask("no-such-panel")).toEqual({});
  });
});
