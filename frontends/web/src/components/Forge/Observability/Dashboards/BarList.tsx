// Copyright (c) 2026 Reliant Labs

/**
 * A ranked list drawn as bars (display.style: "barlist", forge dashspec
 * StyleBarList): one row per result row, its name on the left over a bar as long as its share
 * of the largest value, the value at the right, and the row's other numbers
 * as small columns after it. It is what a ranking or a share (top pages,
 * sources, countries, goals) looks like in Plausible or PostHog, and what the
 * same sql result drew as a bare table before.
 *
 *   label     the first text column; any other text column is a muted second
 *             line (a referrer's path, a landing page's channel).
 *   bar       display.bar, else the first numeric column.
 *   columns   the other numeric columns, in the result's order. A column whose
 *             name ends in "%" holds 0-100 (forge's convention: "Conversion %")
 *             and is drawn with a % sign; display.units names the rest.
 *   share     display.share: the bar's value is followed by its share of the
 *             rows' total.
 *   trend     a trendSql answer grouped by the label: a sparkline per row.
 *
 * The columns after the bar drop out when the card is narrow (a container
 * query), so the same panel reads at a quarter of the grid and on a phone. A
 * long list shows its first `limit` rows and "Show all N".
 */

import { useMemo, useState } from "react";

import type { Panel, PanelResult, Unit } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import { countryFlag, countryName, isCountryCode } from "../geo";
import { formatCount, formatPercent, formatValue } from "../format";
import { UPlotChart } from "../charts/UPlotChart";
import { CHART_PALETTE, namedToken, tokenColor } from "./seriesModel";

export interface BarListProps {
  panel: Panel;
  data: PanelResult;
  /** The panel's unit: the bar column's, when display.units does not name it. */
  unit: Unit;
  keys: string[];
  /** The panel's trendSql answer: one series per row, grouped by the row's label. */
  trend?: PanelResult;
  compact?: boolean;
  /** Rows before "Show all"; absent derives it from the panel's height. */
  limit?: number;
}

interface Column {
  index: number;
  header: string;
  unit: Unit | "pct";
}

function columnOf(name: string, index: number, display: Panel["display"], fallback: Unit): Column {
  const named = display?.units?.[name];
  if (named) return { index, header: name, unit: named };
  if (/%\s*$/.test(name)) return { index, header: name.replace(/\s*%\s*$/, ""), unit: "pct" };
  return { index, header: name, unit: fallback };
}

function format(value: number | null | undefined, unit: Column["unit"]): string {
  if (value === null || value === undefined) return "—";
  if (unit === "pct") return `${value < 10 && value % 1 !== 0 ? value.toFixed(value < 1 ? 2 : 1) : Math.round(value)}%`;
  return formatValue(value, unit);
}

export function BarList({ panel, data, unit, keys, trend, compact = false, limit }: BarListProps) {
  const [all, setAll] = useState(false);
  // A timestamp column ("Last seen (UTC)") is a table's detail, not a name: a bar list leaves it to the table.
  const textKeys = (keys.length ? keys : Object.keys(data.rows[0]?.group ?? {})).filter((k, i) => i === 0 || !/last seen|\(utc\)/i.test(k));
  const barName = panel.display?.bar && data.columns.includes(panel.display.bar) ? panel.display.bar : data.columns[0];
  const bar = columnOf(barName ?? "", data.columns.indexOf(barName ?? ""), panel.display, unit === "percent" ? "percent" : unit || "count");
  const others = data.columns.map((name, index) => columnOf(name, index, panel.display, "count")).filter((c) => c.index !== bar.index);
  const token = namedToken(panel.display, [barName], true) ?? CHART_PALETTE[0];
  const values = data.rows.map((row) => row.values[bar.index] ?? null);
  const max = Math.max(0, ...values.map((v) => Math.abs(v ?? 0)));
  const total = values.reduce<number>((sum, v) => sum + (v ?? 0), 0);
  const cap = limit ?? Math.max(4, Math.min(12, Math.round(((panel.height || 8) * 28 - 110) / 30)));
  const rows = compact ? data.rows.slice(0, 3) : all ? data.rows : data.rows.slice(0, cap);

  // One sparkline per row, from the trend's series grouped like the rows.
  const trendOf = useMemo(() => {
    const map = new Map<string, { timestamps: number[]; values: (number | null)[] }>();
    // Matched by the group's values, not its column names: a trend statement
    // may name its column "channel" where the list's says "Channel".
    for (const s of trend?.series ?? []) map.set(Object.values(s.group).join("\u0000"), s);
    return map;
  }, [trend]);
  const sparkline = !compact && trendOf.size > 0;

  return (
    <div className="@container" data-testid="obs-panel-barlist">
      <div className="mb-1 flex items-center gap-3 text-2xs text-ink-muted" aria-hidden="true">
        <span className="min-w-0 flex-1 truncate">{textKeys[0]}</span>
        {sparkline && <span className="hidden w-16 shrink-0 text-right @md:block">Trend</span>}
        <span className="w-14 shrink-0 text-right">{bar.header}</span>
        {others.map((c, i) => (
          <span key={c.header} className={cn("hidden w-16 shrink-0 text-right", i < 1 ? "@sm:block" : i < 3 ? "@lg:block" : "@2xl:block")}>
            {c.header}
          </span>
        ))}
      </div>
      <ol className="space-y-1" aria-label={panel.title}>
        {rows.map((row, r) => {
          const value = row.values[bar.index] ?? null;
          const width = max > 0 && value !== null ? Math.max((Math.abs(value) / max) * 100, value !== 0 ? 1.5 : 0) : 0;
          const label = row.group[textKeys[0]] ?? Object.values(row.group)[0] ?? "";
          const sub = textKeys.slice(1).map((k) => row.group[k]).filter(Boolean).join(" · ");
          const country = /country/i.test(textKeys[0] ?? "") && isCountryCode(label);
          const line = trendOf.get(label) ?? trendOf.get(Object.values(row.group).join("\u0000"));
          return (
            <li key={`${label}-${r}`} className="flex items-center gap-3 text-xs" data-row={label}>
              <span className="relative min-w-0 flex-1">
                <span className="absolute inset-y-0 left-0 rounded-sm" style={{ width: `${width}%`, backgroundColor: tokenColor(token, 0.18) }} aria-hidden="true" />
                <span className="relative flex min-w-0 items-baseline gap-1.5 px-2 py-1">
                  {country && <span aria-hidden="true">{countryFlag(label)}</span>}
                  <span className="truncate text-ink" title={label}>
                    {country ? countryName(label) : label || "(none)"}
                  </span>
                  {sub && <span className="hidden truncate text-ink-muted @sm:inline">{sub}</span>}
                </span>
              </span>
              {sparkline && (
                <span className="hidden w-16 shrink-0 @md:block" aria-hidden="true">
                  {line && line.values.filter((v) => v !== null).length > 1 ? (
                    <UPlotChart height={18} timestamps={line.timestamps} series={[{ label, values: line.values, color: token }]} unit={unit} label={`${label}, over the window`} compact />
                  ) : null}
                </span>
              )}
              <span className="w-14 shrink-0 text-right font-medium tabular-nums text-ink">
                {format(value, bar.unit)}
                {panel.display?.share && total > 0 && value !== null && <span className="ml-1 font-normal text-ink-muted">{formatPercent(value / total)}</span>}
              </span>
              {others.map((c, i) => (
                <span key={c.header} className={cn("hidden w-16 shrink-0 text-right tabular-nums text-ink-muted", i < 1 ? "@sm:block" : i < 3 ? "@lg:block" : "@2xl:block")}>
                  {format(row.values[c.index], c.unit)}
                </span>
              ))}
            </li>
          );
        })}
      </ol>
      {!compact && data.rows.length > cap && (
        <button type="button" className="mt-2 text-xs text-ink-muted underline decoration-ink-muted/50 underline-offset-2 hover:text-ink" onClick={() => setAll((v) => !v)} data-testid="obs-barlist-all">
          {all ? "Show fewer" : `Show all ${formatCount(data.rows.length)}`}
        </button>
      )}
    </div>
  );
}
