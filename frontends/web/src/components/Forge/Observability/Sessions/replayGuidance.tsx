// Copyright (c) 2026 Reliant Labs

/**
 * The one place the Sessions notes name forge's replay switches. They name
 * only switches the forge this console pins actually has: a note that says
 * "set telemetry.replay" against a forge without it sends the reader to a
 * field that fails to compile.
 *
 *   "build-flag"  forge as pinned today: the frontend's build flag
 *                 (VITE_OTEL_REPLAY / NEXT_PUBLIC_OTEL_REPLAY) plus the
 *                 environment's ingest opt-in (ObservabilitySignals.replay).
 *   "telemetry"   forge with forge#1983: forge.BrowserTelemetry.replay on the
 *                 frontend, from which forge derives the environment's opt-in;
 *                 the build flag is deprecated.
 */

import type { ReactNode } from "react";

export type ReplaySwitch = "build-flag" | "telemetry";

// Flip to "telemetry" once forge.yaml pins a forge with forge.BrowserTelemetry.replay (forge#1983).
export const REPLAY_SWITCH: ReplaySwitch = "build-flag";

const CODE = "font-mono text-ink";

const BUILD_FLAG = (
  <>
    the build flag (<code className={CODE}>VITE_OTEL_REPLAY=true</code> for Vite, <code className={CODE}>NEXT_PUBLIC_OTEL_REPLAY=true</code> for Next.js)
  </>
);
const SIGNALS_OPT_IN = <code className={CODE}>signals = forge.ObservabilitySignals {"{replay = True}"}</code>;
const TELEMETRY_REPLAY = <code className={CODE}>telemetry = forge.BrowserTelemetry {"{replay = True}"}</code>;

/** After "Replay is off for `web` in prod — ": what turns it on. */
export function ReplayOffFix({ env, mode = REPLAY_SWITCH }: { env: ReactNode; mode?: ReplaySwitch }) {
  return mode === "telemetry" ? (
    <>set {TELEMETRY_REPLAY} on that frontend and redeploy.</>
  ) : (
    <>
      set {BUILD_FLAG} on that frontend and {SIGNALS_OPT_IN} in {env}&apos;s <code className={CODE}>forge.Observability</code>, then redeploy.
    </>
  );
}

/** After "Replay is on for `web` in prod ": how, when its config says off but replays arrive. */
export function ReplayBuildFlagNote({ mode = REPLAY_SWITCH }: { mode?: ReplaySwitch }) {
  return mode === "telemetry" ? (
    <>via the deprecated build flag — move it to {TELEMETRY_REPLAY} on that frontend.</>
  ) : (
    <>via its build flag.</>
  );
}

/** After "`web` records replays but prod's observability policy drops them. ": the fix. */
export function ReplayBlockedFix({ env, mode = REPLAY_SWITCH }: { env: ReactNode; mode?: ReplaySwitch }) {
  return mode === "telemetry" ? (
    <>
      The environment has to opt in to replay: newer forge derives that from the frontend&apos;s <code className={CODE}>telemetry.replay</code>, so
      upgrade forge and redeploy.
    </>
  ) : (
    <>
      The environment has to opt in to replay: set {SIGNALS_OPT_IN} in {env}&apos;s <code className={CODE}>forge.Observability</code>, then redeploy.
    </>
  );
}

/** How the generic how-to's lead names the steps that follow it. */
export function turnOnPhrase(mode: ReplaySwitch = REPLAY_SWITCH): string {
  return mode === "telemetry" ? "turn it on" : "turn on both switches";
}

/** The generic how-to, for a frontend whose replay state the console cannot read. */
export function ReplayTurnOnSteps({ mode = REPLAY_SWITCH }: { mode?: ReplaySwitch }) {
  return (
    <ol className="list-decimal space-y-1 pl-4" data-testid="obs-sessions-replay-off-steps">
      {mode === "telemetry" ? (
        <li>
          The frontend&apos;s KCL: {TELEMETRY_REPLAY}.
        </li>
      ) : (
        <>
          <li>
            The browser build flag: <code className={CODE}>VITE_OTEL_REPLAY=true</code> for Vite, <code className={CODE}>NEXT_PUBLIC_OTEL_REPLAY=true</code> for
            Next.js.
          </li>
          <li>
            The environment&apos;s <code className={CODE}>forge.Observability</code> in KCL: {SIGNALS_OPT_IN}.
          </li>
        </>
      )}
    </ol>
  );
}
