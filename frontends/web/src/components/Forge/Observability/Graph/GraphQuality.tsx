// Copyright (c) 2026 Reliant Labs
import type { GraphQuality as Quality } from "@/services/controlPlane/observability/graphTypes";
export function GraphQuality({ quality, window }: { quality: Quality | null; window?: { startMs: number; endMs: number } | null }) {
  if (!quality) return <p role="status" className="rounded-lg border border-border bg-surface-sunken p-3 text-sm">Coverage metadata unavailable. Missing observations are not zero traffic.</p>;
  return <div role="status" className="space-y-1 rounded-lg border border-border bg-surface-sunken p-3 text-xs text-ink-muted" data-testid="graph-quality">
    <p className="font-medium text-ink">{quality.origin || "Unknown metrics origin"} · received observations, not all real traffic or an SLO</p>
    <p>{quality.availableMinutes}/{quality.requestedMinutes} minutes available · {quality.provisional ? "Provisional" : "Published"}
      {quality.aggregatedThroughMs > 0 && <> · aggregated through {new Date(quality.aggregatedThroughMs).toISOString()}</>}</p>
    {window && <p>Effective interval: {new Date(window.startMs).toISOString()} – {new Date(window.endMs).toISOString()}</p>}
    {(quality.lossUnknown || quality.samplingUnknown) && <p>Upstream {quality.lossUnknown ? "loss" : ""}{quality.lossUnknown && quality.samplingUnknown ? " and " : ""}{quality.samplingUnknown ? "sampling" : ""} unknown.</p>}
    <p>Unmatched: {quality.unmatched.toString()} · conflicting: {quality.conflicts.toString()} · duplicate: {quality.duplicates.toString()} · rejected: {quality.rejected.toString()} · overflow: {quality.overflow.toString()}</p>
    {quality.truncated && <p className="font-medium text-ink">Truncated result — omitted traffic is not zero.</p>}
    {quality.warnings.map((warning, i) => <p key={i}>{warning}</p>)}
  </div>;
}
