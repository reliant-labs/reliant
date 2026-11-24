// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY proving harness for the metrics explorer. NOT a product surface.
 *
 * The real console's metrics explorer, in the real console, on its real
 * clients over the shop fixture pack (ConsoleFixturePreview) —
 * unauthenticated, no backend — so every state can be looked at in every
 * colour scheme and screenshotted. A case is a named overlay over the pack
 * (src/fixtures/shop/overlays.ts observabilityOverlays). The harness's URL
 * says where the explorer opens:
 *
 *   /forge-metrics-preview                     p95 payments.charge.duration, its detail open
 *   /forge-metrics-preview?blank=1             nothing picked yet
 *   …&case=cap|read-only|conflict|not-set-up|no-telemetry|nothing-yet|loading
 *   …&env=all|staging|dev     …&range=24h     …&compare=period
 *   …&dark=1&scheme=pure-black
 *
 * Every explorer key (q, m, bd, hide, chart, tab, sql) works as it does in the
 * console: a link copied from the console's explorer opens the same page here.
 */

import { DEMO_PROJECT } from "@/fixtures/shop/handlers/scope";
import { observabilityOverlays } from "@/fixtures/shop/overlays";

import { consolePath, withSearch } from "../../../Console/paths";
import { ConsoleFixturePreview, isDark, PreviewControls, usePageSearch, withPageSearch } from "../../../__preview__/ConsoleFixturePreview";
import { emptyState, searchFromState } from "../model/explorerState";

const BASE = "/forge-metrics-preview";

const o = observabilityOverlays;

const CASES = [
  { id: "data", label: "the shop" },
  { id: "cap", label: "series cap and warnings", overlay: o.metrics.cap },
  { id: "read-only", label: "member, not admin", overlay: o.metrics["read-only"] },
  { id: "conflict", label: "dashboard saved meanwhile", overlay: o.metrics.conflict },
  { id: "not-set-up", label: "observability not set up", overlay: o.telemetry["not-set-up"] },
  { id: "no-telemetry", label: "nothing sent in the window", overlay: o.telemetry.empty },
  { id: "nothing-yet", label: "set up, nothing arrived this week (getting started)", overlay: o.telemetry["nothing-yet"] },
  { id: "loading", label: "loading", overlay: o.telemetry.loading },
] as const;

/** The page a reviewer lands on: the payment call's latency histogram at p95, its detail open. */
const OPENING = searchFromState({
  ...emptyState(),
  panel: {
    id: "explore",
    title: "Explore",
    kind: "line",
    signal: "metrics",
    series: [{ label: "A", agg: "quantile", quantile: 0.95, field: "payments.charge.duration", metricType: "histogram" }],
    groupBy: ["service.name"],
  },
  detailMetric: "payments.charge.duration",
});

export default function MetricsExplorerPreview() {
  const params = usePageSearch();
  const caseId = params.get("case") ?? "data";
  const active = CASES.find((c) => c.id === caseId) ?? CASES[0];
  const opening = !params.has("q") && !params.has("blank") && !params.has("m");
  const explorer = consolePath(DEMO_PROJECT, { kind: "obs-view", view: "metrics" });
  const path = withPageSearch(opening ? withSearch(explorer, OPENING as Record<string, unknown>) : explorer, ["blank"], params.toString());
  return (
    <ConsoleFixturePreview
      key={`${active.id}|${path}`}
      path={path}
      overlay={"overlay" in active ? active.overlay : null}
      controls={<PreviewControls base={BASE} cases={CASES} caseId={active.id} defaultCase="data" scheme={params.get("scheme") ?? "professional-blue"} dark={isDark(params.get("dark"))} />}
    />
  );
}
