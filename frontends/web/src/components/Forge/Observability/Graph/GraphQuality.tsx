// Copyright (c) 2026 Reliant Labs
import { Info } from "lucide-react";
import Popover from "@/components/forge-ui/popover";
import type { GraphQuality as Quality } from "@/services/controlPlane/observability/graphTypes";
import { formatAgo } from "../format";

/** Aggregation lags this far behind before the page says the data is delayed. */
const STALE_MS = 5 * 60_000;

/**
 * Data-quality details behind one info icon. The graph and catalog are built
 * from bounded minute aggregates of received spans; the fine print lives
 * here, not in a wall of text above the data.
 */
export function GraphQualityInfo({ quality, window, notes = [] }: {
  quality: Quality | null; window?: { startMs: number; endMs: number } | null; notes?: string[];
}) {
  return <Popover align="right" triggerLabel="About this data" panelClassName="w-80 p-3"
    triggerClassName="inline-flex h-8 w-8 items-center justify-center rounded-md text-ink-muted hover:bg-surface-muted hover:text-ink"
    trigger={<Info aria-hidden className="h-4 w-4" />}>
    <div className="space-y-2 text-xs text-ink-muted" data-testid="graph-quality">
      <p className="text-sm font-medium text-ink">About this data</p>
      <p>Built from per-minute aggregates of the spans received, before trace sampling. Sampling or dropped telemetry upstream can leave gaps, so a missing connection is not proof of zero traffic.</p>
      {quality ? <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
        <dt>Minutes with data</dt><dd className="tabular-nums text-ink">{quality.availableMinutes} of {quality.requestedMinutes}</dd>
        {quality.aggregatedThroughMs > 0 && <><dt>Aggregated</dt><dd className="text-ink">{formatAgo(quality.aggregatedThroughMs)}</dd></>}
        {window && <><dt>Window</dt><dd className="text-ink">{new Date(window.startMs).toLocaleTimeString()} – {new Date(window.endMs).toLocaleTimeString()}</dd></>}
        {quality.unmatched > 0n && <><dt>Unpaired calls</dt><dd className="tabular-nums text-ink">{quality.unmatched.toString()}</dd></>}
      </dl> : <p>Coverage details are unavailable for this result.</p>}
      {[...notes, ...(quality?.warnings ?? [])].length > 0 && <ul className="list-disc space-y-1 border-t border-border pt-2 pl-4">
        {[...new Set([...notes, ...(quality?.warnings ?? [])])].map((w) => <li key={w}>{w}</li>)}
      </ul>}
    </div>
  </Popover>;
}

/** One subtle line, only when the numbers on screen are actually partial or late. */
export function GraphQualityNotice({ quality, now = Date.now() }: { quality: Quality | null; now?: number }) {
  if (!quality) return null;
  const stale = quality.aggregatedThroughMs > 0 && now - quality.aggregatedThroughMs > STALE_MS;
  const message = quality.truncated
    ? "Counts here are partial: part of this window's traffic was too large to aggregate or display."
    : stale ? `Traffic data is delayed; last aggregated ${formatAgo(quality.aggregatedThroughMs, now)}.` : "";
  return message ? <p role="status" className="text-xs text-ink-muted" data-testid="graph-quality-notice">{message}</p> : null;
}
