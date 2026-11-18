// Copyright (c) 2026 Reliant Labs

/**
 * Beside a range picker: what a view shows when the picked range is longer
 * than its telemetry is kept ("Logs are kept for 7 days, so this shows the
 * last 7 days."). Every picker offers every preset, "Last 30 days" included;
 * a view that cannot cover one says so rather than refusing it or silently
 * showing less. Nothing is drawn when the range is covered.
 */

import { rangeCapNote, type RangePreset, type RangeRead } from "@/services/controlPlane/observability/window";

export function RangeCapNote({ range, read, subject, testId = "range-cap" }: { range: RangePreset; read: RangeRead; subject: string; testId?: string }) {
  const note = rangeCapNote(range, read, subject);
  if (!note) return null;
  return (
    <span className="max-w-[16rem] text-xs text-ink-muted" title={note} data-testid={testId}>
      {note}
    </span>
  );
}
