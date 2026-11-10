// Copyright (c) 2026 Reliant Labs

/**
 * The one place the Sessions notes name forge's replay switches. They name
 * only switches the forge this console pins actually has: a note that says
 * "set telemetry.replay" against a forge without it sends the reader to a
 * field that fails to compile.
 *
 *   "runtime-config"  forge as pinned today: the frontend's runtime config
 *                 (forge.Frontend.runtime_config.OTEL_REPLAY = "true"; the
 *                 scaffolded deploy KCL writes it from `_session_replay`) plus
 *                 the environment's ingest opt-in (ObservabilitySignals.replay).
 *                 The build flag (VITE_OTEL_REPLAY / NEXT_PUBLIC_OTEL_REPLAY)
 *                 is deprecated and read only when the runtime config does not
 *                 set OTEL_REPLAY, so it never overrides an explicit "false":
 *                 the notes never offer it as the way to turn replay on.
 *   "telemetry"   forge with forge#1983: forge.BrowserTelemetry.replay on the
 *                 frontend, from which forge derives the environment's opt-in;
 *                 the build flag is deprecated.
 */

import type { ReactNode } from "react";

export type ReplaySwitch = "runtime-config" | "telemetry";

// Flip to "telemetry" once forge.yaml pins a forge with forge.BrowserTelemetry.replay (forge#1983).
export const REPLAY_SWITCH: ReplaySwitch = "runtime-config";

const CODE = "font-mono text-ink";

const RUNTIME_REPLAY = <code className={CODE}>runtime_config = {"{OTEL_REPLAY = \"true\"}"}</code>;
// The scaffolded deploy/kcl/<env>/main.k derives both switches from this one line.
const SCAFFOLD_SWITCH = <code className={CODE}>_session_replay = True</code>;
const SIGNALS_OPT_IN = <code className={CODE}>signals = forge.ObservabilitySignals {"{replay = True}"}</code>;
const TELEMETRY_REPLAY = <code className={CODE}>telemetry = forge.BrowserTelemetry {"{replay = True}"}</code>;

/** After "Replay is off for `web` in prod — ": what turns it on. */
export function ReplayOffFix({ env, mode = REPLAY_SWITCH }: { env: ReactNode; mode?: ReplaySwitch }) {
  return mode === "telemetry" ? (
    <>set {TELEMETRY_REPLAY} on that frontend and redeploy.</>
  ) : (
    <>
      set {RUNTIME_REPLAY} on that frontend&apos;s <code className={CODE}>forge.Frontend</code> and {SIGNALS_OPT_IN} in {env}&apos;s{" "}
      <code className={CODE}>forge.Observability</code> (a scaffolded {env} sets both from {SCAFFOLD_SWITCH}), then redeploy.
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
            The frontend&apos;s <code className={CODE}>forge.Frontend</code> in KCL: {RUNTIME_REPLAY}.
          </li>
          <li>
            The environment&apos;s <code className={CODE}>forge.Observability</code> in KCL: {SIGNALS_OPT_IN}.
          </li>
          <li>
            A scaffolded environment sets both from one line: {SCAFFOLD_SWITCH}.
          </li>
        </>
      )}
    </ol>
  );
}
