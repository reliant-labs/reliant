// Copyright (c) 2026 Reliant Labs

/**
 * PARTIAL: the trace is not the whole story of its window, and why, in words.
 * A trace is never silently short (ChatTraceService): an expired span store,
 * a span store this deployment cannot read, or a cap on rows each says so
 * here rather than drawing a shorter chart as if it were complete.
 *
 * Status colour is the icon only; the banner is a plain bordered note.
 */

import { AlertTriangle } from "lucide-react";

export function ChatTracePartialBanner({ reasons }: { reasons: string[] }) {
  if (reasons.length === 0) return null;
  return (
    <div
      role="status"
      className="flex gap-2.5 rounded-xl border border-border bg-card px-3 py-2.5"
      data-testid="chat-trace-partial"
    >
      <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warning" aria-hidden="true" />
      <div className="min-w-0 space-y-1">
        <p className="text-ui font-medium text-foreground">Partial trace</p>
        <ul className="space-y-0.5 text-ui text-muted-foreground">
          {reasons.map((reason) => (
            <li key={reason} data-testid="chat-trace-partial-reason">
              {reason}
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}
