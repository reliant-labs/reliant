// Copyright (c) 2026 Reliant Labs

/**
 * What the empty Sessions list says about each frontend's replay: the
 * server's verdict (ListFrontendReplay) reconciled with the replays that
 * actually arrived. Pure, so every state is tested once, here.
 *
 * The server reads a frontend's runtime config, which cannot hold the
 * BUILD flag (VITE_OTEL_REPLAY / NEXT_PUBLIC_OTEL_REPLAY): a frontend
 * switched on that way reads "off" while its replays still arrive. So an
 * "off" frontend with a replay in the evidence window is on via the build
 * flag, and an "off" the evidence cannot vouch for — it could not be read, or
 * the read stopped before the end of the window — is unknown, never "off".
 */

import type { FrontendReplay, FrontendReplayList } from "@/services/controlPlane/observability/frontendReplayApi";
import type { SessionSummary } from "@/services/controlPlane/observability/rumTypes";

/**
 *   on          on, and the environment ingests it: nothing recorded yet.
 *   off         its config does not turn replay on, and no replay arrived.
 *   build-flag  its config says off, but replays arrived: the build flag.
 *   blocked     it records, and the environment's policy drops what it sends.
 *   unknown     nothing can be said: the console falls back to how to turn it on.
 */
export type FrontendReplayNoteKind = "on" | "off" | "build-flag" | "blocked" | "unknown";

export interface FrontendReplayNote {
  kind: FrontendReplayNoteKind;
  envId: string;
  envName: string;
  frontend: string;
  sampleRate: number;
}

export interface ReplayStatus {
  frontends: FrontendReplayNote[];
  /** Environments whose frontends' config could not be read: unknown, never "off". */
  unavailableEnvs: { id: string; name: string }[];
  /** Something is unknown: the generic how-to-turn-it-on stays beside the per-frontend lines. */
  hedged: boolean;
}

/** The replays observed in the evidence window, and whether that is all of them. */
export interface ReplayEvidence {
  sessions: readonly Pick<SessionSummary, "envId" | "service">[];
  /** False: the read stopped early, so a frontend with no replay found may still have one. */
  complete: boolean;
}

/**
 * Whether a session's telemetry service is this frontend's: forge names it
 * `<project>-<frontend>` (naming.FrontendServiceName), and a frontend that
 * starts the SDK itself may report the bare name.
 */
export function servesFrontend(service: string, frontend: string): boolean {
  return service === frontend || service.endsWith(`-${frontend}`);
}

/** The row of `rows` (one environment's) a session belongs to: the longest frontend name its service ends with. */
function rowOfSession(rows: readonly FrontendReplay[], session: Pick<SessionSummary, "envId" | "service">): FrontendReplay | undefined {
  let best: FrontendReplay | undefined;
  for (const row of rows) {
    if (row.envId !== session.envId || !servesFrontend(session.service, row.frontend)) continue;
    if (!best || row.frontend.length > best.frontend.length) best = row;
  }
  return best;
}

/** "25%", "0.5%", "100%". */
export function samplePercent(rate: number): string {
  const pct = Math.round(Math.min(1, Math.max(0, rate)) * 1000) / 10;
  return `${pct}%`;
}

/**
 * The per-frontend status, or null when there is nothing to say per frontend
 * (no rows and no unreadable environment): the generic note stands alone.
 *
 * `evidence` is the replays observed for the frontends that read "off", or
 * null when that could not be read. An "off" frontend with no replay found is
 * "off" only when the evidence is complete; otherwise it is unknown.
 */
export function replayStatus(
  list: FrontendReplayList,
  evidence: ReplayEvidence | null,
  envName: (id: string) => string | undefined = () => undefined
): ReplayStatus | null {
  const observed = new Set<FrontendReplay>();
  for (const session of evidence?.sessions ?? []) {
    const row = rowOfSession(list.frontends, session);
    if (row) observed.add(row);
  }
  const frontends = list.frontends.map((row): FrontendReplayNote => {
    const kind: FrontendReplayNoteKind =
      row.state !== "off" ? row.state : observed.has(row) ? "build-flag" : evidence?.complete ? "off" : "unknown";
    return { kind, envId: row.envId, envName: row.envName || envName(row.envId) || row.envId, frontend: row.frontend, sampleRate: row.sampleRate };
  });
  const unavailableEnvs = list.unavailableEnvIds.map((id) => ({ id, name: envName(id) || id }));
  if (!frontends.length && !unavailableEnvs.length) return null;
  return { frontends, unavailableEnvs, hedged: unavailableEnvs.length > 0 || frontends.some((f) => f.kind === "unknown") };
}

/** The environments with a frontend that reads "off": where the console looks for build-flag replays. */
export function offEnvIds(list: FrontendReplayList): string[] {
  return [...new Set(list.frontends.filter((f) => f.state === "off").map((f) => f.envId))];
}
