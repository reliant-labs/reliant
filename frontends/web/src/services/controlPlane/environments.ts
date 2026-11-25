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
 * (controlplane.v1 public, dials platform-api). Each takes an optional
 * trailing `transport`, a TransportScope's (lib/transportScope.tsx), as
 * getControlPlaneClient does; none is the app's. Access tokens are not here:
 * they are reliant.v1.TokenService on forge's generated hooks
 * (`hooks/token-queries.ts`).
 */
import { ConnectError, type Transport } from "@connectrpc/connect";

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
// GetDaemon, ListPortAccessRules, DeleteDaemon, SetDefaultPortAccess and the
// single-call writes run on DaemonService's generated hooks
// (hooks/controlplane/daemon-service-hooks_gen.ts). What stays here is called
// outside a single mutation: the restart/resize/grow choreography
// (Settings › Machines) and the chip's set-or-remove toggle (DetectedPortsChip).

export interface CreateEnvironmentArgs {
  name: string;
  size: DaemonSize;
  /** Duration string, e.g. "30m". */
  idleTimeout?: string;
  gitRepo?: string;
  gitBranch?: string;
}

export async function createEnvironment(args: CreateEnvironmentArgs, transport?: Transport): Promise<Daemon | undefined> {
  const res = await getControlPlaneClient(DaemonService, transport).createDaemon({
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
export async function resizeDaemon(daemonId: string, size: DaemonSize, transport?: Transport): Promise<void> {
  await getControlPlaneClient(DaemonService, transport).resizeDaemon({ daemonId, size });
}

/**
 * Grows a cloud machine's disk to `storageGib` GiB of data (control-plane
 * docs/design/workspace-disk-resize.md). The target is absolute, not a delta,
 * so a retry is safe. Unlike a resize this needs no stop: the cloud disk grows
 * at once, running or suspended. A running machine USES the space only after
 * its next restart, which is why the detail view offers to restart it.
 */
export async function growDaemonDisk(daemonId: string, storageGib: number, transport?: Transport): Promise<Daemon | undefined> {
  const res = await getControlPlaneClient(DaemonService, transport).growDaemonDisk({ daemonId, storageGib });
  return res.daemon;
}

export async function suspendDaemon(daemonId: string, transport?: Transport): Promise<void> {
  await getControlPlaneClient(DaemonService, transport).suspendDaemon({ daemonId });
}

export async function resumeEnvironment(daemonId: string, transport?: Transport): Promise<void> {
  await getControlPlaneClient(DaemonService, transport).resumeDaemon({ daemonId });
}

// ── Port access ─────────────────────────────────────────────────────────────

export interface SetPortAccessResult {
  rule: PortAccessRule | undefined;
  /** Raw access token, returned exactly once for TOKEN-mode rules. */
  accessToken: string;
}

export async function setPortAccess(args: {
  daemonId: string;
  port: number;
  accessMode: PortAccessMode;
}, transport?: Transport): Promise<SetPortAccessResult> {
  const res = await getControlPlaneClient(DaemonService, transport).setPortAccess(args);
  return { rule: res.rule, accessToken: res.accessToken };
}

export async function removePortAccess(daemonId: string, port: number, transport?: Transport): Promise<void> {
  await getControlPlaneClient(DaemonService, transport).removePortAccess({ daemonId, port });
}

// ── Compute subscription (plan-gated size picker) ───────────────────────────

export async function getComputeSubscription(transport?: Transport): Promise<Subscription | null> {
  const res = await getControlPlaneClient(BillingService, transport).getCurrentUserComputeSubscription({});
  return res.subscription ?? null;
}
