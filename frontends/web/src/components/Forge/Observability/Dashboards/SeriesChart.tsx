// Copyright (c) 2025 Reliant Labs

/**
 * A time-series panel (line, and a sql panel that returned a time series):
 * the chart and its legend, sharing one model of the series (seriesModel.ts)
 * so the swatch beside a name is the colour of its line.
 *
 * `display.style` picks lines (the default), filled areas or one bar per
 * bucket, and `display.stack` stacks areas or bars. A busy panel draws its top
 * N series (display.topN, else eight, the palette's size) and folds the rest
 * into "Other" (see seriesModel, "What adds up"); the legend's "Show all"
 * draws every one.
 */

import type React from "react";
import { useMemo, useState } from "react";

import type { Panel, PanelResult, Unit } from "@/services/controlPlane/observability/types";
import { spanMs } from "@/services/controlPlane/observability/window";

import { UPlotChart, type ChartBand, type ChartSeries } from "../charts/UPlotChart";
import {
  additivityOf,
  alignShifted,
  DANGER_PALETTE,
  alignTo,
  foldTopN,
  OTHER_TOKEN,
  rankEntries,
  rankStat,
  seriesEntries,
  seriesKey,
  statColumns,
  statsOf,
  sumSeries,
  topNOf,
  unionTimestamps,
  type Stat,
} from "./seriesModel";
import { SeriesLegend } from "./SeriesLegend";

/** The way out of a panel into the explorers: what a series or row is, as a filter. */
export interface DrillTarget {
  /** The panel's title, for the menu's heading. */
  panel: string;
  /** The series' label, or the row's first value. */
  label: string;
  /** group-by field → value of the series or row. */
  group: Record<string, string>;
  signal: string;
}

export type OnDrill = (target: DrillTarget, at: { x: number; y: number }) => void;

/** Where a click happened, or — from the keyboard, which has no pointer — the element's bottom-left corner. */
export function pointOf(event: React.MouseEvent | React.KeyboardEvent): { x: number; y: number } {
  if ("clientX" in event && (event.clientX || event.clientY)) return { x: event.clientX, y: event.clientY };
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect();
  return { x: rect.left, y: rect.bottom };
}

const NO_KEYS: ReadonlySet<string> = new Set();

export interface TimeSeriesChartProps {
  panel: Panel;
  data: PanelResult;
  unit: Unit;
  keys: string[];
  compare?: { query: { data: PanelResult | undefined }; label: string; offsetSec: number };
  bands?: readonly ChartBand[];
  onDrill?: OnDrill;
  height: number;
  compact: boolean;
}

export function TimeSeriesChart({ panel, data, unit, keys, compare, bands, onDrill, height, compact }: TimeSeriesChartProps) {
  // keys arrives as a fresh array each render; the chart must not rebuild for that.
  const keyText = keys.join("\u0000");
  const timestamps = useMemo(() => unionTimestamps(data.series), [data]);
  const additivity = useMemo(() => additivityOf(panel, unit), [panel, unit]);
  const ranking = rankStat(additivity);
  const entries = useMemo(
    () => seriesEntries(panel, data, keyText ? keyText.split("\u0000") : [], timestamps, alignTo, ranking),
    [panel, data, keyText, timestamps, ranking]
  );
  const ranked = useMemo(() => rankEntries(entries, ranking), [entries, ranking]);
  const topN = topNOf(panel);

  const [showAll, setShowAll] = useState(false);
  const [sort, setSort] = useState<Stat | null>(null);
  const [isolatedKeys, setIsolated] = useState<ReadonlySet<string>>(NO_KEYS);
  const [highlighted, setHighlighted] = useState<string | null>(null);
  const [cursor, setCursor] = useState<number | null>(null);

  const { shown, rest, restErrors } = useMemo(() => {
    if (showAll) return { shown: ranked, rest: [], restErrors: [] };
    const folded = foldTopN(ranked, topN);
    const tailErrors = folded.rest.filter((e) => e.error);
    const tail = folded.rest.filter((e) => !e.error);
    // Errors are never folded away: summed into "Other errors" when the values
    // add up, else drawn as themselves.
    return additivity.foldable ? { shown: folded.shown, rest: tail, restErrors: tailErrors } : { shown: [...folded.shown, ...tailErrors], rest: tail, restErrors: [] };
  }, [ranked, topN, showAll, additivity.foldable]);
  // A key the latest result no longer has is not "shown alone": forget it.
  const isolated = useMemo(() => {
    const present = new Set(entries.map((e) => e.key));
    const kept = [...isolatedKeys].filter((k) => present.has(k));
    return kept.length === isolatedKeys.size ? isolatedKeys : new Set(kept);
  }, [isolatedKeys, entries]);
  // Drawn in the result's own order, not by rank: a stack keeps its order
  // from one refresh to the next instead of reshuffling as values move.
  const drawn = useMemo(() => {
    const keep = isolated.size ? isolated : new Set(shown.map((e) => e.key));
    return entries.filter((e) => keep.has(e.key));
  }, [isolated, entries, shown]);
  const otherValues = useMemo(() => (rest.length && additivity.foldable ? sumSeries(rest, timestamps.length) : null), [rest, additivity.foldable, timestamps.length]);
  const otherErrorValues = useMemo(() => (restErrors.length ? sumSeries(restErrors, timestamps.length) : null), [restErrors, timestamps.length]);
  const drawOther = isolated.size === 0;

  const style = panel.display?.style;
  const barStyle = style === "bars";
  const areaStyle = style === "area";
  const stack = !!panel.display?.stack && (barStyle || areaStyle);

  const previous = compare?.query.data?.series;
  const compareLabel = compare?.label;
  const offsetSec = compare?.offsetSec ?? 0;
  const chartSeries = useMemo<ChartSeries[]>(() => {
    const main: ChartSeries[] = drawn.map((e) => ({ label: e.label, values: e.values, color: e.token, dotted: e.repeat }));
    if (drawOther && otherValues) main.push({ label: `Other (${rest.length} series)`, values: otherValues, color: OTHER_TOKEN });
    if (drawOther && otherErrorValues) main.push({ label: `Other errors (${restErrors.length} series)`, values: otherErrorValues, color: DANGER_PALETTE[0], dotted: true });
    const compared: ChartSeries[] = (previous ?? []).length
      ? drawn.flatMap((e) => {
          const match = previous!.find((p) => seriesKey(p) === e.key);
          return match ? [{ label: `${e.label} (${compareLabel})`, values: alignShifted(timestamps, match, offsetSec), dashed: true, color: e.token }] : [];
        })
      : [];
    return [...main, ...compared];
  }, [drawn, drawOther, otherValues, otherErrorValues, rest.length, restErrors.length, previous, compareLabel, offsetSec, timestamps]);

  // The panel's minStep IS its bucket (a weekly statement groups by toMonday):
  // the server can only infer one from the first column's type, and a Date
  // reads as a day whatever the statement grouped it by.
  const bucketSeconds = panelBucketSeconds(panel, data);
  const calendar = useMemo(
    () => (data.bucketTimezone === "UTC" ? { timezone: "UTC" as const, bucketSeconds } : undefined),
    [data.bucketTimezone, bucketSeconds]
  );
  const windowSec = useMemo(() => data.window && { startSec: data.window.startMs / 1000, endSec: data.window.endMs / 1000 }, [data.window]);

  const legend = !compact && (entries.length > 1 || !!onDrill);
  const highlightIndex = highlighted === null ? null : drawn.findIndex((e) => e.key === highlighted);
  const columns = statColumns(additivity);
  const activeSort = sort ?? ranking;
  const listed = rankEntries(showAll ? ranked : shown, activeSort);
  const cursorLabel = cursor !== null && timestamps[cursor] !== undefined ? bucketTime(timestamps[cursor], calendar) : null;

  return (
    <div>
      <UPlotChart
        height={height}
        timestamps={timestamps}
        series={chartSeries}
        unit={unit}
        label={panel.title}
        compact={compact}
        bands={bands}
        window={windowSec}
        calendar={calendar}
        area={stack ? "stacked" : areaStyle ? "fill" : undefined}
        bars={barStyle}
        highlight={highlightIndex !== null && highlightIndex >= 0 ? highlightIndex : null}
        onCursor={legend ? setCursor : undefined}
        // The legend below names the series and reads the cursor; uPlot's would repeat them.
        legend={!compact && !legend}
      />
      {legend && (
        <SeriesLegend
          label={`${panel.title}: series`}
          rows={listed.map((e) => ({ key: e.key, label: e.label, token: e.token, repeat: e.repeat, error: e.error, stats: e.stats, atCursor: cursor === null ? null : e.values[cursor] ?? null }))}
          unit={unit}
          columns={columns}
          sort={activeSort}
          onSort={setSort}
          cursorLabel={cursorLabel}
          isolated={isolated}
          highlighted={highlighted}
          onHighlight={setHighlighted}
          onToggle={(key, additive) =>
            setIsolated((prev) => {
              const next = new Set(prev);
              if (additive) {
                if (!next.delete(key)) next.add(key);
                return next;
              }
              return prev.size === 1 && prev.has(key) ? NO_KEYS : new Set([key]);
            })
          }
          onReset={() => setIsolated(NO_KEYS)}
          onDrill={
            onDrill
              ? (key, event) => {
                  const entry = entries.find((e) => e.key === key);
                  if (entry) onDrill({ panel: panel.title, label: entry.label, group: entry.source.group, signal: panel.signal }, pointOf(event));
                }
              : undefined
          }
          others={[
            ...(rest.length ? [{ label: "Other", token: OTHER_TOKEN, count: rest.length, stats: otherValues ? statsOf(otherValues) : undefined, atCursor: otherValues && cursor !== null ? otherValues[cursor] : null }] : []),
            ...(otherErrorValues ? [{ label: "Other errors", token: DANGER_PALETTE[0], count: restErrors.length, stats: statsOf(otherErrorValues), atCursor: cursor !== null ? otherErrorValues[cursor] : null }] : []),
          ]}
          total={entries.length}
          topN={topN}
          showAll={showAll}
          onShowAll={rest.length || restErrors.length || showAll ? setShowAll : undefined}
          compareLabel={compare ? compare.label : undefined}
        />
      )}
    </div>
  );
}

/** The bucket under the pointer: its time, or its date for calendar buckets. */
function bucketTime(sec: number, calendar?: { timezone: "UTC" }): string {
  const date = new Date(sec * 1000);
  if (calendar) return date.toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: "UTC" });
  return date.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

/** A panel's bucket in seconds: its declared minStep, else what the server reported. */
export function panelBucketSeconds(panel: Pick<Panel, "minStep">, data: Pick<PanelResult, "bucketSeconds">): number | undefined {
  const declared = spanMs(panel.minStep);
  return declared !== undefined ? declared / 1000 : data.bucketSeconds;
}
