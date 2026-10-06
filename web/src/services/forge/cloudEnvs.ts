// Copyright (c) 2025 Reliant Labs

/**
 * The CONTROL PLANE's view of a forge project's environments.
 *
 * Sibling of topology.ts, and the reason it exists is the one property the
 * forge surface did not have: a HOSTED environment must be readable with no
 * daemon at all. topology.ts is forge's own report, and forge runs on the
 * user's daemon against their checkout — so before this module, a laptop that
 * was asleep blanked a production environment that the control plane was
 * observing the whole time. Here the browser asks control-plane's
 * DeployService directly, over the same transport the managed secret store
 * already uses (services/controlPlane/client.ts).
 *
 * THE DAEMON IS STILL THE SOURCE FOR EVERYTHING LOCAL — what `forge env up`
 * launched, a cluster forge deploys by kube context, forge's own declarations.
 * The two sources are joined in ./environments.ts by the forge project NAME
 * (forge.yaml `name`), which is also what forge sends as the environment's
 * `project` when it ensures one on the control plane.
 *
 * Plain, serialisable shapes rather than the generated messages, for the same
 * reason secretStore.ts converts: the view layer stays testable with object
 * literals, and every field is read EXPLICITLY — a field a newer control plane
 * adds is dropped here rather than spread into the view.
 *
 * ── WHAT THE WEB MUST NEVER CALL ────────────────────────────────────────────
 *
 * The control plane's local-secret pull RPC RETURNS SECRET VALUES. It is for
 * `forge env up`, which injects them into the processes it launches, in
 * memory. The browser has no use for a value and must hold none, so nothing in
 * this app imports that service — pinned by a test that scans the source tree
 * (services/forge/__tests__/noLocalSecretReads.test.ts).
 */

import { Code, ConnectError } from "@connectrpc/connect";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";

import {
  DeployEnvironmentKind,
  DeployObservedState,
  DeploySuspendReason,
  DeployRunState,
  DeployPromotionKind,
  DeployTier,
  DeployVerdict,
  type DeployEnvironment,
  type DeployPromotion,
} from "@/gen/controlplane/controlplane/v1/deploy_pb";
import {
  DeployService,
  type DeploymentStatus,
  type GetDeploymentStatusResponse,
} from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { getControlPlaneClient } from "@/services/controlPlane/client";
import { CONTROL_PLANE_API_URL } from "@/services/controlPlane/config";

import { hostedVerdictOf, type ForgeHostedWorkload, type HostedVerdict } from "./topology";

// ── Domain types ────────────────────────────────────────────────────────────

/**
 * What the control plane says an environment IS.
 *
 *   persistent  the platform runs its workloads (Reliant cloud).
 *   preview     the same, with an expiry (one per pull request).
 *   local       the workloads run on a developer machine via `forge env up`;
 *               the control plane is its SECRET STORE only, and never deploys,
 *               promotes or rolls it back. Immutable once created.
 *   unknown     a kind this build does not recognise. Never folded into
 *               `persistent` — that would offer cloud language (deployments,
 *               promotions) for an environment that may have neither.
 */
export type CloudEnvKind = "persistent" | "preview" | "local" | "unknown";

export interface CloudEnv {
  id: string;
  name: string;
  /** The forge project (forge.yaml `name`). Empty for an env ensured before projects existed. */
  project: string;
  kind: CloudEnvKind;
  createdAt?: string;
}

/** One artifact a promotion froze. Digest for an image; empty for a source pin. */
export interface CloudArtifact {
  name: string;
  digest: string;
}

export interface CloudPromotion {
  id: string;
  releaseVersion: string;
  kind: "promote" | "rollback" | "unknown";
  /** The env this release came FROM, or empty on a first deploy. */
  fromEnvironmentId: string;
  promotedByUserId: string;
  /** Automation ("ci", "preview-bot") when no human pressed the button. */
  promotedByActor: string;
  note: string;
  createdAt?: string;
  artifacts: CloudArtifact[];
}

export interface CloudEnvStatus {
  /** Worst of the deployments' verdicts. An environment with none is `unknown`. */
  verdict: HostedVerdict;
  /** One row per live deployment, in forge's hosted-workload shape so one renderer serves both. */
  workloads: ForgeHostedWorkload[];
  /** The release bound now: the newest promotion in the append-only ledger. Null before the first deploy. */
  currentPromotion: CloudPromotion | null;
  /** When the reconcile worker last refreshed the newest observation, if ever. */
  observedAt?: string;
}

// ── Availability ────────────────────────────────────────────────────────────

/**
 * Whether the control plane can answer for this project's environments.
 *
 * Five answers, because they call for different sentences — and because four
 * of them are NOT errors. A user who runs Reliant without a control plane, or
 * whose role in the organization may not read deploy state, simply sees no
 * cloud environments; painting that red would put an error banner in front of
 * every local-only forge project.
 *
 * There is no "deploy is not enabled for your organization" answer: the
 * control plane no longer gates the deploy product per organization, so every
 * org may deploy and a FailedPrecondition from a READ is not an entitlement
 * signal. It lands in `unreachable` with the server's own message as detail.
 *
 *   available         the control plane answered.
 *   no-control-plane  this build has none (no VITE_CONTROL_PLANE_API_URL).
 *   no-access         PermissionDenied: the caller's role in the organization
 *                     may not read deploy state (deploy reads need org admin).
 *   not-configured    Unimplemented: this control plane does not serve the
 *                     deploy domain (a deployment without its database, or one
 *                     that predates it).
 *   unreachable       anything else. The only genuinely bad state.
 */
export type CloudAvailability =
  | "available"
  | "no-control-plane"
  | "no-access"
  | "not-configured"
  | "unreachable";

export function cloudAvailabilityFromError(err: unknown): CloudAvailability {
  if (!CONTROL_PLANE_API_URL) return "no-control-plane";
  if (err instanceof ConnectError) {
    if (err.code === Code.PermissionDenied) return "no-access";
    if (err.code === Code.Unimplemented) return "not-configured";
  }
  return "unreachable";
}

/**
 * The control plane's own explanation, for display beside an unavailable
 * state. DeployService carries no secret material in any direction, so its
 * messages are safe to show — unlike SecretStoreService's, which the secrets
 * hooks never surface.
 */
export function cloudErrorDetail(err: unknown): string {
  if (err instanceof ConnectError) return err.rawMessage || err.message;
  if (err instanceof Error) return err.message;
  return "";
}

export function hasCloudControlPlane(): boolean {
  return !!CONTROL_PLANE_API_URL;
}

// ── Conversion ──────────────────────────────────────────────────────────────

function toISO(ts: Timestamp | undefined): string | undefined {
  if (!ts) return undefined;
  try {
    const date = timestampDate(ts);
    // A zero timestamp converts to the epoch, and "1 Jan 1970" rendered as
    // a promotion time looks like real data. Nothing is better than that.
    if (!Number.isFinite(date.getTime()) || date.getTime() <= 0) return undefined;
    return date.toISOString();
  } catch {
    return undefined;
  }
}

function kindOf(kind: DeployEnvironmentKind): CloudEnvKind {
  switch (kind) {
    case DeployEnvironmentKind.PERSISTENT:
      return "persistent";
    case DeployEnvironmentKind.PREVIEW:
      return "preview";
    case DeployEnvironmentKind.LOCAL:
      return "local";
    default:
      return "unknown";
  }
}

export function toCloudEnv(msg: DeployEnvironment): CloudEnv {
  return {
    id: msg.id,
    name: msg.name,
    project: msg.project ?? "",
    kind: kindOf(msg.kind),
    createdAt: toISO(msg.createdAt),
  };
}

/** DEPLOY_VERDICT_CONVERGED → "converged", through topology's one vocabulary. */
function verdictOf(verdict: DeployVerdict): HostedVerdict {
  return hostedVerdictOf(DeployVerdict[verdict]);
}

function observedStateOf(state: DeployObservedState | undefined): string {
  if (state === undefined || state === DeployObservedState.UNSPECIFIED) return "unknown";
  return (DeployObservedState[state] ?? "unknown").toLowerCase();
}

function suspendReasonOf(reason: DeploySuspendReason | undefined): string {
  if (reason === DeploySuspendReason.OWNER) return "owner";
  if (reason === DeploySuspendReason.NO_COMPUTE_PLAN) return "no_compute_plan";
  if (reason === DeploySuspendReason.BILLING_LAPSED) return "billing_lapsed";
  return "unspecified";
}

function tierOf(tier: DeployTier): string {
  if (tier === DeployTier.UNSPECIFIED) return "";
  return (DeployTier[tier] ?? "").toLowerCase();
}

export function toCloudPromotion(msg: DeployPromotion): CloudPromotion {
  const resolved = msg.resolvedArtifacts ?? {};
  return {
    id: msg.id,
    releaseVersion: msg.releaseVersion,
    // There is no rollback kind any more (roll forward only): the control
    // plane reads historical rollback rows as promotes of that release.
    kind: msg.kind === DeployPromotionKind.PROMOTE ? "promote" : "unknown",
    fromEnvironmentId: msg.fromEnvironmentId,
    promotedByUserId: msg.promotedByUserId,
    promotedByActor: msg.promotedByActor,
    note: msg.note,
    createdAt: toISO(msg.createdAt),
    artifacts: Object.keys(resolved)
      .sort()
      .map((name) => ({ name, digest: resolved[name] ?? "" })),
  };
}

/**
 * One deployment, in forge's hosted-workload shape. Same object
 * HostedWorkloadList already renders from forge's reports, so a workload's
 * verdict, drift and URL read identically whichever source produced them.
 */
function toWorkload(msg: DeploymentStatus): ForgeHostedWorkload {
  const deployment = msg.deployment;
  const observed = deployment?.observed;
  return {
    name: deployment?.name ?? "",
    tier: tierOf(deployment?.tier ?? DeployTier.UNSPECIFIED),
    url: observed?.url ?? "",
    verdict: verdictOf(msg.verdict),
    verdict_reason: msg.verdictReason,
    observed_state: observedStateOf(observed?.state),
    observed_digest: observed?.imageDigest ?? "",
    desired_digest: msg.desiredDigest,
    drifted: msg.drifted === true,
    last_error: observed?.lastError ?? "",
    suspend_reason: suspendReasonOf(observed?.suspendReason),
    deployment_id: deployment?.id ?? "",
    declared_run_state: runStateOf(deployment?.runState),
  };
}

function runStateOf(state: DeployRunState | undefined): "running" | "suspended" | "unspecified" {
  if (state === DeployRunState.RUNNING) return "running";
  if (state === DeployRunState.SUSPENDED) return "suspended";
  return "unspecified";
}

export function toCloudEnvStatus(msg: GetDeploymentStatusResponse): CloudEnvStatus {
  const observedTimes = (msg.deployments ?? [])
    .map((d) => toISO(d.observedAt))
    .filter((t): t is string => !!t)
    .sort();
  return {
    verdict: verdictOf(msg.environmentVerdict),
    workloads: (msg.deployments ?? []).map(toWorkload),
    currentPromotion: msg.currentPromotion ? toCloudPromotion(msg.currentPromotion) : null,
    observedAt: observedTimes[observedTimes.length - 1],
  };
}

// ── RPCs ────────────────────────────────────────────────────────────────────

function client() {
  return getControlPlaneClient(DeployService);
}

/**
 * Every environment the control plane holds for ONE forge project.
 *
 * The server filters by `project`, and the result is filtered AGAIN here, on
 * purpose. A control plane older than the project dimension ignores the
 * unknown request field and answers with the whole organization's
 * environments — which rendered under this project would put another
 * project's prod on this screen with this project's Promote button beside it.
 * Filtering on the response field makes that impossible: an old server's rows
 * carry no project, so they match nothing.
 */
export async function listProjectEnvironments(project: string): Promise<CloudEnv[]> {
  const res = await client().listEnvironments({ project });
  return (res.environments ?? [])
    .map(toCloudEnv)
    .filter((env) => env.id !== "" && env.name !== "" && env.project === project)
    .sort((a, b) => a.name.localeCompare(b.name));
}

/** The control plane's observation of one environment — a database read, no cluster hop. */
export async function getEnvironmentStatus(environmentId: string): Promise<CloudEnvStatus> {
  return toCloudEnvStatus(await client().getStatus({ environmentId }));
}

/** The environment's promotion ledger, newest first. */
export async function listEnvironmentPromotions(
  environmentId: string,
  limit = 25
): Promise<CloudPromotion[]> {
  const res = await client().listPromotions({ environmentId, limit });
  return (res.promotions ?? []).map(toCloudPromotion);
}

// ── Run state and teardown ──────────────────────────────────────────────────

export type RunStateRequest = "running" | "suspended";

function runStateEnum(state: RunStateRequest): DeployRunState {
  return state === "running" ? DeployRunState.RUNNING : DeployRunState.SUSPENDED;
}

/**
 * Stop or start EVERY deployment of an environment. Resume is refused by the
 * server (FailedPrecondition, billing reason in the message) when any
 * deployment is not entitled — nothing is written then. The caller shows
 * `cloudErrorDetail(err)` verbatim.
 */
export async function setEnvironmentRunState(environmentId: string, state: RunStateRequest): Promise<void> {
  await client().setEnvironmentRunState({ environmentId, runState: runStateEnum(state) });
}

/** Stop or start ONE deployment. */
export async function scaleDeployment(deploymentId: string, state: RunStateRequest): Promise<void> {
  await client().scale({ deploymentId, runState: runStateEnum(state) });
}

/** Tear the environment down. `force` removes its deployments with it. */
export async function deleteEnvironment(environmentId: string): Promise<void> {
  await client().deleteEnvironment({ environmentId, force: true });
}

/** True when the control plane refused for a reason the user can act on (billing, permission). */
export function isRefusal(err: unknown): boolean {
  return err instanceof ConnectError && (err.code === Code.FailedPrecondition || err.code === Code.PermissionDenied);
}
