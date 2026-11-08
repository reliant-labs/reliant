// Copyright (c) 2025 Reliant Labs

/**
 * The Sessions view's own states beyond the shared ones (ObsStates): a control
 * plane without sessions, an unfiltered window with no replays, and the frame
 * every other note here uses.
 */

import type { ReactNode } from "react";
import { ArrowUpRight, MonitorPlay } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { DOCS } from "@/components/Forge/GettingStarted";
import { RANGE_LABELS, type RangePreset } from "@/services/controlPlane/observability/window";

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

const CODE = "font-mono text-ink";

/**
 * The first page is empty and nothing narrows it: no replay was recorded in
 * the window. The console cannot tell whether replay is on (neither switch is
 * visible to it), so the note says how to turn it on without claiming it is off.
 */
export function SessionsReplayOff({ range, onWiden }: { range: RangePreset; onWiden?: () => void }) {
  return (
    <SessionNote
      icon={<MonitorPlay className="h-4 w-4" />}
      title={`No replays recorded in the ${RANGE_LABELS[range].toLowerCase()}`}
      testId="obs-sessions-replay-off"
      action={
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          {onWiden && range !== "7d" && (
            <Button variant="ghost" size="sm" onClick={onWiden} data-testid="obs-widen">
              Look at the last 7 days
            </Button>
          )}
          <a
            href={DOCS.sessionReplay}
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-1 rounded-sm text-xs font-medium text-primary-ink underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            data-testid="obs-sessions-replay-off-docs"
          >
            How session replay works
            <ArrowUpRight className="h-3 w-3" aria-hidden="true" />
          </a>
        </div>
      }
    >
      <div className="max-w-2xl space-y-1.5">
        <p>
          Sessions lists browser sessions that have a replay. Replay is off by default, and every replay is masked when it is on. If replay
          isn&apos;t on yet, turn on both switches, then redeploy:
        </p>
        <ol className="list-decimal space-y-1 pl-4" data-testid="obs-sessions-replay-off-steps">
          <li>
            The browser build flag: <code className={CODE}>VITE_OTEL_REPLAY=true</code> for Vite, <code className={CODE}>NEXT_PUBLIC_OTEL_REPLAY=true</code> for
            Next.js.
          </li>
          <li>
            The environment&apos;s <code className={CODE}>forge.Observability</code> in KCL:{" "}
            <code className={CODE}>signals = forge.ObservabilitySignals {"{replay = True}"}</code>.
          </li>
        </ol>
        <p>Browser spans still arrive in Traces and browser errors in Errors without replay.</p>
      </div>
    </SessionNote>
  );
}
