/**
 * Environments service — thin wrappers around the control-plane public
 * `controlplane.v1.DaemonService` + `BillingService`, plus the daemon
 * personal-access-token (`reliant.v1.DaemonTokenService`) surface.
 *
 * This is the data layer for the in-app Settings → Environments section
 * (`components/Settings/cloud/environments.tsx`), which ports admin-web's
 * workspaces management into reliant-web using ONLY public RPCs.
 *
 * We intentionally REUSE the existing `./daemon` module (re-exported below)
 * for `listDaemons` / the `Daemon` type / status constants rather than
 * duplicating them — onboarding (ComputeStep, DaemonConnectingGate) already
 * depends on that module. This file only ADDS the create/get/update/lifecycle
 * + port-access + token RPCs the section needs on top.
 *
 * Two transports are in play, matching the rest of the app:
 *   - Daemon lifecycle + billing → `getControlPlaneClient(...)`
 *     (controlplane.v1 public, dials admin-server).
 *   - Access tokens → `grpcClient.daemonToken()` (reliant.v1, routed to the
 *     control-plane admin-server's compat adapter when cloud is configured;
 *     same client `useDaemonStatus` uses for the registry).
 */
import { ConnectError } from "@connectrpc/connect";

import { grpcClient } from "@/api/grpc-client";
import { DaemonService } from "@/gen/controlplane/v1/public/daemon_service_pb";
import { BillingService } from "@/gen/controlplane/v1/public/billing_service_pb";
import {
  DaemonSize,
  DaemonStatus,
  DaemonType,
  PortAccessMode,
  type Daemon,
  type PortAccessRule,
  type Subscription,
} from "@/gen/controlplane/v1/public/shared_pb";
import type { DaemonTokenInfo } from "@/gen/reliant/v1/daemon_token_pb";
import { getControlPlaneClient } from "./client";

// Re-export the shared daemon surface so the section imports everything from
// one module. `listDaemons`, the `Daemon` type and the DAEMON_STATUS_*
// constants live in ./daemon and are reused as-is.
export {
  listDaemons,
  hasActiveDaemon,
  getDaemonStatusMessage,
  DAEMON_STATUS_PENDING,
  DAEMON_STATUS_ACTIVE,
  DAEMON_STATUS_SUSPENDED,
  DAEMON_STATUS_DISCONNECTED,
  DAEMON_STATUS_FAILED,
  type Daemon,
} from "./daemon";

export { DaemonSize, DaemonStatus, DaemonType, PortAccessMode };
export type { PortAccessRule, Subscription, DaemonTokenInfo };

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
    // Repo cloning is a follow-up (GitCredentialService.CloneRepo); the field
    // is accepted here but not yet auto-cloned, mirroring admin-web.
    gitRepo: args.gitRepo ?? "",
    gitBranch: args.gitBranch ?? "",
  });
  return res.daemon;
}

// Canonical CPU/memory pairs per size — mirrors admin-web's detail-page map so
// an Update keeps the daemon's resources consistent with its size tier.
const SIZE_RESOURCES: Record<number, { cpu: string; memory: string }> = {
  [DaemonSize.SMALL]: { cpu: "1", memory: "2Gi" },
  [DaemonSize.MEDIUM]: { cpu: "2", memory: "4Gi" },
  [DaemonSize.LARGE]: { cpu: "4", memory: "8Gi" },
  [DaemonSize.XL]: { cpu: "8", memory: "16Gi" },
};

export async function updateEnvironment(args: {
  daemonId: string;
  newName: string;
  size: DaemonSize;
}): Promise<void> {
  const res = SIZE_RESOURCES[args.size] ?? SIZE_RESOURCES[DaemonSize.MEDIUM];
  await getControlPlaneClient(DaemonService).updateDaemon({
    daemonId: args.daemonId,
    newName: args.newName,
    resources: {
      cpuRequest: res.cpu,
      cpuLimit: res.cpu,
      memoryRequest: res.memory,
      memoryLimit: res.memory,
    },
  });
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

// ── Access tokens (reliant.v1.DaemonTokenService) ───────────────────────────

export async function listDaemonTokens(): Promise<DaemonTokenInfo[]> {
  const res = await grpcClient.daemonToken().listDaemonTokens({});
  return res.tokens;
}

export interface CreateDaemonTokenResult {
  /** Raw token — shown exactly once. */
  token: string;
  tokenId: string;
}

export async function createDaemonToken(name: string): Promise<CreateDaemonTokenResult> {
  const res = await grpcClient.daemonToken().createDaemonToken({ name });
  return { token: res.token, tokenId: res.tokenId };
}

export async function revokeDaemonToken(tokenId: string): Promise<void> {
  await grpcClient.daemonToken().revokeDaemonToken({ tokenId });
}
