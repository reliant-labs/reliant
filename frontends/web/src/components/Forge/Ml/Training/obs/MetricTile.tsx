// Copyright (c) 2026 Reliant Labs

/**
 * A small multiple of the Metrics workspace: the kit's ChartTile look, drawn
 * straight over the console chart so each line keeps its colour when
 * smoothing adds a faint raw line under it (an attempt is the same colour on
 * every tile; a p95 or p5 is its p50's colour, dashed). Events are markers,
 * the redone steps a band; a family nothing reports is <NotCollected>.
 */

import { useMemo, type ReactNode } from "react";

import { MlAxis, MlCollection, type MlEvent, type MlSeries } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { UPlotChart, type ChartBand, type ChartMarker, type ChartSeries } from "../../../Observability/charts/UPlotChart";
import { alignSeries, type MlBand } from "../../kit/charts";
import { fmtCompact, fmtStep, fmtUnit, num, tsMs } from "../../kit/format";
import { eventKindOf, type MlTone } from "../../kit/labels";
import { NotCollected } from "../../kit/ui";
import { ema } from "./obsModel";

const PALETTE = ["--chart-1", "--chart-2", "--chart-3", "--chart-4", "--chart-5", "--chart-6"] as const;
const MARKER_TONE: Record<MlTone, ChartMarker["tone"]> = { danger: "destructive", warning: "warning", success: "success", info: "muted", neutral: "muted" };

/** The line a series belongs to, without its stat: "attempt 2", "layers 28–31 · attempt 3". */
export const lineIdentity = (s: MlSeries) => s.group || s.label || s.key;

/** A palette token per series: one per distinct line identity, in order of first appearance. */
export function paletteOf(series: readonly MlSeries[], pinned?: ReadonlyMap<string, string>): string[] {
  const order: string[] = [];
  return series.map((s) => {
    const id = lineIdentity(s);
    const fixed = pinned?.get(id);
    if (fixed) return fixed;
    let i = order.indexOf(id);
    if (i < 0) i = order.push(id) - 1;
    return PALETTE[i % PALETTE.length];
  });
}

export function MetricTile({
  title,
  series,
  events,
  bands,
  syncKey,
  smoothing = 0,
  height = 120,
  colors,
  note,
  testId,
}: {
  title: string;
  series: readonly MlSeries[];
  events?: readonly MlEvent[];
  bands?: readonly MlBand[];
  syncKey?: string;
  /** EMA weight 0–0.99; 0 draws raw. */
  smoothing?: number;
  height?: number;
  /** Line identity → palette token, so one attempt (or run) has one colour on every tile. */
  colors?: ReadonlyMap<string, string>;
  note?: ReactNode;
  testId?: string;
}) {
  const collected = useMemo(() => series.filter((s) => s.collection === MlCollection.COLLECTED && s.x.length > 0), [series]);
  const axis = series[0]?.axis ?? MlAxis.STEP;
  const unit = series[0]?.unit ?? "";
  const { xs, ys } = useMemo(() => alignSeries(collected), [collected]);
  const chartSeries = useMemo<ChartSeries[]>(() => {
    const tokens = paletteOf(collected, colors);
    const out: ChartSeries[] = [];
    const smoothed: ChartSeries[] = [];
    collected.forEach((s, i) => {
      const name = s.stat ? `${lineIdentity(s)} ${s.stat}` : lineIdentity(s);
      const dashed = !!s.stat && s.stat !== "p50" && s.stat !== "mean";
      const smoothable = smoothing > 0 && !s.stat && s.x.length > 20;
      if (smoothable) {
        out.push({ label: `${name} (raw)`, values: ys[i], color: "--border" });
        const raw = ys[i].map((v) => (v === null ? NaN : v));
        smoothed.push({ label: name, values: ema(raw, smoothing).map((v) => (Number.isFinite(v) ? v : null)), color: tokens[i] });
      } else {
        smoothed.push({ label: name, values: ys[i], color: tokens[i], ...(dashed ? { dashed: true } : {}) });
      }
    });
    return [...out, ...smoothed];
  }, [collected, ys, smoothing, colors]);
  const markers = useMemo<ChartMarker[]>(
    () =>
      (events ?? []).flatMap((e) => {
        const at = axis === MlAxis.TIME ? (tsMs(e.at) ?? 0) / 1000 : axis === MlAxis.STEP ? num(e.step) : 0;
        if (!at) return [];
        const kind = eventKindOf(e.kind);
        return [{ timeSec: at, label: kind.label, description: e.title || kind.label, tone: MARKER_TONE[kind.tone] }];
      }),
    [events, axis],
  );
  const chartBands = useMemo<ChartBand[]>(() => (axis === MlAxis.STEP ? (bands ?? []).map((b) => ({ startSec: b.from, endSec: b.to, label: b.label, tone: b.tone === "warning" ? "warning" : "muted" })) : []), [bands, axis]);
  const latest = useMemo(() => {
    const main = collected.filter((s) => !s.stat || s.stat === "p50");
    const last = main[main.length - 1];
    if (!last || main.length > 1 && new Set(main.map((s) => s.attempt)).size < main.length) return undefined;
    for (let j = last.y.length - 1; j >= 0; j--) if (Number.isFinite(last.y[j])) return fmtUnit(last.y[j], unit);
    return undefined;
  }, [collected, unit]);

  const xFormat = axis === MlAxis.STEP ? (x: number) => `step ${fmtStep(x)}` : axis === MlAxis.TOKENS ? (x: number) => `${fmtCompact(x)} tokens` : undefined;
  return (
    <div className="min-w-0 rounded-xl border border-border bg-surface px-3 pb-2 pt-2.5" data-testid={testId}>
      <div className="mb-1 flex items-baseline justify-between gap-2">
        <h3 className="truncate text-ui font-medium text-ink" title={series.map((s) => s.key).filter((k, i, all) => all.indexOf(k) === i).join(", ")}>
          {title}
        </h3>
        {latest !== undefined && <span className="shrink-0 text-ui tabular-nums text-ink">{latest}</span>}
      </div>
      {note && <p className="-mt-0.5 mb-1 truncate text-meta text-ink-muted">{note}</p>}
      {collected.length === 0 ? (
        <TileMissing title={title} series={series} height={height} />
      ) : (
        <UPlotChart timestamps={xs} series={chartSeries} format={(v) => fmtUnit(v, unit)} height={height} label={title} legend={false} markers={markers} bands={chartBands} syncKey={syncKey} xFormat={xFormat} />
      )}
    </div>
  );
}

function TileMissing({ title, series, height }: { title: string; series: readonly MlSeries[]; height: number }) {
  const na = series.find((s) => s.collection === MlCollection.NOT_APPLICABLE);
  if (na && !series.some((s) => s.collection === MlCollection.NOT_COLLECTED)) {
    return (
      <div className="flex flex-col items-center justify-center gap-1 rounded-md border border-dashed border-border bg-surface-sunken px-3 text-center" style={{ minHeight: height }} data-testid="ml-not-applicable">
        <span className="text-meta font-medium text-ink-muted">Not applicable</span>
        {na.missingSource && <span className="text-meta text-ink-muted">{na.missingSource}</span>}
      </div>
    );
  }
  const missing = series.find((s) => s.collection === MlCollection.NOT_COLLECTED);
  return <NotCollected what={title} source={missing?.missingSource} findingId={missing?.fixFindingId} height={height} />;
}
