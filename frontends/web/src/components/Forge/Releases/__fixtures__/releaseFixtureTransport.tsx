// Copyright (c) 2025 Reliant Labs

/**
 * The fixture ledger (releaseFixtures.ts) served the way production reads it:
 * DeployService over a transport, so the Releases pages' tests drive the real
 * generated hooks and services/forge/releases.ts's mapping.
 *
 * The fixtures are written as the pages' view models (ReleaseRecord,
 * ReleasePromotion, …), because that is what a test reads back off the
 * screen. The handlers here turn each into the wire message the control plane
 * would send, and the mapping turns it back: a field the mapping drops, or
 * reads differently, shows up in a test the same way it would in the console.
 *
 * ReleasesFixtureScope mounts it over a subtree as a TransportScope
 * (lib/transportScope.tsx) on the test's own QueryClient.
 */

import { useContext, useState, type ReactNode } from "react";
import { QueryClientContext, type QueryClient } from "@tanstack/react-query";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import type { Transport } from "@connectrpc/connect";

import {
  DeployAttemptState,
  DeployObservedState,
  DeployRolloutPhase,
  DeployVerdict,
} from "@/gen/controlplane/controlplane/v1/deploy_pb";
import {
  DeployChangeKind,
  DeployCommitComparisonState,
  DeployObjectComparisonState,
  type CompareDeployReleasesRequest,
  type GetDeployAttemptRequest,
  type GetDeployBundleRequest,
  type GetDeployReleaseRequest,
  type GetDeployRolloutRequest,
  type ListDeployAttemptsRequest,
  type ListDeployPromotionsRequest,
  type ListDeployReleasesRequest,
} from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { transport as appTransport } from "@/lib/connect";
import { mockTransport, type MockHandlers } from "@/lib/test-utils";
import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";
import type { DeployRun, DeployRunState } from "@/services/forge/deployAttempts";
import type {
  ChangeKind,
  ReleaseBundle,
  ReleaseCommit,
  ReleaseComparison,
  ReleaseGate,
  ReleasePromotion,
  ReleaseRecord,
  ReleaseRollout,
  WorkloadRollout,
} from "@/services/forge/releases";

import type { ReleasesFixtureApi } from "./releaseFixtures";

const ts = (iso: string | undefined) => (iso ? timestampFromDate(new Date(iso)) : undefined);

function wireCommit(c: ReleaseCommit) {
  return { sha: c.sha, subject: c.subject, author: c.author, time: ts(c.time) };
}

export function wireRelease(r: ReleaseRecord) {
  return {
    id: r.id,
    version: r.version,
    project: r.project,
    gitCommit: r.gitCommit,
    gitTag: r.gitTag,
    gitDirty: r.dirty,
    createdAt: ts(r.createdAt),
    createdByUserId: r.createdByUserId,
    provenance: {
      commitSubject: r.subject,
      commitAuthor: r.author,
      branch: r.branch,
      repo: r.repo,
      forgeVersion: r.forgeVersion,
      commitTime: ts(r.commitTime),
    },
    run: r.run ?? undefined,
    artifacts: r.artifacts,
    commits: r.commits ? { ...r.commits, commits: r.commits.commits.map(wireCommit) } : undefined,
  };
}

function wireGate(g: ReleaseGate) {
  return { name: g.name, status: g.rawStatus || g.status, summary: g.summary, url: g.url, recordedBy: g.recordedBy, finishedAt: ts(g.finishedAt) };
}

export function wirePromotion(p: ReleasePromotion) {
  return {
    id: p.id,
    environmentId: p.environmentId,
    releaseVersion: p.releaseVersion,
    fromEnvironmentName: p.fromEnvironmentName,
    promotedByUserId: p.promotedByUserId,
    promotedByActor: p.promotedByActor,
    approvedBy: p.approvedBy,
    note: p.note,
    createdAt: ts(p.createdAt),
    targets: p.targets,
    gates: p.gates.filter((g) => g.source === "claimed").map(wireGate),
    recordedGates: p.gates.filter((g) => g.source === "recorded").map(wireGate),
    bundleId: p.bundleId,
    bundleDigest: p.bundleDigest,
    resolvedArtifacts: p.resolvedArtifacts,
  };
}

const CHANGE: Record<ChangeKind, DeployChangeKind> = {
  added: DeployChangeKind.ADDED,
  modified: DeployChangeKind.MODIFIED,
  removed: DeployChangeKind.REMOVED,
  unknown: DeployChangeKind.UNSPECIFIED,
};

const COMMIT_STATE: Record<ReleaseComparison["commits"]["state"], DeployCommitComparisonState> = {
  computed: DeployCommitComparisonState.COMPUTED,
  same: DeployCommitComparisonState.SAME,
  "by-cut-time": DeployCommitComparisonState.BY_CUT_TIME,
  unrecorded: DeployCommitComparisonState.UNRECORDED,
  unknown: DeployCommitComparisonState.UNSPECIFIED,
};

const OBJECT_STATE: Record<ReleaseComparison["objects"]["state"], DeployObjectComparisonState> = {
  compared: DeployObjectComparisonState.COMPARED,
  "no-environment": DeployObjectComparisonState.NO_ENVIRONMENT,
  "bundle-missing": DeployObjectComparisonState.BUNDLE_MISSING,
  unknown: DeployObjectComparisonState.UNSPECIFIED,
};

function wireComparison(c: ReleaseComparison) {
  return {
    from: c.from ? wireRelease(c.from) : undefined,
    to: c.to ? wireRelease(c.to) : undefined,
    commits: { ...c.commits, state: COMMIT_STATE[c.commits.state], commits: c.commits.commits.map(wireCommit) },
    artifacts: c.artifacts.map((a) => ({ ...a, change: CHANGE[a.change] })),
    unchangedArtifacts: c.unchangedArtifacts,
    objects: { ...c.objects, state: OBJECT_STATE[c.objects.state], deltas: c.objects.deltas.map((d) => ({ ...d, change: CHANGE[d.change] })) },
  };
}

const PHASE: Record<ReleaseRollout["phase"], DeployRolloutPhase> = {
  pending: DeployRolloutPhase.PENDING,
  progressing: DeployRolloutPhase.PROGRESSING,
  stabilizing: DeployRolloutPhase.STABILIZING,
  succeeded: DeployRolloutPhase.SUCCEEDED,
  degraded: DeployRolloutPhase.DEGRADED,
  superseded: DeployRolloutPhase.SUPERSEDED,
  held: DeployRolloutPhase.HELD,
  unknown: DeployRolloutPhase.UNKNOWN,
};

const VERDICT: Record<WorkloadRollout["verdict"], DeployVerdict> = {
  converged: DeployVerdict.CONVERGED,
  converging: DeployVerdict.CONVERGING,
  diverged: DeployVerdict.DIVERGED,
  degraded: DeployVerdict.DEGRADED,
  unknown: DeployVerdict.UNKNOWN,
};

const OBSERVED: Record<WorkloadRollout["observedState"], DeployObservedState> = {
  pending: DeployObservedState.PENDING,
  progressing: DeployObservedState.PROGRESSING,
  ready: DeployObservedState.READY,
  degraded: DeployObservedState.DEGRADED,
  suspended: DeployObservedState.SUSPENDED,
  unknown: DeployObservedState.UNSPECIFIED,
};

function wireWorkloadRollout(w: WorkloadRollout) {
  return { ...w, phase: PHASE[w.phase], verdict: VERDICT[w.verdict], observedState: OBSERVED[w.observedState], convergedAt: ts(w.convergedAt) };
}

function wireRollout(r: ReleaseRollout) {
  return {
    promotion: { id: r.promotionId },
    phase: PHASE[r.phase],
    reason: r.reason,
    startedAt: ts(r.startedAt),
    finishedAt: ts(r.finishedAt),
    workloads: r.workloads.map(wireWorkloadRollout),
    unpinned: r.unpinned.map(wireWorkloadRollout),
    forgeApplied: r.forgeApplied,
  };
}

function wireBundle(b: ReleaseBundle) {
  const objects = Object.entries(b.workloadImages ?? {}).map(([workload, images]) => ({ workload, images }));
  return {
    id: b.id,
    digest: b.digest,
    reference: b.reference,
    configDigest: b.configDigest,
    snapshotDigest: b.snapshotDigest,
    createdAt: ts(b.createdAt),
    createdBy: b.createdBy,
    shape: objects.length ? { objects } : undefined,
  };
}

const ATTEMPT_STATE: Record<DeployRunState, DeployAttemptState> = {
  running: DeployAttemptState.RUNNING,
  succeeded: DeployAttemptState.SUCCEEDED,
  failed: DeployAttemptState.FAILED,
  queued: DeployAttemptState.QUEUED,
  cancelled: DeployAttemptState.CANCELLED,
  abandoned: DeployAttemptState.ABANDONED,
  unknown: DeployAttemptState.UNSPECIFIED,
};

function wireAttempt(run: DeployRun) {
  return {
    id: run.id,
    environmentName: run.env,
    mode: run.mode,
    source: run.source,
    releaseVersion: run.release,
    promotionId: run.promotionId,
    startedByUserId: run.startedByUserId,
    startedByActor: run.startedByActor,
    phase: run.phase,
    phases: run.phases.map((p) => ({ name: p.name, startedAt: ts(p.at) })),
    state: ATTEMPT_STATE[run.state],
    failedPhase: run.failedPhase,
    errorMessage: run.error,
    exitCode: run.exitCode ?? undefined,
    run: run.runUrl ? { url: run.runUrl } : undefined,
    startedAt: ts(run.startedAt),
    finishedAt: ts(run.finishedAt),
    hasLog: run.hasLog,
    logTruncated: run.logTruncated,
    hasReport: run.hasReport,
  };
}

/** DeployService's Releases reads, answered from `api` as wire messages. */
export function releasesFixtureHandlers(api: ReleasesFixtureApi): MockHandlers {
  const handlers: Record<string, (req: never) => Promise<unknown>> = {
    listReleases: async (req: ListDeployReleasesRequest) => ({ releases: (await api.listReleases(req.project ?? "")).map(wireRelease) }),
    getRelease: async (req: GetDeployReleaseRequest) => {
      const found = await api.getRelease(req.project, req.version ?? "");
      return found ? { release: wireRelease(found.release), currentEnvironmentIds: found.currentEnvironmentIds } : {};
    },
    compareReleases: async (req: CompareDeployReleasesRequest) =>
      wireComparison(
        await api.compareReleases({
          project: req.project,
          from: req.fromVersion,
          to: req.toVersion,
          environmentId: req.environmentId || undefined,
          ...(req.workload ? { workload: req.workload } : {}),
        }),
      ),
    listPromotions: async (req: ListDeployPromotionsRequest) => ({ promotions: (await api.listPromotions(req.environmentId)).map(wirePromotion) }),
    getRollout: async (req: GetDeployRolloutRequest) => {
      const rollout = await api.getRollout(req.environmentId, req.promotionId);
      return rollout ? { rollout: wireRollout(rollout) } : {};
    },
    getBundle: async (req: GetDeployBundleRequest) => {
      const bundle = await api.getBundle(req.environmentId, req.bundleId, req.digest);
      return bundle ? { bundle: wireBundle(bundle) } : {};
    },
    listDeployAttempts: async (req: ListDeployAttemptsRequest) => ({
      attempts: (await api.listDeployRuns(req.project, req.environmentName || undefined)).map(wireAttempt),
    }),
    getDeployAttempt: async (req: GetDeployAttemptRequest) => {
      const detail = await api.getDeployRun(req.id);
      return { attempt: wireAttempt(detail.run), log: detail.log, reportJson: detail.report };
    },
  };
  return Object.fromEntries(
    Object.entries(handlers).map(([method, handler]) => [`controlplane.v1.DeployService/${method[0]!.toUpperCase()}${method.slice(1)}`, (req: unknown) => handler(req as never)]),
  );
}

/**
 * The fixture ledger's rpcs answered from `api`; every other rpc goes to
 * `fallback` (the app's transport by default). A test can compose this with
 * another fixture transport before binding its QueryClient to one scope.
 */
export function ledgerTransport(api: ReleasesFixtureApi, fallback: Transport = appTransport): Transport {
  const handlers = releasesFixtureHandlers(api);
  const ledger = mockTransport(handlers);
  const owns = (method: { parent: { typeName: string }; name: string }) => `${method.parent.typeName}/${method.name}` in handlers;
  return {
    unary: (method, ...rest) => (owns(method) ? ledger : fallback).unary(method, ...rest),
    stream: (method, ...rest) => (owns(method) ? ledger : fallback).stream(method, ...rest),
  };
}

/** A TransportScope on `client` (default: the QueryClient above it) whose Releases reads answer from `api`. */
export function ReleasesFixtureScope({ api, client, children }: { api: ReleasesFixtureApi; client?: QueryClient; children: ReactNode }) {
  const outer = useContext(QueryClientContext);
  // Once per mount: a QueryClient belongs to one transport.
  const [scope] = useState(() =>
    createTransportScope({ id: "releases-fixture", transport: ledgerTransport(api), queryClient: client ?? outer ?? undefined }),
  );
  return <TransportScopeProvider scope={scope}>{children}</TransportScopeProvider>;
}
