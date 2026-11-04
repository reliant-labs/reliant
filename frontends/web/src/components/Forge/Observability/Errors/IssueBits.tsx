// Copyright (c) 2025 Reliant Labs

/** Small pieces the issue list and the issue detail share: the status badge and the hourly trend. */

import Badge from "@/components/forge-ui/badge";
import type { IssueStatus, IssueTrendPoint } from "@/services/controlPlane/observability/issuesTypes";

import { STATUS_LABEL, STATUS_TONE } from "./issueModel";

export function StatusBadge({ status }: { status: IssueStatus | null }) {
  if (!status) return <Badge label="Unknown" size="sm" />;
  return <Badge label={STATUS_LABEL[status]} variant={STATUS_TONE[status]} size="sm" />;
}

/** The hourly event counts as bars: the shape of an issue's last day at a glance. */
export function Trend({ points, label }: { points: IssueTrendPoint[]; label: string }) {
  if (points.length === 0) return <span className="text-ink-muted">—</span>;
  const max = Math.max(1, ...points.map((p) => p.events));
  const width = 64;
  const height = 18;
  const bar = width / points.length;
  return (
    <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} className="text-danger-ink/70" role="img" aria-label={label}>
      {points.map((p, index) => {
        const h = p.events > 0 ? Math.max(1.5, (p.events / max) * height) : 0;
        return <rect key={p.hourMs || index} x={index * bar} y={height - h} width={Math.max(1, bar - 1)} height={h} fill="currentColor" />;
      })}
    </svg>
  );
}
