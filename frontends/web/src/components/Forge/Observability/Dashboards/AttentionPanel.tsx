// Copyright (c) 2026 Reliant Labs

/**
 * "Needs attention" (kind: "attention", forge dashspec KindAttention): the figures on this dashboard
 * that moved the wrong way against the comparison, one line each, worst
 * first, each linking to where the detail is; then the ones that moved the
 * right way. It runs no query of its own: it reads the same panel queries
 * the figures' cards read (one React Query cache entry each, so nothing is
 * fetched twice), and a figure says which way is better by display.good.
 *
 * A figure counts when its change is at least the panel's threshold (5% by
 * default; a percent figure's change is relative too, so 0.30% → 0.25% is a
 * 17% fall). Nothing is invented: with no comparison on, it says so.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { ArrowDownRight, ArrowUpRight, CheckCircle2 } from "lucide-react";

import Link from "@/components/forge-ui/link";
import { usePanelQuery } from "@/hooks/observability-queries";
import { DEFAULT_ATTENTION_THRESHOLD, type ObsScope, type Panel, type PanelResult, type Unit } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { formatPercent, formatValue } from "../format";

export interface AttentionCompare {
  offsetKey: string;
  offsetMs: () => number;
  label: string;
}

export interface AttentionPanelProps {
  panel: Panel;
  /** The watched figures, as their cards query them. */
  watched: Panel[];
  scope: ObsScope | null;
  range: RangePreset;
  dashboardId: string;
  compare: AttentionCompare | null;
  enabled: boolean;
  drillHref: (panel: Panel) => string | null;
}

interface Verdict {
  id: string;
  title: string;
  now: number;
  before: number;
  change: number;
  bad: boolean;
  unit: Unit;
  href: string | null;
}

const figureOf = (data: PanelResult | undefined): number | null => {
  const v = data?.rows.length === 1 ? data.rows[0].values[0] : (data?.series[0]?.value ?? null);
  return v === null || v === undefined || !Number.isFinite(v) ? null : v;
};

function Watch({ panel, scope, range, dashboardId, compare, enabled, href, onVerdict }: {
  panel: Panel;
  scope: ObsScope | null;
  range: RangePreset;
  dashboardId: string;
  compare: AttentionCompare;
  enabled: boolean;
  href: string | null;
  onVerdict: (id: string, verdict: Verdict | null | undefined) => void;
}) {
  const now = usePanelQuery(scope, range, panel, [], [], { enabled, dashboardId });
  const before = usePanelQuery(scope, range, panel, [], [], { offsetKey: compare.offsetKey, offsetMs: compare.offsetMs, enabled, dashboardId });
  const a = figureOf(now.data);
  const b = figureOf(before.data);
  const loading = now.isLoading || before.isLoading;
  useEffect(() => {
    if (loading) return onVerdict(panel.id, undefined);
    if (a === null || b === null || b === 0) return onVerdict(panel.id, null);
    const change = (a - b) / Math.abs(b);
    const good = panel.display?.good;
    onVerdict(panel.id, { id: panel.id, title: panel.title, now: a, before: b, change, bad: good === "up" ? change < 0 : change > 0, unit: (panel.unit ?? "") as Unit, href });
  }, [loading, a, b, panel, href, onVerdict]);
  return null;
}

function sentence(v: Verdict, label: string): string {
  const moved = v.change > 0 ? "rose" : "fell";
  return `${v.title} ${moved} ${formatPercent(Math.abs(v.change))} vs ${label} (${formatValue(v.before, v.unit)} → ${formatValue(v.now, v.unit)})`;
}

export function AttentionPanel({ panel, watched, scope, range, dashboardId, compare, enabled, drillHref }: AttentionPanelProps) {
  const [verdicts, setVerdicts] = useState<Record<string, Verdict | null | undefined>>({});
  const onVerdict = useCallback((id: string, v: Verdict | null | undefined) => setVerdicts((prev) => (prev[id] === v ? prev : { ...prev, [id]: v })), []);
  const threshold = panel.attention?.threshold || DEFAULT_ATTENTION_THRESHOLD;
  const settled = watched.every((p) => verdicts[p.id] !== undefined);
  const moved = useMemo(
    () =>
      Object.values(verdicts)
        .filter((v): v is Verdict => !!v && Math.abs(v.change) >= threshold)
        .sort((x, y) => Math.abs(y.change) - Math.abs(x.change)),
    [verdicts, threshold]
  );
  if (!compare) {
    return <p className="text-xs text-ink-muted">Turn on Compare (previous period) to see which figures moved.</p>;
  }
  const bad = moved.filter((v) => v.bad);
  const good = moved.filter((v) => !v.bad);
  return (
    <div data-testid="obs-panel-attention">
      {watched.map((p) => (
        <Watch key={p.id} panel={p} scope={scope} range={range} dashboardId={dashboardId} compare={compare} enabled={enabled} href={drillHref(p)} onVerdict={onVerdict} />
      ))}
      {!settled ? (
        <div className="h-16 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" aria-busy="true" aria-label="Loading" />
      ) : (
        <>
          <p className="mb-2 text-sm text-ink">
            {bad.length === 0
              ? `Nothing moved the wrong way by ${formatPercent(threshold)} or more vs ${compare.label}.${good.length ? " Improved:" : ""}`
              : `${bad.length} of ${watched.length} watched figures moved the wrong way vs ${compare.label}.`}
          </p>
          <ul className="space-y-1.5">
            {[...bad, ...good].map((v) => {
              const Icon = v.bad ? (v.change < 0 ? ArrowDownRight : ArrowUpRight) : v.change < 0 ? ArrowDownRight : ArrowUpRight;
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
          {moved.length === 0 && (
            <p className="mt-1 flex items-center gap-1.5 text-xs text-ink-muted">
              <CheckCircle2 className="h-3.5 w-3.5 text-success-ink" aria-hidden="true" />
              Every watched figure is within {formatPercent(threshold)} of {compare.label}.
            </p>
          )}
        </>
      )}
    </div>
  );
}
