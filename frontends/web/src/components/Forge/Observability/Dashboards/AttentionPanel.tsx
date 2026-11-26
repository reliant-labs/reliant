// Copyright (c) 2026 Reliant Labs

/**
 * "Needs attention" (kind: "attention", forge dashspec KindAttention): the
 * figures on this dashboard that moved the wrong way against the comparison,
 * one line each, worst first, each linking to where the detail is; then the
 * ones that moved the right way. It runs no query of its own: it reads each
 * watched figure exactly as its card does (the same panel, the same services,
 * so the same React Query cache entry: nothing is fetched twice, and nothing
 * different is judged), and a figure says which way is better by display.good.
 *
 * A figure counts when its change is at least the panel's threshold (5% by
 * default; a percent figure's change is relative too, so 0.30% → 0.25% is a
 * 17% fall). Nothing is invented: with no comparison on, it says so; a figure
 * it could not compare (a failed read, no figure, no or a zero earlier
 * figure) is named as not compared, and never counted toward an all-clear.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { ArrowDownRight, ArrowUpRight, CheckCircle2 } from "lucide-react";

import Link from "@/components/forge-ui/link";
import { usePanelQuery, type PanelOffset } from "@/hooks/observability-queries";
import { DEFAULT_ATTENTION_THRESHOLD, type ObsScope, type Panel, type PanelResult, type Unit } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { formatPercent, formatValue } from "../format";

/** How a watched figure's card reads it (cardRead): its range, and its comparison read. */
export interface FigureRead {
  range: RangePreset;
  compare: { offset: PanelOffset } | null;
}

export interface AttentionCompare {
  label: string;
  /** Each figure's reads, exactly as its card makes them, so both share one cache entry. */
  readOf: (panel: Panel) => FigureRead;
}

/** A watched figure as its card reads it: the panel and the services the viewer's filter narrows it to. */
export interface WatchedFigure {
  panel: Panel;
  services: string[];
}

export interface AttentionPanelProps {
  panel: Panel;
  /** The watched figures, as their cards query them. */
  watched: WatchedFigure[];
  scope: ObsScope | null;
  dashboardId: string;
  compare: AttentionCompare | null;
  enabled: boolean;
  drillHref: (panel: Panel) => string | null;
}

/** Why a figure was not compared. */
type Unavailable = "failed" | "no-figure" | "no-baseline" | "zero-baseline";

type Verdict =
  | { state: "compared"; id: string; title: string; now: number; before: number; change: number; bad: boolean; unit: Unit; href: string | null }
  | { state: "unavailable"; id: string; title: string; reason: Unavailable };

type Compared = Extract<Verdict, { state: "compared" }>;

const UNAVAILABLE: Record<Unavailable, string> = {
  failed: "its read failed",
  "no-figure": "no figure for this range",
  "no-baseline": "no earlier figure",
  "zero-baseline": "the earlier figure is zero",
};

const figureOf = (data: PanelResult | undefined): number | null => {
  const v = data?.rows.length === 1 ? data.rows[0].values[0] : (data?.series[0]?.value ?? null);
  return v === null || v === undefined || !Number.isFinite(v) ? null : v;
};

function Watch({ figure, scope, dashboardId, compare, enabled, href, onVerdict }: {
  figure: WatchedFigure;
  scope: ObsScope | null;
  dashboardId: string;
  compare: AttentionCompare;
  enabled: boolean;
  href: string | null;
  onVerdict: (id: string, verdict: Verdict | undefined) => void;
}) {
  const { panel, services } = figure;
  const read = compare.readOf(panel);
  const now = usePanelQuery(scope, read.range, panel, services, [], { enabled, dashboardId });
  const before = usePanelQuery(scope, read.range, panel, services, [], { offset: read.compare?.offset, enabled: enabled && !!read.compare, dashboardId });
  const loading = now.isLoading || before.isLoading;
  const failed = !!now.error || !!before.error;
  const a = figureOf(now.data);
  const b = figureOf(before.data);
  useEffect(() => {
    const unavailable = (reason: Unavailable) => onVerdict(panel.id, { state: "unavailable", id: panel.id, title: panel.title, reason });
    if (failed) return unavailable("failed");
    if (loading) return onVerdict(panel.id, undefined);
    if (a === null) return unavailable("no-figure");
    if (b === null) return unavailable("no-baseline");
    if (b === 0) return unavailable("zero-baseline");
    const change = (a - b) / Math.abs(b);
    const good = panel.display?.good;
    onVerdict(panel.id, { state: "compared", id: panel.id, title: panel.title, now: a, before: b, change, bad: good === "up" ? change < 0 : change > 0, unit: (panel.unit ?? "") as Unit, href });
  }, [loading, failed, a, b, panel, href, onVerdict]);
  return null;
}

function sentence(v: Compared, label: string): string {
  const moved = v.change > 0 ? "rose" : "fell";
  return `${v.title} ${moved} ${formatPercent(Math.abs(v.change))} vs ${label} (${formatValue(v.before, v.unit)} → ${formatValue(v.now, v.unit)})`;
}

export function AttentionPanel({ panel, watched, scope, dashboardId, compare, enabled, drillHref }: AttentionPanelProps) {
  const [verdicts, setVerdicts] = useState<Record<string, Verdict | undefined>>({});
  const onVerdict = useCallback((id: string, v: Verdict | undefined) => setVerdicts((prev) => (prev[id] === v ? prev : { ...prev, [id]: v })), []);
  const threshold = panel.attention?.threshold || DEFAULT_ATTENTION_THRESHOLD;
  const current = watched.map((w) => verdicts[w.panel.id]);
  const settled = current.every((v) => v !== undefined);
  const compared = useMemo(() => current.filter((v): v is Compared => v?.state === "compared"), [current]);
  const notCompared = current.filter((v): v is Extract<Verdict, { state: "unavailable" }> => v?.state === "unavailable");
  const moved = useMemo(() => compared.filter((v) => Math.abs(v.change) >= threshold).sort((x, y) => Math.abs(y.change) - Math.abs(x.change)), [compared, threshold]);
  if (!compare) {
    return <p className="text-xs text-ink-muted">Turn on Compare (previous period) to see which figures moved.</p>;
  }
  const bad = moved.filter((v) => v.bad);
  const good = moved.filter((v) => !v.bad);
  const headline =
    bad.length > 0
      ? `${bad.length} of ${compared.length} compared figures moved the wrong way vs ${compare.label}.`
      : compared.length === 0
        ? `None of the ${watched.length} watched figures could be compared with ${compare.label}.`
        : `Nothing moved the wrong way by ${formatPercent(threshold)} or more vs ${compare.label}.${good.length ? " Improved:" : ""}`;
  return (
    <div data-testid="obs-panel-attention">
      {watched.map((w) => (
        <Watch key={w.panel.id} figure={w} scope={scope} dashboardId={dashboardId} compare={compare} enabled={enabled} href={drillHref(w.panel)} onVerdict={onVerdict} />
      ))}
      {!settled ? (
        <div className="h-16 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" aria-busy="true" aria-label="Loading" />
      ) : (
        <>
          <p className="mb-2 text-sm text-ink">{headline}</p>
          <ul className="space-y-1.5">
            {[...bad, ...good].map((v) => {
              const Icon = v.change < 0 ? ArrowDownRight : ArrowUpRight;
              const body = (
                <>
                  <Icon className={cn("mt-0.5 h-3.5 w-3.5 shrink-0", v.bad ? "text-danger-ink" : "text-success-ink")} aria-hidden="true" />
                  <span className={cn("text-xs", v.bad ? "text-ink" : "text-ink-muted")}>{sentence(v, compare.label)}</span>
                </>
              );
              return (
                <li key={v.id} data-attention={v.bad ? "bad" : "good"}>
                  {v.href ? (
                    <Link href={v.href} className="no-color flex items-start gap-1.5 rounded px-1 py-0.5 hover:bg-ink/5">
                      {body}
                    </Link>
                  ) : (
                    <span className="flex items-start gap-1.5 px-1 py-0.5">{body}</span>
                  )}
                </li>
              );
            })}
          </ul>
          {compared.length > 0 && moved.length === 0 && (
            <p className="mt-1 flex items-center gap-1.5 text-xs text-ink-muted">
              <CheckCircle2 className="h-3.5 w-3.5 text-success-ink" aria-hidden="true" />
              {notCompared.length ? `Every compared figure is within ${formatPercent(threshold)} of ${compare.label}.` : `Every watched figure is within ${formatPercent(threshold)} of ${compare.label}.`}
            </p>
          )}
          {notCompared.length > 0 && (
            <p className="mt-1 text-xs text-ink-muted" data-testid="obs-attention-not-compared">
              Not compared: {notCompared.map((v) => `${v.title} (${UNAVAILABLE[v.reason]})`).join(", ")}.
            </p>
          )}
        </>
      )}
    </div>
  );
}
