// Copyright (c) 2026 Reliant Labs

/**
 * SPEED INSIGHTS (U6c, owner round 5): /forge/p/:app/speed-insights — how
 * fast each page loads and responds for real visitors.
 *
 * The data is already collected: forge's browser runtime
 * (@reliantlabs/forge-web-runtime, telemetry.ts) records web vitals as spans
 * of the `webvitals` tracer, one attribute per vital (`lcp`, `inp`, `cls`,
 * `ttfb`, `fcp`), each stamped with the page's `location.href`. One SQL
 * QueryPanel reads them, grouped by page path, at p75 (the threshold Google
 * judges each vital by):
 *
 *   headline   the app's p75 of each vital over the window, rated
 *              good / needs improvement / poor by web.dev's thresholds
 *   pages      one row per page path: p75 of each vital and its sample count
 *
 * With no samples: what the page will show and how to turn it on (the
 * runtime's telemetry, which a forge frontend scaffolds).
 */

import { useMemo } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { Gauge } from "lucide-react";

import Card from "@/components/forge-ui/card";
import { ProjectEmptyCta } from "@/components/EmptyState/ProjectEmptyCta";
import { DeployWithAgentButton } from "@/components/Forge/Observability/AskReliant/AskReliant";
import Status, { type StatusTone } from "@/components/forge-ui/status";
import { useObservabilityOrg, usePanelQuery } from "@/hooks/observability-queries";
import { cn } from "@/lib/utils";
import type { ObsScope, Panel, PanelResult } from "@/services/controlPlane/observability/types";
import { RANGE_LABELS, RANGE_PRESETS, type RangePreset } from "@/services/controlPlane/observability/window";

import { useForgeConsole } from "../Console/consoleContext";
import { Page } from "../Console/consolePageFrame";
import { ENV_ALL } from "../Console/envScope";
import { PAGE_SUBTITLES } from "../Console/groupedNav";
import PageHeader from "@/components/forge-ui/page_header";
import { VariableSelect } from "../Observability/Dashboards/parts";
import { formatCount } from "../Observability/format";
import { ObsError } from "../Observability/ObsStates";

const DEFAULT_RANGE: RangePreset = "7d";

export type Vital = "lcp" | "inp" | "cls" | "ttfb";
export const VITALS: readonly Vital[] = ["lcp", "inp", "cls", "ttfb"];

/** web.dev's thresholds: good at or under the first, poor over the second. CLS is unitless; the rest milliseconds. */
export const VITAL_INFO: Record<Vital, { label: string; about: string; good: number; poor: number; unit: "ms" | "score" }> = {
  lcp: { label: "Largest Contentful Paint", about: "When the main content has loaded", good: 2500, poor: 4000, unit: "ms" },
  inp: { label: "Interaction to Next Paint", about: "How quickly the page responds to a tap or click", good: 200, poor: 500, unit: "ms" },
  cls: { label: "Cumulative Layout Shift", about: "How much the page jumps around as it loads", good: 0.1, poor: 0.25, unit: "score" },
  ttfb: { label: "Time to First Byte", about: "How long the server takes to start answering", good: 800, poor: 1800, unit: "ms" },
};

export function rateVital(vital: Vital, value: number | null): { tone: StatusTone; label: string } | null {
  if (value === null || !Number.isFinite(value)) return null;
  const { good, poor } = VITAL_INFO[vital];
  if (value <= good) return { tone: "success", label: "Good" };
  if (value <= poor) return { tone: "warning", label: "Needs improvement" };
  return { tone: "danger", label: "Poor" };
}

export function formatVital(vital: Vital, value: number | null): string {
  if (value === null || !Number.isFinite(value)) return "—";
  if (VITAL_INFO[vital].unit === "score") return value.toFixed(2);
  return value >= 1000 ? `${(value / 1000).toFixed(2)} s` : `${Math.round(value)} ms`;
}

const p75 = (vital: Vital) => `quantileIf(0.75)(toFloat64OrZero(SpanAttributes['${vital}']), SpanAttributes['${vital}'] != '') AS ${vital}`;

/** p75 of each vital per page path, from the runtime's `webvitals` spans; the scope's environments. */
export const SPEED_INSIGHTS_PANEL: Panel = {
  id: "speed-insights-pages",
  title: "Web vitals by page",
  kind: "sql",
  signal: "",
  series: [],
  sql: [
    "SELECT path(SpanAttributes['location.href']) AS page,",
    `       ${VITALS.map(p75).join(",\n       ")},`,
    "       count() AS samples",
    "FROM otel_traces",
    "WHERE ScopeName = 'webvitals'",
    "  AND Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)}",
    "  AND (empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, ResourceAttributes['reliant.env.id']))",
    "GROUP BY page",
    "ORDER BY samples DESC",
    "LIMIT 50",
  ].join("\n"),
};

export interface VitalsRow {
  page: string;
  samples: number;
  values: Record<Vital, number | null>;
}

/** The panel's rows: the page in its group, the numbers by column; a vital with no sample reads null. */
export function vitalsRows(result: PanelResult | undefined): VitalsRow[] {
  if (!result) return [];
  const at = (name: string) => result.columns.indexOf(name);
  return result.rows.map((row) => {
    const num = (name: string) => {
      const i = at(name);
      const v = i >= 0 ? (row.values[i] ?? null) : null;
      return v === null || v === 0 ? null : v;
    };
    return {
      page: row.group.page ?? Object.values(row.group)[0] ?? "/",
      samples: num("samples") ?? 0,
      values: { lcp: num("lcp"), inp: num("inp"), cls: num("cls"), ttfb: num("ttfb") },
    };
  });
}

/** The app's figure for a vital: the pages' p75s weighted by their samples (an estimate; the per-page figures are exact). */
export function overallVital(rows: readonly VitalsRow[], vital: Vital): number | null {
  let weight = 0;
  let sum = 0;
  for (const row of rows) {
    const v = row.values[vital];
    if (v === null) continue;
    weight += row.samples;
    sum += v * row.samples;
  }
  return weight ? sum / weight : null;
}

export function SpeedInsightsPage() {
  const { forgeProject, env, roster } = useForgeConsole();
  const search = useSearch({ strict: false }) as { range?: RangePreset };
  const navigate = useNavigate();
  const range = search.range ?? DEFAULT_RANGE;
  const org = useObservabilityOrg();
  const envId = env === ENV_ALL ? undefined : roster.envs.find((candidate) => candidate.name === env)?.live?.id;
  const scope = useMemo<ObsScope | null>(() => (org.orgId ? { orgId: org.orgId, projectId: forgeProject, envIds: envId ? [envId] : [], filters: [] } : null), [org.orgId, forgeProject, envId]);
  const query = usePanelQuery(scope, range, SPEED_INSIGHTS_PANEL, []);
  const rows = useMemo(() => vitalsRows(query.data), [query.data]);
  const samples = rows.reduce((acc, row) => acc + row.samples, 0);
  const windowWords = RANGE_LABELS[range].toLowerCase();

  return (
    <Page testId="console-speed-insights">
      <PageHeader
        density="compact"
        className="mb-0"
        title="Speed insights"
        subtitle={PAGE_SUBTITLES["speed-insights"]}
        meta={rows.length > 0 ? <span>{formatCount(samples)} page views measured · p75</span> : undefined}
        controls={
          <VariableSelect
            id="speed-insights-range"
            label="time"
            value={range}
            options={RANGE_PRESETS.map((preset) => ({ value: preset, label: RANGE_LABELS[preset] }))}
            onChange={(value) => void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, range: value === DEFAULT_RANGE ? undefined : (value as RangePreset) }), replace: true } as never)}
          />
        }
      />
      {query.isLoading ? (
        <div className="h-64 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" aria-label="Loading speed insights" />
      ) : query.error && !query.data ? (
        <ObsError error={query.error} subject={forgeProject} onRetry={() => void query.refetch()} retrying={query.isFetching} />
      ) : rows.length === 0 ? (
        <SpeedInsightsEmpty windowWords={windowWords} />
      ) : (
        <>
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4" data-testid="speed-insights-headline">
            {VITALS.map((vital) => {
              const value = overallVital(rows, vital);
              const rating = rateVital(vital, value);
              return (
                <Card key={vital} padding="none" className="min-w-0 px-4 py-3" data-testid={`speed-insights-${vital}`}>
                  <h3 className="text-ui font-medium text-ink-muted" title={VITAL_INFO[vital].label}>
                    {vital.toUpperCase()}
                  </h3>
                  <div className="mt-0.5 text-2xl font-semibold tabular-nums text-ink">{formatVital(vital, value)}</div>
                  <div className="mt-1 flex items-center gap-2 text-meta text-ink-muted">
                    {rating ? <Status tone={rating.tone} label={rating.label} /> : <span>No samples</span>}
                  </div>
                  <p className="mt-2 text-meta text-ink-muted">{VITAL_INFO[vital].about}</p>
                </Card>
              );
            })}
          </div>
          <section className="space-y-2">
            <h2 className="text-section text-ink">By page</h2>
            <div className="overflow-x-auto rounded-xl border border-border bg-surface" data-testid="speed-insights-pages">
              <table className="w-full min-w-[44rem] border-collapse">
                <thead>
                  <tr className="border-b border-border">
                    <th className="h-9 px-3 text-left text-meta font-medium text-ink-muted">Page</th>
                    {VITALS.map((vital) => (
                      <th key={vital} className="h-9 px-3 text-right text-meta font-medium text-ink-muted" title={VITAL_INFO[vital].label}>
                        {vital.toUpperCase()}
                      </th>
                    ))}
                    <th className="h-9 px-3 text-right text-meta font-medium text-ink-muted">Views</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((row) => (
                    <tr key={row.page} className="border-b border-border-subtle last:border-b-0">
                      <td className="h-11 max-w-0 truncate px-3 font-mono text-code text-ink">{row.page}</td>
                      {VITALS.map((vital) => {
                        const rating = rateVital(vital, row.values[vital]);
                        return (
                          <td
                            key={vital}
                            className={cn(
                              "h-11 px-3 text-right text-ui tabular-nums",
                              rating?.tone === "danger" ? "text-destructive" : rating?.tone === "warning" ? "text-warning-ink" : "text-ink"
                            )}
                          >
                            {formatVital(vital, row.values[vital])}
                          </td>
                        );
                      })}
                      <td className="h-11 px-3 text-right text-ui tabular-nums text-ink-muted">{formatCount(row.samples)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <p className="text-meta text-ink-muted">
              p75 over the {windowWords}: three in four visits were at least this fast. Good / needs improvement / poor follow web.dev&apos;s thresholds.
            </p>
          </section>
        </>
      )}
    </Page>
  );
}

function SpeedInsightsEmpty({ windowWords }: { windowWords: string }) {
  return (
    <ProjectEmptyCta
      testId="speed-insights-empty"
      icon={<Gauge />}
      title={`No page has been measured in the ${windowWords}`}
      deploy={<DeployWithAgentButton env={null} />}
      learn="speed-insights"
    >
      <p>
        Speed insights shows, for every page of your frontend, how fast it loads (LCP), how quickly it responds to a tap or click (INP), how much it shifts as it
        loads (CLS) and how long the server takes to answer (TTFB), as real visitors experience them.
      </p>
      <p>
        A forge frontend measures them already: the browser runtime (<code className="font-mono text-code">@reliantlabs/forge-web-runtime</code>) sends them with its
        telemetry. Deploy a frontend, open it, and the first figures appear within a minute.
      </p>
    </ProjectEmptyCta>
  );
}
