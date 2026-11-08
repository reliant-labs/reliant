// Copyright (c) 2025 Reliant Labs

/**
 * The Sessions view's own states beyond the shared ones (ObsStates): a control
 * plane without sessions, an unfiltered window with no replays (with each
 * frontend's replay state when the console can read it), and the frame every
 * other note here uses.
 */

import type { ReactNode } from "react";
import { ArrowUpRight, MonitorPlay } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Status, { type StatusTone } from "@/components/forge-ui/status";
import { DOCS } from "@/components/Forge/GettingStarted";
import { RANGE_LABELS, type RangePreset } from "@/services/controlPlane/observability/window";

import { samplePercent, type FrontendReplayNote, type FrontendReplayNoteKind, type ReplayStatus } from "./replayStatus";

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
const TELEMETRY_REPLAY = "telemetry = forge.BrowserTelemetry {replay = True}";

const NOTE_TONE: Record<FrontendReplayNoteKind, StatusTone> = {
  on: "success",
  off: "neutral",
  "build-flag": "warning",
  blocked: "warning",
  unknown: "neutral",
};

/** One frontend in one environment: its replay state and, when it is not on, the one change that turns it on. */
function FrontendReplayLine({ note }: { note: FrontendReplayNote }) {
  const fe = <code className={CODE}>{note.frontend}</code>;
  const env = <span className="font-medium text-ink">{note.envName}</span>;
  const telemetry = <code className={CODE}>{TELEMETRY_REPLAY}</code>;
  return (
    <li className="flex items-start gap-2" data-testid={`obs-sessions-replay-frontend-${note.envId}-${note.frontend}`} data-state={note.kind}>
      <Status tone={NOTE_TONE[note.kind]} size="sm" className="mt-1.5 shrink-0" />
      <span>
        {note.kind === "on" ? (
          <>
            Replay is on for {fe} in {env} ({samplePercent(note.sampleRate)} of sessions); no replays recorded in this range yet.
          </>
        ) : note.kind === "off" ? (
          <>
            Replay is off for {fe} in {env} — set {telemetry} on that frontend and redeploy.
          </>
        ) : note.kind === "build-flag" ? (
          <>
            Replay is on for {fe} in {env} via the deprecated build flag — move it to {telemetry} on that frontend.
          </>
        ) : note.kind === "blocked" ? (
          <>
            {fe} records replays but {env}&apos;s observability policy drops them. The environment has to opt in to replay: newer forge derives
            that from the frontend&apos;s <code className={CODE}>telemetry.replay</code>, so upgrade forge and redeploy.
          </>
        ) : (
          <>
            The console couldn&apos;t tell whether replay is on for {fe} in {env}.
          </>
        )}
      </span>
    </li>
  );
}

/** The generic how-to: what the console says about a frontend whose replay state it cannot read. */
function HedgedReplaySteps({ lead }: { lead: string }) {
  return (
    <>
      <p>{lead}</p>
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
    </>
  );
}

/**
 * The first page is empty and nothing narrows it: no replay was recorded in
 * the window. With `status` (ListFrontendReplay, reconciled in
 * replayStatus.ts) it says, per frontend in the selected environments, whether
 * replay is on and the one change that turns it on. Without it — still
 * loading, a failed read, an older control plane, no frontend found — the
 * console cannot know, so it says how to turn replay on without claiming it
 * is off; likewise for any frontend or environment `status` marks unknown.
 */
export function SessionsReplayOff({ range, onWiden, status }: { range: RangePreset; onWiden?: () => void; status?: ReplayStatus | null }) {
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
      <div className="max-w-2xl space-y-1.5" data-testid="obs-sessions-replay-state" data-known={status ? "true" : "false"}>
        {status ? (
          <>
            <p>Sessions lists browser sessions that have a replay, and every replay is masked.</p>
            <ul className="space-y-1" data-testid="obs-sessions-replay-frontends">
              {status.frontends.map((note) => (
                <FrontendReplayLine key={`${note.envId}/${note.frontend}`} note={note} />
              ))}
              {status.unavailableEnvs.map((env) => (
                <li key={env.id} className="flex items-start gap-2" data-testid={`obs-sessions-replay-unavailable-${env.id}`} data-state="unavailable">
                  <Status tone="neutral" size="sm" className="mt-1.5 shrink-0" />
                  <span>
                    <span className="font-medium text-ink">{env.name}</span>&apos;s frontend config couldn&apos;t be read, so whether its frontends record
                    replays is unknown.
                  </span>
                </li>
              ))}
            </ul>
            {status.hedged && <HedgedReplaySteps lead="Where it isn't known, turn on both switches if replay isn't on yet, then redeploy:" />}
          </>
        ) : (
          <HedgedReplaySteps lead="Sessions lists browser sessions that have a replay. Replay is off by default, and every replay is masked when it is on. If replay isn't on yet, turn on both switches, then redeploy:" />
        )}
        <p>Browser spans still arrive in Traces and browser errors in Errors without replay.</p>
      </div>
    </SessionNote>
  );
}
