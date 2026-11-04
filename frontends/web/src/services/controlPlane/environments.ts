/**
 * Environments service — thin wrappers around the control-plane public
 * `controlplane.v1.DaemonService` + `BillingService`.
 *
 * This is the data layer for the in-app Settings → Environments section
 * (`components/Settings/cloud/machines.tsx`), which ports admin-web's
 * workspaces management into reliant-web using ONLY public RPCs.
 *
 * We intentionally REUSE the existing `./daemon` module (re-exported below)
 * for `listDaemons` / the `Daemon` type / status constants rather than
 * duplicating them — onboarding (ComputeStep, DaemonConnectingGate) already
 * depends on that module. This file only ADDS the create/get/update/lifecycle
 * + port-access RPCs the section needs on top.
 *
 * Daemon lifecycle + billing go through `getControlPlaneClient(...)`
 * (controlplane.v1 public, dials admin-server). Access tokens are not here:
 * they are reliant.v1.TokenService on forge's generated hooks
 * (`hooks/token-queries.ts`).
 */
import { ConnectError } from "@connectrpc/connect";

import { DaemonService } from "@/gen/controlplane/services/daemon/v1/daemon_pb";
import { BillingService } from "@/gen/controlplane/services/billing/v1/billing_pb";
import {
  DaemonSize,
  DaemonStatus,
  DaemonType,
  PortAccessMode,
  type Daemon,
  type PortAccessRule,
  type Subscription,
} from "@/gen/controlplane/controlplane/v1/shared_pb";
import { getControlPlaneClient } from "./client";

// Re-export the shared daemon surface so the section imports everything from
// one module. `listDaemons`, the `Daemon` type and the DAEMON_STATUS_*
// constants live in ./daemon and are reused as-is.
export {
  DAEMON_STATUS_PENDING,
  DAEMON_STATUS_ACTIVE,
  DAEMON_STATUS_SUSPENDED,
  DAEMON_STATUS_DISCONNECTED,
  DAEMON_STATUS_FAILED,
  type Daemon,
} from "./daemon";

export { DaemonSize, DaemonStatus, DaemonType, PortAccessMode };
export type { PortAccessRule, Subscription };

// ── Error helper ────────────────────────────────────────────────────────────
// reliant has no shared `mapServerError` (admin-web's lib), so extract a
// user-readable message from a ConnectError (or any thrown value) here.
export function describeError(err: unknown, fallback = "Something went wrong"): string {
  if (err instanceof ConnectError) return err.message.replace(/^\[[^\]]+\]\s*/, "");
  if (err instanceof Error && err.message) return err.message;
  return fallback;
}

// ── Daemon lifecycle (controlplane.v1.DaemonService) ────────────────────────

export interface GetDaemonResult {
  daemon: Daemon | undefined;
  workspaceBaseDomain: string;
}

export async function getDaemon(daemonId: string): Promise<GetDaemonResult> {
  const res = await getControlPlaneClient(DaemonService).getDaemon({ daemonId });
  return { daemon: res.daemon, workspaceBaseDomain: res.workspaceBaseDomain };
}

export interface CreateEnvironmentArgs {
  name: string;
  size: DaemonSize;
  /** Duration string, e.g. "30m". */
  idleTimeout?: string;
  gitRepo?: string;
  gitBranch?: string;
}

export async function createEnvironment(args: CreateEnvironmentArgs): Promise<Daemon | undefined> {
  const res = await getControlPlaneClient(DaemonService).createDaemon({
    name: args.name,
    daemonType: DaemonType.MANAGED,
    size: args.size,
    idleTimeout: args.idleTimeout ?? "",
    // A record of which repo the machine was made for — nothing clones from
    // it. The clone is CreateProjectFromRepo; see Settings/cloud/createMachine.
    gitRepo: args.gitRepo ?? "",
    gitBranch: args.gitBranch ?? "",
  });
  return res.daemon;
}

/**
 * Changes a cloud machine's size. The control plane accepts this only while
 * the machine is SUSPENDED, and the new size takes effect when it next starts
 * (control-plane docs/design/daemon-resize.md); resizing a running machine is
 * stop → resize → start, which `restartMachine`'s `resize` step drives.
 */
export async function resizeDaemon(daemonId: string, size: DaemonSize): Promise<void> {
  await getControlPlaneClient(DaemonService).resizeDaemon({ daemonId, size });
}

/**
 * Grows a cloud machine's disk to `storageGib` GiB of data (control-plane
 * docs/design/workspace-disk-resize.md). The target is absolute, not a delta,
 * so a retry is safe. Unlike a resize this needs no stop: the cloud disk grows
 * at once, running or suspended. A running machine USES the space only after
 * its next restart, which is why the detail view offers to restart it.
 */
export async function growDaemonDisk(daemonId: string, storageGib: number): Promise<Daemon | undefined> {
  const res = await getControlPlaneClient(DaemonService).growDaemonDisk({ daemonId, storageGib });
  return res.daemon;
}

export async function suspendDaemon(daemonId: string): Promise<void> {
  await getControlPlaneClient(DaemonService).suspendDaemon({ daemonId });
}

export async function resumeEnvironment(daemonId: string): Promise<void> {
  await getControlPlaneClient(DaemonService).resumeDaemon({ daemonId });
}

export async function deleteDaemon(daemonId: string): Promise<void> {
  await getControlPlaneClient(DaemonService).deleteDaemon({ daemonId });
}

// ── Port access ─────────────────────────────────────────────────────────────

// Shared React Query key for a daemon's port-access rules. Exported so every
// surface that reads or mutates rules (the Settings → Environments panel AND
// the header DetectedPortsChip one-click-public toggle) keys the same cache
// entry — a "Make public" in the chip invalidates the panel and vice-versa.
export const portAccessRulesQueryKey = (daemonId: string) =>
  ["cp", "environments", "ports", daemonId] as const;

export async function listPortAccessRules(daemonId: string): Promise<PortAccessRule[]> {
  const res = await getControlPlaneClient(DaemonService).listPortAccessRules({ daemonId });
  return res.rules;
}

export interface SetPortAccessResult {
  rule: PortAccessRule | undefined;
  /** Raw access token, returned exactly once for TOKEN-mode rules. */
  accessToken: string;
}

export async function setPortAccess(args: {
  daemonId: string;
  port: number;
  accessMode: PortAccessMode;
}): Promise<SetPortAccessResult> {
  const res = await getControlPlaneClient(DaemonService).setPortAccess(args);
  return { rule: res.rule, accessToken: res.accessToken };
}

export async function removePortAccess(daemonId: string, port: number): Promise<void> {
  await getControlPlaneClient(DaemonService).removePortAccess({ daemonId, port });
}

// ── Default port access (workspace-level) ────────────────────────────────────
// The policy the preview proxy applies to a listening port that has NO explicit
// per-port rule. AUTHENTICATED (safe: only the owner) is the global default;
// PUBLIC opts the whole workspace into zero-click sharing (every unruled port
// reachable by URL). Explicit per-port rules always override this. Read the
// current value from getDaemon(...).daemon.defaultPortAccess.
export async function setDefaultPortAccess(args: {
  daemonId: string;
  defaultAccessMode: PortAccessMode;
}): Promise<Daemon | undefined> {
  const res = await getControlPlaneClient(DaemonService).setDefaultPortAccess(args);
  return res.daemon;
}

// ── Compute subscription (plan-gated size picker) ───────────────────────────

export async function getComputeSubscription(): Promise<Subscription | null> {
  const res = await getControlPlaneClient(BillingService).getCurrentUserComputeSubscription({});
  return res.subscription ?? null;
}
