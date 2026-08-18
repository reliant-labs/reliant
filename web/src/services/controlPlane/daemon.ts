/**
 * Cloud-only thin wrappers around `controlplane.v1.DaemonService`.
 *
 * Cloud onboarding (ComputeStep, GitHubConnectStep, DaemonConnectingGate) and
 * the React-Query hooks in `useOnboardingQueries.ts` consume these. Each
 * function delegates to a typed Connect-Web client; request shapes match the
 * generated TS types and DO NOT send snake_case duplicates.
 *
 * Status constants are re-exported from the generated `DaemonStatus` enum so
 * callers don't have to import the enum AND a numeric constant for the same
 * value.
 */

import { DaemonService } from "@/gen/controlplane/v1/public/daemon_service_pb";
import {
  type Daemon as ProtoDaemon,
  DaemonStatus,
} from "@/gen/controlplane/v1/public/shared_pb";
import { getControlPlaneClient } from "./client";

// Re-exports so call sites can `import { DAEMON_STATUS_ACTIVE } from
// "@/services/controlPlane/daemon"` without also importing the enum.
export const DAEMON_STATUS_PENDING = DaemonStatus.PENDING;
export const DAEMON_STATUS_ACTIVE = DaemonStatus.ACTIVE;
export const DAEMON_STATUS_SUSPENDED = DaemonStatus.SUSPENDED;
export const DAEMON_STATUS_DISCONNECTED = DaemonStatus.DISCONNECTED;
export const DAEMON_STATUS_FAILED = DaemonStatus.FAILED;

export type Daemon = ProtoDaemon;

export async function listDaemons(): Promise<{ daemons: Daemon[] }> {
  const res = await getControlPlaneClient(DaemonService).listDaemons({});
  return { daemons: res.daemons };
}

export interface CreateDaemonArgs {
  name: string;
  // Numeric enum values from `DaemonType` (1 = MANAGED) and `DaemonSize`
  // (1 = SMALL). Kept as `number` so callers don't have to import the enum
  // just to construct a "managed small" daemon — Connect/protobuf-es accepts
  // the raw enum number on the wire.
  daemonType: number;
  size: number;
  idleTimeout?: string;
  gitRepo?: string;
  gitBranch?: string;
}

/**
 * Creates a daemon and returns its id.
 *
 * The id matters to callers that must then wait for the machine: it only
 * becomes usable once it registers with reliant, and identifying it by
 * "whichever row is new" picks up unrelated daemons that happen to appear (or
 * pre-existing ones the caller had not seen). Returns "" when the server
 * declines to name it, so callers must handle that rather than assume.
 */
export async function createDaemon(args: CreateDaemonArgs): Promise<string> {
  const res = await getControlPlaneClient(DaemonService).createDaemon({
    name: args.name,
    daemonType: args.daemonType,
    size: args.size,
    idleTimeout: args.idleTimeout ?? "",
    gitRepo: args.gitRepo ?? "",
    gitBranch: args.gitBranch ?? "",
  });
  return res.daemon?.id ?? "";
}

export async function resumeDaemon(daemonId: string): Promise<void> {
  if (!daemonId) throw new Error("Cannot resume daemon: no daemon id available");
  await getControlPlaneClient(DaemonService).resumeDaemon({ daemonId });
}

export async function suspendDaemon(daemonId: string): Promise<void> {
  if (!daemonId) throw new Error("Cannot suspend daemon: no daemon id available");
  await getControlPlaneClient(DaemonService).suspendDaemon({ daemonId });
}

export async function deleteDaemon(daemonId: string): Promise<void> {
  if (!daemonId) throw new Error("Cannot delete daemon: no daemon id available");
  await getControlPlaneClient(DaemonService).deleteDaemon({ daemonId });
}

// ── Helpers used by ComputeStep / GitHubConnectStep ─────────────

/** Active in cloud-land = control-plane DaemonStatus.ACTIVE (=2). */
export function hasActiveDaemon(daemons: Daemon[]): boolean {
  return daemons.some((d) => d.status === DaemonStatus.ACTIVE);
}

/** Optional human-readable status detail surfaced by the gateway/controller
 *  on a connect failure (e.g. "image pull failed"). Empty when the daemon is
 *  actively connected. */
export function getDaemonStatusMessage(daemon: Daemon | undefined): string {
  if (!daemon) return "";
  return daemon.lastStatusMessage || "";
}
