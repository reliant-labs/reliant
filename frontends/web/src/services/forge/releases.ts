// Copyright (c) 2025 Reliant Labs

/**
 * RELEASES: the control plane's release ledger, as the console's Releases
 * pages read it — DeployService.ListReleases / GetRelease / CompareReleases /
 * ListPromotions / GetRollout / GetBundle — mapped into plain objects.
 *
 * Like live.ts and cloudEnvs.ts this is the ONE file that knows the wire
 * shape. Every field is read explicitly, so a field a newer control plane
 * adds is dropped here rather than spread into a view that did not expect it,
 * and the views stay testable with object literals.
 *
 * ── WHAT THE LEDGER CAN AND CANNOT SAY ──────────────────────────────────────
 *
 *   - A release's commit subject and author are what forge recorded at cut
 *     time. A release cut by an older forge has neither, and
 *     the pages show the short sha alone rather than inventing a subject.
 *   - A commit range is CAPPED. `truncated` says the list is not the whole
 *     range (a shallow clone could not count it); it is never rendered as
 *     complete.
 *   - A promotion with `targets` moved only those components (a scoped
 *     deploy). It must not be read as the environment running
 *     the whole release.
 *   - There is no rollback kind any more: promoting an older release is an
 *     ordinary promote, and the pages work out "rolled back" from the order
 *     of releases, not from a field.
 */

import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  DeployObservedState,
  DeployRolloutPhase,
  DeployVerdict,
  type DeployArtifact,
  type DeployBundle,
  type DeployCommit,
  type DeployCommitRange,
  type DeployGate,
  type DeployPromotion,
  type DeployRelease,
  type DeployRollout,
  type DeployWorkloadRollout,
} from "@/gen/controlplane/controlplane/v1/deploy_pb";
import {
  DeployChangeKind,
  DeployCommitComparisonState,
  DeployObjectComparisonState,
  DeployService,
  type CompareDeployReleasesResponse,
  type DeployObjectDelta,
} from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { getControlPlaneClient } from "@/services/controlPlane/client";

// ── Shapes ──────────────────────────────────────────────────────────────────

export interface ReleaseCommit {
  sha: string;
  subject: string;
  author: string;
  time?: string;
}

/** The commits a release added over the release before it, as recorded at cut. */
export interface ReleaseCommitRange {
  baseRelease: string;
  baseCommit: string;
  /** Newest first. Capped. */
  commits: ReleaseCommit[];
  /** How many commits the range holds; 0 when it could not be counted. */
  total: number;
  /** The list is not the whole range. */
  truncated: boolean;
}

export interface ReleaseArtifact {
  name: string;
  /** image | static | source | package …, as forge recorded it. */
  kind: string;
  digest: string;
  version: string;
  platforms: string[];
  uri: string;
  variant: string;
}

export interface ReleaseRecord {
  id: string;
  version: string;
  project: string;
  gitCommit: string;
  gitTag: string;
  dirty: boolean;
  createdAt?: string;
  createdByUserId: string;
  /** The cut commit's subject line; "" when the cut recorded none. */
  subject: string;
  /** The cut commit's author; "" when the cut recorded none. */
  author: string;
  branch: string;
  repo: string;
  forgeVersion: string;
  commitTime?: string;
  /** The CI run that cut it, when one did. */
  run: { id: string; url: string; provider: string } | null;
  artifacts: ReleaseArtifact[];
  commits: ReleaseCommitRange | null;
}

export type GateStatus = "passed" | "failed" | "skipped" | "error";

export interface ReleaseGate {
  name: string;
  status: GateStatus;
  /** The raw status, when it was not one of the four (historical rows). */
  rawStatus: string;
  summary: string;
  url: string;
  recordedBy: string;
  finishedAt?: string;
  /** "claimed" at promote time, or "recorded" after it. */
  source: "claimed" | "recorded";
}

export interface ReleasePromotion {
  id: string;
  environmentId: string;
  releaseVersion: string;
  fromEnvironmentName: string;
  promotedByUserId: string;
  promotedByActor: string;
  approvedBy: string;
  note: string;
  createdAt?: string;
  /** The components a scoped deploy moved; empty when the whole release moved. */
  targets: string[];
  gates: ReleaseGate[];
  bundleId: string;
  bundleDigest: string;
}

export type CommitComparisonState = "computed" | "same" | "by-cut-time" | "unrecorded" | "unknown";
export type ObjectComparisonState = "compared" | "no-environment" | "bundle-missing" | "unknown";
export type ChangeKind = "added" | "modified" | "removed" | "unknown";

export interface ArtifactChange {
  name: string;
  kind: string;
  change: ChangeKind;
  from: string;
  to: string;
}

export interface ObjectDelta {
  change: ChangeKind;
  cluster: string;
  kind: string;
  namespace: string;
  name: string;
  workload: string;
  summary: string;
  images: { artifact: string; from: string; to: string }[];
}

export interface ReleaseComparison {
  from: ReleaseRecord | null;
  to: ReleaseRecord | null;
  commits: {
    state: CommitComparisonState;
    /** `to` is the older release: the commits are what moving to it takes away. */
    reverts: boolean;
    commits: ReleaseCommit[];
    total: number;
    truncated: boolean;
    releases: string[];
  };
  artifacts: ArtifactChange[];
  unchangedArtifacts: number;
  objects: {
    state: ObjectComparisonState;
    detail: string;
    deltas: ObjectDelta[];
    unchanged: number;
    summariesMissing: boolean;
  };
}

export type RolloutPhase =
  | "pending"
  | "progressing"
  | "stabilizing"
  | "succeeded"
  | "degraded"
  | "superseded"
  | "unknown"
  | "held";

export interface WorkloadRollout {
  name: string;
  artifact: string;
  pinnedDigest: string;
  observedDigest: string;
  phase: RolloutPhase;
  verdict: "converged" | "converging" | "diverged" | "degraded" | "unknown";
  observedState: "pending" | "progressing" | "ready" | "degraded" | "suspended" | "unknown";
  updatedReplicas: number;
  desiredReplicas: number;
  convergedAt?: string;
  lastError: string;
  /** One human line explaining the row's phase, as the control plane judged it. */
  reason: string;
}

/**
 * A runtime slot on a cluster FORGE applies (a self-managed environment the
 * hub does not converge). The control plane makes no observation of it, so it
 * carries no phase or verdict: the wire's are UNSPECIFIED ("not judged here",
 * which is not UNKNOWN), and folding them through the phase mapping would
 * call a part nobody is watching "Cannot be observed". Reported, never gating.
 */
export interface ForgeAppliedSlot {
  /** `<cluster>/<workload>/<slot>`. */
  name: string;
  artifact: string;
  /** The snapshot's content for this slot. */
  pinnedDigest: string;
  /** "applied by forge (not observed by the control plane)". */
  reason: string;
}

export interface ReleaseRollout {
  promotionId: string;
  phase: RolloutPhase;
  reason: string;
  startedAt?: string;
  finishedAt?: string;
  workloads: WorkloadRollout[];
  /** Workloads the promotion pinned nothing for (not part of this release). */
  unpinned: WorkloadRollout[];
  /** Slots forge applies itself: reported apart, never judged into `phase`. */
  forgeApplied: ForgeAppliedSlot[];
}

export interface ReleaseBundle {
  id: string;
  digest: string;
  reference: string;
  configDigest: string;
  snapshotDigest: string;
  createdAt?: string;
  createdBy: string;
}

// ── Mapping ─────────────────────────────────────────────────────────────────

function toISO(ts: Timestamp | undefined): string | undefined {
  if (!ts) return undefined;
  try {
    const date = timestampDate(ts);
    // A zero timestamp converts to the epoch; "1 Jan 1970" is not a time.
    if (!Number.isFinite(date.getTime()) || date.getTime() <= 0) return undefined;
    return date.toISOString();
  } catch {
    return undefined;
  }
}

function toCommit(msg: DeployCommit): ReleaseCommit {
  return { sha: msg.sha, subject: msg.subject, author: msg.author, time: toISO(msg.time) };
}

function toCommitRange(msg: DeployCommitRange | undefined): ReleaseCommitRange | null {
  if (!msg) return null;
  return {
    baseRelease: msg.baseRelease,
    baseCommit: msg.baseCommit,
    commits: (msg.commits ?? []).map(toCommit),
    total: msg.total,
    truncated: msg.truncated === true,
  };
}

function toArtifact(msg: DeployArtifact): ReleaseArtifact {
  return {
    name: msg.name,
    kind: msg.kind,
    digest: msg.digest,
    version: msg.version,
    platforms: [...(msg.platforms ?? [])],
    uri: msg.uri,
    variant: msg.variant,
  };
}

export function toReleaseRecord(msg: DeployRelease): ReleaseRecord {
  const p = msg.provenance;
  const run = msg.run;
  return {
    id: msg.id,
    version: msg.version,
    project: msg.project,
    gitCommit: msg.gitCommit || p?.commit || "",
    gitTag: msg.gitTag || p?.tag || "",
    dirty: msg.gitDirty === true || p?.dirty === true,
    createdAt: toISO(msg.createdAt),
    createdByUserId: msg.createdByUserId,
    subject: p?.commitSubject ?? "",
    author: p?.commitAuthor ?? "",
    branch: p?.branch ?? "",
    repo: p?.repo ?? "",
    forgeVersion: p?.forgeVersion ?? "",
    commitTime: toISO(p?.commitTime),
    run: run && (run.id || run.url) ? { id: run.id, url: run.url, provider: run.provider } : null,
    artifacts: (msg.artifacts ?? []).map(toArtifact).sort((a, b) => a.name.localeCompare(b.name)),
    commits: toCommitRange(msg.commits),
  };
}

const GATE_STATUSES: readonly GateStatus[] = ["passed", "failed", "skipped", "error"];

function toGate(msg: DeployGate, source: ReleaseGate["source"]): ReleaseGate {
  const raw = msg.status.trim().toLowerCase();
  // The read path maps an unrecognised (historical, free-text) status to
  // error, keeping the raw string — the proto's own rule.
  const status = (GATE_STATUSES as readonly string[]).includes(raw) ? (raw as GateStatus) : "error";
  return {
    name: msg.name,
    status,
    rawStatus: status === raw ? "" : msg.status,
    summary: msg.summary,
    url: msg.url,
    recordedBy: msg.recordedBy,
    finishedAt: toISO(msg.finishedAt) ?? toISO(msg.recordedAt),
    source,
  };
}

export function toReleasePromotion(msg: DeployPromotion): ReleasePromotion {
  return {
    id: msg.id,
    environmentId: msg.environmentId,
    releaseVersion: msg.releaseVersion,
    fromEnvironmentName: msg.fromEnvironmentName,
    promotedByUserId: msg.promotedByUserId,
    promotedByActor: msg.promotedByActor,
    approvedBy: msg.approvedBy,
    note: msg.note,
    createdAt: toISO(msg.createdAt),
    targets: [...(msg.targets ?? [])],
    gates: [...(msg.gates ?? []).map((g) => toGate(g, "claimed")), ...(msg.recordedGates ?? []).map((g) => toGate(g, "recorded"))],
    bundleId: msg.bundleId,
    bundleDigest: msg.bundleDigest,
  };
}

function changeOf(kind: DeployChangeKind): ChangeKind {
  switch (kind) {
    case DeployChangeKind.ADDED:
      return "added";
    case DeployChangeKind.MODIFIED:
      return "modified";
    case DeployChangeKind.REMOVED:
      return "removed";
    default:
      return "unknown";
  }
}

function commitStateOf(state: DeployCommitComparisonState | undefined): CommitComparisonState {
  switch (state) {
    case DeployCommitComparisonState.COMPUTED:
      return "computed";
    case DeployCommitComparisonState.SAME:
      return "same";
    case DeployCommitComparisonState.BY_CUT_TIME:
      return "by-cut-time";
    case DeployCommitComparisonState.UNRECORDED:
      return "unrecorded";
    default:
      return "unknown";
  }
}

function objectStateOf(state: DeployObjectComparisonState | undefined): ObjectComparisonState {
  switch (state) {
    case DeployObjectComparisonState.COMPARED:
      return "compared";
    case DeployObjectComparisonState.NO_ENVIRONMENT:
      return "no-environment";
    case DeployObjectComparisonState.BUNDLE_MISSING:
      return "bundle-missing";
    default:
      return "unknown";
  }
}

function toObjectDelta(msg: DeployObjectDelta): ObjectDelta {
  return {
    change: changeOf(msg.change),
    cluster: msg.cluster,
    kind: msg.kind,
    namespace: msg.namespace,
    name: msg.name,
    workload: msg.workload,
    summary: msg.summary,
    images: (msg.images ?? []).map((image) => ({ artifact: image.artifact, from: image.from, to: image.to })),
  };
}

export function toReleaseComparison(msg: CompareDeployReleasesResponse): ReleaseComparison {
  const commits = msg.commits;
  const objects = msg.objects;
  return {
    from: msg.from ? toReleaseRecord(msg.from) : null,
    to: msg.to ? toReleaseRecord(msg.to) : null,
    commits: {
      state: commitStateOf(commits?.state),
      reverts: commits?.reverts === true,
      commits: (commits?.commits ?? []).map(toCommit),
      total: commits?.total ?? 0,
      truncated: commits?.truncated === true,
      releases: [...(commits?.releases ?? [])],
    },
    artifacts: (msg.artifacts ?? []).map((a) => ({ name: a.name, kind: a.kind, change: changeOf(a.change), from: a.from, to: a.to })),
    unchangedArtifacts: msg.unchangedArtifacts,
    objects: {
      state: objectStateOf(objects?.state),
      detail: objects?.detail ?? "",
      deltas: (objects?.deltas ?? []).map(toObjectDelta),
      unchanged: objects?.unchanged ?? 0,
      summariesMissing: objects?.summariesMissing === true,
    },
  };
}

function phaseOf(phase: DeployRolloutPhase): RolloutPhase {
  switch (phase) {
    case DeployRolloutPhase.PENDING:
      return "pending";
    case DeployRolloutPhase.PROGRESSING:
      return "progressing";
    case DeployRolloutPhase.STABILIZING:
      return "stabilizing";
    case DeployRolloutPhase.SUCCEEDED:
      return "succeeded";
    case DeployRolloutPhase.DEGRADED:
      return "degraded";
    case DeployRolloutPhase.SUPERSEDED:
      return "superseded";
    case DeployRolloutPhase.HELD:
      return "held";
    default:
      // UNKNOWN, UNSPECIFIED, and any value this build does not know: never
      // folded into success.
      return "unknown";
  }
}

function verdictOf(verdict: DeployVerdict): WorkloadRollout["verdict"] {
  switch (verdict) {
    case DeployVerdict.CONVERGED:
      return "converged";
    case DeployVerdict.CONVERGING:
      return "converging";
    case DeployVerdict.DIVERGED:
      return "diverged";
    case DeployVerdict.DEGRADED:
      return "degraded";
    default:
      return "unknown";
  }
}

function observedStateOf(state: DeployObservedState): WorkloadRollout["observedState"] {
  switch (state) {
    case DeployObservedState.PENDING:
      return "pending";
    case DeployObservedState.PROGRESSING:
      return "progressing";
    case DeployObservedState.READY:
      return "ready";
    case DeployObservedState.DEGRADED:
      return "degraded";
    case DeployObservedState.SUSPENDED:
      return "suspended";
    default:
      return "unknown";
  }
}

function toWorkloadRollout(msg: DeployWorkloadRollout): WorkloadRollout {
  return {
    name: msg.name,
    artifact: msg.artifact,
    pinnedDigest: msg.pinnedDigest,
    observedDigest: msg.observedDigest,
    phase: phaseOf(msg.phase),
    verdict: verdictOf(msg.verdict),
    observedState: observedStateOf(msg.observedState),
    updatedReplicas: msg.updatedReplicas,
    desiredReplicas: msg.desiredReplicas,
    convergedAt: toISO(msg.convergedAt),
    lastError: msg.lastError,
    reason: msg.reason,
  };
}

/** Deliberately reads no phase or verdict: see ForgeAppliedSlot. */
function toForgeAppliedSlot(msg: DeployWorkloadRollout): ForgeAppliedSlot {
  return { name: msg.name, artifact: msg.artifact, pinnedDigest: msg.pinnedDigest, reason: msg.reason };
}

export function toReleaseRollout(msg: DeployRollout): ReleaseRollout {
  return {
    promotionId: msg.promotion?.id ?? "",
    phase: phaseOf(msg.phase),
    reason: msg.reason,
    startedAt: toISO(msg.startedAt),
    finishedAt: toISO(msg.finishedAt),
    workloads: (msg.workloads ?? []).map(toWorkloadRollout),
    unpinned: (msg.unpinned ?? []).map(toWorkloadRollout),
    forgeApplied: (msg.forgeApplied ?? []).map(toForgeAppliedSlot),
  };
}

export function toReleaseBundle(msg: DeployBundle): ReleaseBundle {
  return {
    id: msg.id,
    digest: msg.digest,
    reference: msg.reference,
    configDigest: msg.configDigest,
    snapshotDigest: msg.snapshotDigest,
    createdAt: toISO(msg.createdAt),
    createdBy: msg.createdBy,
  };
}

// ── RPCs ────────────────────────────────────────────────────────────────────

function client() {
  return getControlPlaneClient(DeployService);
}

/** How many releases the list asks for. The ledger is newest first. */
export const RELEASE_LIST_LIMIT = 50;

/**
 * The project's releases, newest first. Filtered on the response's project as
 * well as the request's, for the reason listProjectEnvironments gives: a
 * server that ignored the field would otherwise put another project's
 * releases, with this project's Promote button, on this screen.
 */
export async function listProjectReleases(project: string, limit = RELEASE_LIST_LIMIT): Promise<ReleaseRecord[]> {
  const res = await client().listReleases({ project, limit });
  return (res.releases ?? [])
    .map(toReleaseRecord)
    .filter((release) => release.version !== "" && (release.project === "" || release.project === project))
    .sort((a, b) => (b.createdAt ?? "").localeCompare(a.createdAt ?? ""));
}

/** One release, with the environments whose CURRENT promotion is it. Null when there is no such release. */
export async function getProjectRelease(
  project: string,
  version: string
): Promise<{ release: ReleaseRecord; currentEnvironmentIds: string[] } | null> {
  try {
    const res = await client().getRelease({ project, version });
    if (!res.release) return null;
    return { release: toReleaseRecord(res.release), currentEnvironmentIds: [...(res.currentEnvironmentIds ?? [])] };
  } catch (err) {
    if (err instanceof ConnectError && err.code === Code.NotFound) return null;
    throw err;
  }
}

/** What changed from `from` to `to`; objects are compared in `environmentId` when given. */
export async function compareProjectReleases(req: {
  project: string;
  from: string;
  to: string;
  environmentId?: string;
}): Promise<ReleaseComparison> {
  const res = await client().compareReleases({
    project: req.project,
    fromVersion: req.from,
    toVersion: req.to,
    environmentId: req.environmentId ?? "",
  });
  return toReleaseComparison(res);
}

/** One environment's promotion ledger, newest first. */
export async function listReleasePromotions(environmentId: string, limit = 100): Promise<ReleasePromotion[]> {
  const res = await client().listPromotions({ environmentId, limit });
  return (res.promotions ?? []).map(toReleasePromotion);
}

/** How far one promotion has rolled out, judged against its own pins. */
export async function getPromotionRollout(environmentId: string, promotionId: string): Promise<ReleaseRollout | null> {
  const res = await client().getRollout({ environmentId, promotionId });
  return res.rollout ? toReleaseRollout(res.rollout) : null;
}

/** The bundle one promotion applied. */
export async function getPromotionBundle(environmentId: string, bundleId: string, digest: string): Promise<ReleaseBundle | null> {
  const res = await client().getBundle({ environmentId, bundleId, digest });
  return res.bundle ? toReleaseBundle(res.bundle) : null;
}
