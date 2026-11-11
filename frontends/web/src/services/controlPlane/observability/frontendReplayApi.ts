// Copyright (c) 2026 Reliant Labs

/**
 * control-plane `ObservabilityService.ListFrontendReplay`: per environment and
 * per frontend, whether session replay is on — read from the frontend's live
 * runtime config (a hosted StaticSite's), else the one its current release
 * recorded, and judged against the environment's ingest policy. The only file
 * that knows its wire shape.
 *
 * It is a Postgres + hub read, not a ClickHouse one: it takes no read slot
 * (readSlots.ts NO_READ_SLOT_RPCS) and never queues behind the session list.
 *
 * What the server cannot see, and the console has to reconcile:
 *
 *  - a frontend still switched on by the deprecated BUILD flag
 *    (VITE_OTEL_REPLAY / NEXT_PUBLIC_OTEL_REPLAY) has no OTEL_REPLAY in its
 *    runtime config, so it reads "off" while its replays still arrive;
 *  - a self-hosted frontend the release names nowhere gets no row at all.
 */

import {
  FrontendReplaySource as WireSource,
  FrontendReplayState as WireState,
  ObservabilityService,
} from "@/gen/controlplane/services/observability/v1/observability_pb";

import { obsClient } from "./obsClient";

/**
 *   on       its runtime config turns replay on and the environment ingests it.
 *   off      its runtime config does not turn replay on.
 *   blocked  it records replays the environment's observability policy drops.
 *   unknown  the server could not tell (no readable config, or the policy was unreadable).
 */
export type FrontendReplayState = "on" | "off" | "blocked" | "unknown";

/** Where the runtime config was read: the hosted site's live spec, the deployed release, or nowhere. */
export type FrontendReplaySource = "live" | "release" | "none";

/** One frontend instance: a frontend in one environment. */
export interface FrontendReplay {
  envId: string;
  envName: string;
  /** The forge frontend name (`web`), not the telemetry service name (`<project>-web`). */
  frontend: string;
  state: FrontendReplayState;
  source: FrontendReplaySource;
  /** The share of sessions recorded, in [0, 1]. */
  sampleRate: number;
}

export interface FrontendReplayList {
  /** In the scope's environment order, then by frontend name. */
  frontends: FrontendReplay[];
  /** Environments whose facts could not be read. Not "no frontends": nothing is known about them. */
  unavailableEnvIds: string[];
}

export interface FrontendReplayRequest {
  orgId: string;
  /** Unset: every project. */
  projectId?: string;
  /** Empty: every environment the caller may read (of the project, when set). */
  envIds: string[];
}

const STATE_OF: Record<WireState, FrontendReplayState> = {
  [WireState.UNSPECIFIED]: "unknown",
  [WireState.ON]: "on",
  [WireState.OFF]: "off",
  [WireState.BLOCKED]: "blocked",
  [WireState.UNKNOWN]: "unknown",
};

const SOURCE_OF: Record<WireSource, FrontendReplaySource> = {
  [WireSource.UNSPECIFIED]: "none",
  [WireSource.LIVE]: "live",
  [WireSource.RELEASE]: "release",
  [WireSource.NONE]: "none",
};

/** The scope's frontends and their replay state. */
export async function listFrontendReplay(req: FrontendReplayRequest, options: { signal?: AbortSignal } = {}): Promise<FrontendReplayList> {
  const res = await obsClient(ObservabilityService).listFrontendReplay(
    { orgId: req.orgId, projectId: req.projectId || undefined, envIds: req.envIds },
    options
  );
  return {
    frontends: res.frontends.map((f) => ({
      envId: f.envId,
      envName: f.envName,
      frontend: f.frontend,
      // A state this build does not know (a newer server) is one it cannot explain.
      state: STATE_OF[f.state] ?? "unknown",
      source: SOURCE_OF[f.source] ?? "none",
      sampleRate: Number.isFinite(f.sampleRate) ? Math.min(1, Math.max(0, f.sampleRate)) : 1,
    })),
    unavailableEnvIds: [...res.unavailableEnvIds],
  };
}
