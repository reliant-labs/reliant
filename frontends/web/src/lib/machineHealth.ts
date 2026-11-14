// Copyright (c) 2026 Reliant Labs

/**
 * Whether a connected machine can do work, as its daemon reports it
 * (internal/forkwatch → DaemonInfo.health; reliant#1919).
 *
 * `status` says whether a machine is connected; `health` says whether it can
 * start a process. They are separate fields so a client that switches on
 * status keeps working: a machine whose process table is full stays ACTIVE —
 * attached and answering — while every command it is asked to run fails.
 * Before this, the UI showed it as a healthy, connected machine for the 24
 * minutes it took its owner to restart it by hand.
 */

import { DaemonStatus, type DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import { MachineHealthState } from "@/gen/reliant/v1/tools_daemon_pb";

/** Shown when a degraded machine's daemon gave no reason of its own. */
const FALLBACK_REASON = "This machine is connected but cannot start new processes, so commands on it fail.";

/**
 * Why a connected machine cannot do work, or null when it can (or when it is
 * not connected, which its status already explains). Unset health — a daemon
 * that predates the report — is healthy.
 */
export function degradedReason(daemon: Pick<DaemonInfo, "status" | "health"> | null | undefined): string | null {
  if (!daemon) return null;
  if (daemon.status !== DaemonStatus.ACTIVE && daemon.status !== DaemonStatus.IDLE) return null;
  if (daemon.health?.state !== MachineHealthState.DEGRADED) return null;
  return daemon.health.reason.trim() || FALLBACK_REASON;
}

/**
 * The heartbeat's `health_state` ("ok" | "degraded") disagrees with what the
 * cached daemon list says, so the list is stale. A heartbeat without the key
 * (a gateway that predates it) never disagrees.
 */
export function heartbeatHealthDisagrees(
  daemon: Pick<DaemonInfo, "status" | "health"> | null | undefined,
  heartbeatHealth: unknown,
): boolean {
  if (heartbeatHealth !== "ok" && heartbeatHealth !== "degraded") return false;
  const cachedDegraded = daemon?.health?.state === MachineHealthState.DEGRADED;
  return cachedDegraded !== (heartbeatHealth === "degraded");
}
