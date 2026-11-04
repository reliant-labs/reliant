// Copyright (c) 2025 Reliant Labs

/**
 * The Sessions view's own states beyond the shared ones (ObsStates): a control
 * plane without sessions, and the frame every other note here uses.
 */

import type { ReactNode } from "react";
import { MonitorPlay } from "lucide-react";

export function SessionNote({ icon, title, children, testId, action }: { icon: ReactNode; title: string; children: ReactNode; testId: string; action?: ReactNode }) {
  return (
    <div role="status" data-testid={testId} className="flex items-start gap-3 rounded-lg border border-dashed border-border bg-surface-sunken px-4 py-5">
      <span className="mt-0.5 shrink-0 text-ink-muted" aria-hidden="true">
        {icon}
      </span>
      <div className="min-w-0 space-y-1.5">
        <p className="text-sm font-medium text-ink">{title}</p>
        <div className="text-xs text-ink-muted">{children}</div>
        {action && <div className="pt-1">{action}</div>}
      </div>
    </div>
  );
}

/** Unimplemented, or Unavailable "not configured": this control plane serves no sessions (yet). */
export function SessionsNotConfigured() {
  return (
    <SessionNote icon={<MonitorPlay className="h-4 w-4" />} title="Sessions aren't available on this control plane" testId="obs-sessions-not-configured">
      <p>
        It doesn&apos;t serve browser sessions or replays yet. Browser spans are still in Traces and browser errors in Errors; sessions appear here once
        the control plane runs the sessions service.
      </p>
    </SessionNote>
  );
}
