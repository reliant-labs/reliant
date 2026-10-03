// Copyright (c) 2025 Reliant Labs

/**
 * react-query hooks for the forge topology screen.
 *
 * THE LOADING STRATEGY IS THE DESIGN, not an optimisation.
 *
 * `GetTopology` without verify is a local file read under a 15s budget, so it
 * paints the whole matrix essentially at once. With verify it reads EVERY
 * declared environment's live cluster under a 110s budget and can fail as a
 * whole — one unreachable cluster and nobody sees any of the topology.
 *
 * So the ledger is fetched unverified, and verification is opt-in PER
 * ENVIRONMENT via `VerifyEnv` (75s, one cluster). Each result is merged into the
 * cached topology, moving that env's cells out of `not_verified` and leaving
 * every other env visibly unverified — which is the honest rendering, because
 * nobody looked at them.
 *
 * Consequently there is no refetchInterval here. A background poll would
 * silently discard merged verifications and quietly walk observed cells back to
 * `not_verified`, which is the same class of bug as painting them green.
 */

import { useCallback, useMemo } from "react";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import { forgeGrpc, type ApplyPromoteResult, type DeployStatus, type StartDeployResult } from "../api/forge-grpc";
import type { ForgeAuditReport } from "../services/forge/audit";
import {
  cloudAvailabilityFromError,
  cloudErrorDetail,
  getEnvironmentStatus,
  hasCloudControlPlane,
  listEnvironmentPromotions,
  listProjectEnvironments,
  type CloudAvailability,
  type CloudEnv,
  type CloudEnvStatus,
  type CloudPromotion,
} from "../services/forge/cloudEnvs";
import {
  getLiveView,
  hasLiveControlPlane,
  listEnvironmentConvergences,
  liveAvailabilityFromError,
  liveErrorDetail,
  type LiveAvailability,
  type LiveConvergence,
  type LiveEnv,
} from "../services/forge/live";
import {
  registerEnvironment,
  type ForgeEnvShapeReport,
  type RegisterCandidate,
} from "../services/forge/register";
import {
  cloudRunIdOf,
  daemonSideOf,
  joinEnvironments,
  resolveForgeProjectName,
  type DaemonSide,
  type ForgeEnvSummary,
  type ForgeProjectName,
} from "../services/forge/environments";
import { deployTokenFor, type ForgeDeployReport } from "../services/forge/deploy";
import type { PlanApproval } from "../services/forge/deployPlan";
import type { ForgeCheckoutsReport } from "../services/forge/checkouts";
import type { ForgeEnvDiffReport } from "../services/forge/envDiff";
import { useProjectStore, type Project } from "../store/projectStore";
import { confirmationTokenFor, type ForgePromotePlan } from "../services/forge/promote";
import {
  availabilityFromError,
  deleteSecret,
  destroySecret,
  getSecretVersions,
  listSecrets,
  setSecret,
  undeleteSecret,
  type ManagedSecretHistory,
  type ManagedSecretSummary,
  type ManagedStoreAvailability,
  type ManagedStoreTarget,
  type SetSecretResult,
} from "../services/forge/secretStore";
import type { ForgeEnvStatusReport } from "../services/forge/status";
import {
  environments,
  mergeVerifyIntoTopology,
  type ForgeOutcome,
  type ForgeTopologyReport,
  type ForgeVerifyReport,
} from "../services/forge/topology";

// ── Retry policy ────────────────────────────────────────────────────────────

/**
 * Codes on which asking the daemon again cannot change the answer.
 *
 * FailedPrecondition is the one that matters: it is what the api-server now
 * answers when forge RAN and failed (a KCL render error, a provider the
 * command refuses) and when the daemon is too old for the command. Both are
 * facts about state a retry does not touch, and retrying them only delays the
 * sentence that says what to fix.
 */
const NON_RETRYABLE_FORGE_CODES: ReadonlySet<Code> = new Set([
  Code.FailedPrecondition,
  Code.NotFound,
  Code.InvalidArgument,
  Code.PermissionDenied,
  Code.Unauthenticated,
]);

/**
 * forgeRetry: at most ONE retry, and none for an answer. A thrown error on a
 * forge query is a transport or daemon problem (every meaningful non-success
 * arrives as data — see the useForgeTopology comment), and hammering a daemon
 * that is not answering only delays telling the user.
 */
export function forgeRetry(failureCount: number, error: unknown): boolean {
  if (error instanceof ConnectError && NON_RETRYABLE_FORGE_CODES.has(error.code)) return false;
  return failureCount < 1;
}

// ── Key factory ─────────────────────────────────────────────────────────────

export const forgeKeys = {
  all: ["forge"] as const,
  topology: (projectId: string) => [...forgeKeys.all, "topology", projectId] as const,
  audit: (projectId: string) => [...forgeKeys.all, "audit", projectId] as const,
  envStatus: (projectId: string, env: string) =>
    [...forgeKeys.all, "env-status", projectId, env] as const,
  promotePlan: (projectId: string, env: string, release: string) =>
    [...forgeKeys.all, "promote-plan", projectId, env, release] as const,
  deployPlan: (projectId: string, env: string) =>
    [...forgeKeys.all, "deploy-plan", projectId, env] as const,
  deployStatus: (projectId: string, handle: string) =>
    [...forgeKeys.all, "deploy-status", projectId, handle] as const,
  checkouts: (projectId: string, withTree: boolean) =>
    [...forgeKeys.all, "checkouts", projectId, withTree] as const,
  // Keyed by the CHECKOUT as well as the environment: the same environment
  // diffed from two branches is two different answers, and sharing a key would
  // show one branch's diff under the other's name.
  envDiff: (projectId: string, env: string, all: boolean, checkoutPath: string) =>
    [...forgeKeys.all, "env-diff", projectId, env, all, checkoutPath] as const,
  managedSecrets: (projectId: string, env: string) =>
    [...forgeKeys.all, "managed-secrets", projectId, env] as const,
  managedSecretVersions: (projectId: string, env: string, name: string) =>
    [...forgeKeys.all, "managed-secret-versions", projectId, env, name] as const,
  // Control-plane reads. Keyed by the FORGE project name and the control
  // plane's environment id — not the Reliant project id — because that is
  // what the control plane knows them by.
  cloudEnvs: (forgeProject: string) => [...forgeKeys.all, "cloud-envs", forgeProject] as const,
  // LIVE. Keyed by the forge project name, like cloudEnvs — one entry holds
  // every environment's whole Live answer, because GetLiveView returns them
  // together (see services/forge/live.ts on why it is one round trip).
  liveView: (forgeProject: string) => [...forgeKeys.all, "live-view", forgeProject] as const,
  envShape: (projectId: string, env: string) =>
    [...forgeKeys.all, "env-shape", projectId, env] as const,
  cloudStatus: (environmentId: string) => [...forgeKeys.all, "cloud-status", environmentId] as const,
  cloudPromotions: (environmentId: string) =>
    [...forgeKeys.all, "cloud-promotions", environmentId] as const,
  convergences: (environmentId: string) =>
    [...forgeKeys.all, "convergences", environmentId] as const,
};

// ── Topology ────────────────────────────────────────────────────────────────

/**
 * useForgeTopology loads the unverified ledger view.
 *
 * retry is 1, not the default 3. Every meaningful non-success — not a forge
 * project, forge too old, cluster unreachable — arrives as a SUCCESSFUL response
 * carrying data, by design. So a thrown error here is a transport or daemon
 * problem, and hammering a daemon that is not answering only delays the moment
 * the user is told.
 */
export function useForgeTopology(projectId: string | null | undefined) {
  return useQuery<ForgeOutcome<ForgeTopologyReport>>({
    queryKey: forgeKeys.topology(projectId ?? ""),
    queryFn: () => forgeGrpc.getTopology({ projectId: projectId as string }),
    enabled: !!projectId,
    staleTime: 30_000,
    retry: forgeRetry,
  });
}

/**
 * useVerifyForgeEnv verifies ONE environment and merges the observation into the
 * cached topology.
 *
 * The merge is deliberately narrow: only the named env's image cells change, and
 * only for images the verify report actually mentions. An image it did not
 * mention keeps its prior cell — a verify that did not see an image has not
 * established that the release stopped declaring it.
 *
 * When the verify itself comes back unreachable or unsupported, the cached
 * topology is left ALONE. The env's cells stay `not_verified`, which is the true
 * statement; overwriting them with `unreachable` would replace "nobody has
 * looked" with "we looked and could not see", and the latter is a stronger claim
 * than the RPC supports at the env level.
 */
export function useVerifyForgeEnv(projectId: string | null | undefined) {
  const queryClient = useQueryClient();

  const mutation = useMutation<
    { env: string; outcome: ForgeOutcome<ForgeVerifyReport> },
    Error,
    string
  >({
    mutationFn: async (env: string) => ({
      env,
      outcome: await forgeGrpc.verifyEnv(projectId as string, env),
    }),
    onSuccess: ({ env, outcome }) => {
      if (outcome.kind !== "report") return;
      queryClient.setQueryData<ForgeOutcome<ForgeTopologyReport>>(
        forgeKeys.topology(projectId ?? ""),
        (previous) => {
          if (!previous || previous.kind !== "report") return previous;
          return {
            ...previous,
            report: mergeVerifyIntoTopology(previous.report, env, outcome.report),
          };
        }
      );
    },
  });

  const verify = useCallback(
    (env: string) => {
      if (!projectId) return;
      mutation.mutate(env);
    },
    [mutation, projectId]
  );

  // Which env is in flight, so a row can show its own pending state rather than
  // the whole table going busy while one cluster is read.
  const pendingEnv = mutation.isPending ? (mutation.variables ?? null) : null;

  // An env whose verify returned a non-report outcome: the cells legitimately
  // stayed unverified, and the row needs to say why rather than looking like the
  // click did nothing.
  const lastOutcome = useMemo(
    () => (mutation.data && mutation.data.outcome.kind !== "report" ? mutation.data : null),
    [mutation.data]
  );

  return {
    verify,
    pendingEnv,
    lastOutcome,
    error: mutation.error,
    reset: mutation.reset,
  };
}

// ── Environments: the control plane ∪ the daemon ────────────────────────────

/**
 * useCloudEnvironments lists the control plane's environments for ONE forge
 * project — no daemon involved.
 *
 * A failure resolves to an `availability` instead of throwing, for the same
 * reason useManagedSecrets does: four of the five ways this can "fail" (no
 * control plane in this build, the deploy product not enabled for the org, a
 * control plane that does not serve the deploy domain) are states to DESCRIBE,
 * and a red banner in front of every local-only forge project would be a lie.
 *
 * `forgeProject` null means the join key is not known yet (the daemon has not
 * answered and nothing is cached) — nothing is fetched, rather than listing a
 * guess.
 */
export function useCloudEnvironments(forgeProject: string | null | undefined) {
  const enabled = !!forgeProject && hasCloudControlPlane();
  return useQuery<{ availability: CloudAvailability; envs: CloudEnv[]; detail: string }>({
    queryKey: forgeKeys.cloudEnvs(forgeProject ?? ""),
    queryFn: async () => {
      try {
        return {
          availability: "available" as const,
          envs: await listProjectEnvironments(forgeProject as string),
          detail: "",
        };
      } catch (err) {
        return { availability: cloudAvailabilityFromError(err), envs: [], detail: cloudErrorDetail(err) };
      }
    },
    enabled,
    staleTime: 30_000,
    retry: false,
  });
}

// ── LIVE: the control plane, and nothing else ───────────────────────────────

/**
 * useLiveView is THE Live query. One round trip to the control plane, with the
 * user's session, for every environment in one forge project.
 *
 * NO DAEMON HOOK IS COMPOSED INTO THIS, and that is the property under test
 * (ForgeEnvPage.liveNoDaemon.test.tsx renders the Live surfaces with the
 * daemon transport mocked to THROW and asserts zero calls). The old env page
 * joined a daemon topology report with a control-plane list, so an asleep
 * laptop degraded a page describing a production environment the control plane
 * was observing the whole time — and because forge called ListEnvironments
 * with the DAEMON's token, it produced a 403 on a page the user was entitled
 * to see.
 *
 * A failure resolves to an `availability` rather than throwing, for
 * useCloudEnvironments' reason: four of the five ways this can "fail" are
 * states to DESCRIBE, and a red banner in front of every local-only forge
 * project would be a lie.
 *
 * `forgeProject` null means the join key is not known yet — Reliant has no
 * forge.yaml name on the project row and no daemon has ever reported one. The
 * query is not enabled, and the screen says so rather than listing a guess
 * (see resolveForgeProjectName).
 *
 * staleTime 15s with no poll: the ledger moves when somebody deploys, and
 * every write path in this module invalidates it. A background poll would buy
 * a few seconds of freshness for a request per environment per interval.
 */
export function useLiveView(forgeProject: string | null | undefined) {
  const enabled = !!forgeProject && hasLiveControlPlane();
  return useQuery<{ availability: LiveAvailability; envs: LiveEnv[]; detail: string }>({
    queryKey: forgeKeys.liveView(forgeProject ?? ""),
    queryFn: async () => {
      try {
        return {
          availability: "available" as const,
          envs: await getLiveView(forgeProject as string),
          detail: "",
        };
      } catch (err) {
        return { availability: liveAvailabilityFromError(err), envs: [], detail: liveErrorDetail(err) };
      }
    },
    enabled,
    staleTime: 15_000,
    retry: false,
  });
}

/**
 * Invalidate Live after a write moved it — a Register, a promote, a deploy.
 *
 * Invalidated rather than patched: the only honest source for "what does the
 * control plane hold for this env now" is the control plane. A fabricated row
 * would paper over a server-side refusal (EnsureEnvironment refuses a
 * declaration whose immutable fields disagree) and show the user a
 * registration that did not happen.
 */
export function useInvalidateLiveView() {
  const queryClient = useQueryClient();
  return useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: [...forgeKeys.all, "live-view"] });
  }, [queryClient]);
}

/**
 * The control plane's status for ONE environment it runs.
 *
 * THIS POLLS (30s, foreground only), and that is a deliberate exception to the
 * no-poll rule the daemon hooks above follow. Those re-probe a live cluster;
 * this reads columns the control plane's reconcile worker already wrote — a
 * database read with no cluster hop — so a poll costs a query, and in exchange
 * a deploy converging in another tab settles on this screen without a reload.
 */
const CLOUD_STATUS_POLL_MS = 30_000;

function cloudStatusQuery(environmentId: string) {
  return {
    queryKey: forgeKeys.cloudStatus(environmentId),
    queryFn: () => getEnvironmentStatus(environmentId),
    enabled: environmentId !== "",
    staleTime: 10_000,
    refetchInterval: CLOUD_STATUS_POLL_MS,
    retry: forgeRetry,
  };
}

export function useCloudEnvStatus(environmentId: string | null | undefined) {
  return useQuery<CloudEnvStatus>(cloudStatusQuery(environmentId ?? ""));
}

/** The same query per environment, for the overview's rows. One cache entry per env, shared with the env page. */
export interface CloudStatusEntry {
  data?: CloudEnvStatus;
  isLoading: boolean;
  error: unknown;
}

export function useCloudEnvStatuses(environmentIds: string[]): Map<string, CloudStatusEntry> {
  const results = useQueries({ queries: environmentIds.map((id) => cloudStatusQuery(id)) });
  // Rebuilt per render on purpose: a handful of entries, and memoising on a
  // freshly-allocated results array would never hit.
  const byId = new Map<string, CloudStatusEntry>();
  environmentIds.forEach((id, index) => {
    const result = results[index];
    byId.set(id, { data: result?.data, isLoading: !!result?.isLoading, error: result?.error ?? null });
  });
  return byId;
}

/** One environment's promotion ledger, newest first. Append-only, so no poll. */
export function useCloudPromotions(environmentId: string | null | undefined) {
  return useQuery<CloudPromotion[]>({
    queryKey: forgeKeys.cloudPromotions(environmentId ?? ""),
    queryFn: () => listEnvironmentPromotions(environmentId as string),
    enabled: !!environmentId,
    staleTime: 15_000,
    retry: forgeRetry,
  });
}

/**
 * One environment's OBSERVATION TIMELINE, newest first.
 *
 * Asked for EVERY environment kind, deliberately — unlike useCloudEnvStatus,
 * which is gated on the platform placing the workloads. The reading here is
 * made by the platform watching the cluster converge to the promoted config,
 * which happens for a customer's own cluster as much as for one we host.
 *
 * A FAILURE RESOLVES TO AN EMPTY LIST rather than propagating. The timeline is
 * a secondary record — derived from the cluster's current state, rebuildable,
 * never required for correctness — so it must not be able to take down the
 * promotion history or the state line beside it. An empty timeline renders as
 * nothing at all, which is also the correct rendering for the common case
 * today, where no observations exist.
 */
export function useLiveConvergences(environmentId: string | null | undefined) {
  return useQuery<LiveConvergence[]>({
    queryKey: forgeKeys.convergences(environmentId ?? ""),
    queryFn: async () => {
      try {
        return await listEnvironmentConvergences(environmentId as string);
      } catch {
        return [];
      }
    },
    enabled: !!environmentId,
    staleTime: 15_000,
    retry: false,
  });
}

export interface ForgeEnvironmentsState {
  /** Every environment either source knows, joined by name. */
  envs: ForgeEnvSummary[];
  /** forge's side: the topology query, and what state it is in. */
  topology: ReturnType<typeof useForgeTopology>;
  daemon: DaemonSide;
  /** The control plane's side. `undefined` data while it has not answered. */
  cloud: ReturnType<typeof useCloudEnvironments>;
  /** The join key, and where it came from. */
  projectName: ForgeProjectName;
  /** Nothing to show yet from EITHER side. */
  isLoading: boolean;
}

/** The forge project name persisted on `projectId`'s row, from whichever store slot holds it. */
function persistedForgeProjectName(
  state: { projects: Project[]; currentProject: Project | null },
  projectId: string | null | undefined
): string | null {
  if (!projectId) return null;
  const row =
    state.currentProject?.id === projectId
      ? state.currentProject
      : state.projects.find((project) => project.id === projectId);
  return row?.forge_project_name ?? null;
}

/**
 * useForgeEnvironments is the ONE environment list every forge screen reads —
 * the sidebar, the overview and the environment page — so none of them can
 * disagree about which environments exist.
 *
 * The two sources are queried in parallel and joined; neither waits for the
 * other. The only ordering is the join KEY: the forge project name comes from
 * forge's topology report when the daemon answers, and from the name Reliant
 * persisted on the project row when it does not (see resolveForgeProjectName
 * for why nothing is guessed beyond that).
 */
export function useForgeEnvironments(projectId: string | null | undefined): ForgeEnvironmentsState {
  const topology = useForgeTopology(projectId);
  const persistedName = useProjectStore((state) => persistedForgeProjectName(state, projectId));
  const projectName = resolveForgeProjectName(persistedName, topology.data);

  const cloud = useCloudEnvironments(projectName.name);

  const envs = useMemo(() => {
    const forgeEnvs = topology.data?.kind === "report" ? environments(topology.data.report) : [];
    return joinEnvironments(forgeEnvs, cloud.data?.envs ?? []);
  }, [topology.data, cloud.data]);

  const daemon = daemonSideOf(topology.data, topology.error);
  // The cloud side is "still asking" only when it has something to ask with.
  const cloudPending = cloud.isLoading && !!projectName.name;

  return {
    envs,
    topology,
    daemon,
    cloud,
    projectName,
    isLoading: envs.length === 0 && (daemon === "loading" || cloudPending),
  };
}

/** Invalidate everything the control plane reports for a project after a write moved it. */
export function useInvalidateCloudEnvironments() {
  const queryClient = useQueryClient();
  return useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: [...forgeKeys.all, "cloud-envs"] });
    void queryClient.invalidateQueries({ queryKey: [...forgeKeys.all, "cloud-status"] });
    void queryClient.invalidateQueries({ queryKey: [...forgeKeys.all, "cloud-promotions"] });
  }, [queryClient]);
}

/** The cloud-run environments among `envs`, as the ids their statuses are keyed by. */
export function cloudRunIds(envs: ForgeEnvSummary[]): string[] {
  return envs.map(cloudRunIdOf).filter((id): id is string => !!id);
}

// ── Register: the bootstrap, and the one daemon read Live depends on ────────

/**
 * useForgeEnvShape reads forge's projection of one environment's render.
 *
 * A PREVIEW query, not a Live one. It is the only daemon call on the Register
 * path, and it is here because only the daemon can read the user's files. The
 * WRITE that follows does not touch the daemon: useRegisterEnvironment calls
 * control-plane EnsureEnvironment straight from the browser.
 *
 * staleTime 0 and gcTime 0, following the plan queries and for the same
 * reason: this document becomes an immutable declaration — an environment's
 * kind cannot be changed once recorded — so the shape a user registers is
 * always the one forge just rendered, never one cached from a checkout that
 * has since moved.
 */
export function useForgeEnvShape(
  projectId: string | null | undefined,
  env: string | null | undefined,
  enabled = true
) {
  return useQuery<ForgeOutcome<ForgeEnvShapeReport>>({
    queryKey: forgeKeys.envShape(projectId ?? "", env ?? ""),
    queryFn: () => forgeGrpc.getEnvShape(projectId as string, env as string),
    enabled: enabled && !!projectId && !!env,
    staleTime: 0,
    gcTime: 0,
    retry: forgeRetry,
  });
}

/**
 * useRegisterEnvironment creates the environment's control-plane row from a
 * Preview render — FROM THE BROWSER, with the user's session.
 *
 * THE MUTATION TAKES A CANDIDATE, NOT AN ENV NAME AND A KIND. That signature
 * is the safety property: a candidate can only come from registerCandidate,
 * which refuses a document whose kind forge did not state, whose shape
 * disagrees with its own kind, whose project does not match the one on screen,
 * or which carries a value-like key. So no call site can assemble a
 * declaration from component state, and "the shape recorded is the shape forge
 * rendered" holds by construction — which matters because the kind is
 * immutable and a wrong one produces an environment that can only be
 * abandoned.
 *
 * retry is DISABLED. EnsureEnvironment is idempotent, so a retry would be
 * harmless rather than dangerous, but it would also be pointless: the
 * failures worth seeing here are a refusal (the row exists with different
 * immutable fields) and an authz error, and neither changes on a second
 * attempt.
 *
 * On success LIVE is invalidated, which is the whole point — the env appears
 * in Live as "Declared, not built yet" and its secrets become settable with
 * the daemon offline.
 */
export function useRegisterEnvironment() {
  const invalidateLive = useInvalidateLiveView();
  return useMutation<string, Error, RegisterCandidate>({
    mutationFn: (candidate: RegisterCandidate) => registerEnvironment(candidate),
    retry: false,
    onSuccess: invalidateLive,
  });
}

// ── Env runtime status ──────────────────────────────────────────────────────

/**
 * useForgeEnvStatus runs forge's env-runtime checks for ONE environment.
 *
 * NO refetchInterval, and that is a decision rather than an omission. This looks
 * like the archetypal polling surface — it is a monitoring panel — but the call
 * probes a live stack under a 15s check budget, and a background poll would mean
 * the panel silently re-probes a cluster nobody is looking at. Worse, a poll that
 * lands while the daemon is briefly unavailable replaces a screen of measured
 * results with a screen of undetermined ones. Refresh is therefore an explicit
 * act: `refetch()`, driven by the reader.
 *
 * staleTime is 10s — long enough that navigating away and back does not re-probe
 * the stack, short enough that a deliberate revisit after a restart measures
 * again rather than showing the pre-restart answer.
 *
 * retry is 1, as everywhere on this surface: every meaningful non-success (not a
 * forge project, forge too old, cluster unreachable) arrives as a SUCCESSFUL
 * response carrying data, so a thrown error here is a transport or daemon
 * problem, and hammering a daemon that is not answering only delays the moment
 * the user is told.
 */
export function useForgeEnvStatus(
  projectId: string | null | undefined,
  env: string | null | undefined
) {
  return useQuery<ForgeOutcome<ForgeEnvStatusReport>>({
    queryKey: forgeKeys.envStatus(projectId ?? "", env ?? ""),
    queryFn: () =>
      forgeGrpc.getEnvStatus(projectId as string, env as string) as Promise<
        ForgeOutcome<ForgeEnvStatusReport>
      >,
    enabled: !!projectId && !!env,
    staleTime: 10_000,
    retry: forgeRetry,
  });
}

// ── Project audit ───────────────────────────────────────────────────────────

/**
 * useForgeAudit loads the project-audit roll-up.
 *
 * staleTime is 60s, the longest on this surface, because the audit is static
 * analysis over files on disk: it cannot change without someone editing the
 * project, and on a project the size of control-plane it produces ~58KB. A strip
 * that re-ran it on every mount would pay that repeatedly to learn nothing.
 */
export function useForgeAudit(projectId: string | null | undefined) {
  return useQuery<ForgeOutcome<ForgeAuditReport>>({
    queryKey: forgeKeys.audit(projectId ?? ""),
    queryFn: () =>
      forgeGrpc.getAudit(projectId as string) as Promise<ForgeOutcome<ForgeAuditReport>>,
    enabled: !!projectId,
    staleTime: 60_000,
    retry: forgeRetry,
  });
}

// ── Promote ─────────────────────────────────────────────────────────────────

/**
 * useForgePromotePlan previews a promote. READ-ONLY — nothing is written.
 *
 * staleTime is 0, alone on this surface, and that is the point rather than an
 * oversight. Every other query here can serve a slightly old answer harmlessly;
 * this one's result becomes a CONFIRMATION TOKEN authorising a destructive
 * write. A cached plan is a claim about a binding that may have moved since, so
 * the plan a reviewer confirms from is always freshly computed, and the guard on
 * the server is left as the backstop it should be rather than the only check.
 *
 * retry is 1, as everywhere on this surface: the meaningful non-successes arrive
 * as successful responses carrying data, so a thrown error is a transport or
 * daemon problem.
 */
export function useForgePromotePlan(
  projectId: string | null | undefined,
  env: string | null | undefined,
  release: string | null | undefined
) {
  return useQuery<ForgeOutcome<ForgePromotePlan>>({
    queryKey: forgeKeys.promotePlan(projectId ?? "", env ?? "", release ?? ""),
    queryFn: () =>
      forgeGrpc.planPromote({
        projectId: projectId as string,
        env: env as string,
        release: release as string,
      }),
    enabled: !!projectId && !!env && !!release,
    staleTime: 0,
    gcTime: 0,
    retry: forgeRetry,
  });
}

/**
 * useApplyForgePromote performs THE WRITE.
 *
 * THE MUTATION TAKES A PLAN, NOT AN ENV AND A RELEASE. That signature is the
 * safety property: the confirmation token is derived here, from the plan
 * document that was rendered, by the single producer in the data layer. No call
 * site can supply a token of its own, assemble one from component state, or
 * reach this write without a plan in hand — so "the token describes what the
 * user saw" holds by construction. The env and release are read off the same
 * document for the same reason.
 *
 * retry is DISABLED outright. Retrying a write whose outcome is unknown is
 * exactly the wrong reflex: a timed-out promote may or may not have written, and
 * a second attempt would either refuse (harmless but confusing) or, if the first
 * attempt landed, silently re-assert a stale claim. The user re-plans instead,
 * which shows them what actually happened.
 *
 * On success the topology cache is INVALIDATED rather than patched. A promote
 * changes the binding, and every image cell in that row was verified against the
 * OLD release — patching the release string while leaving `match` cells in place
 * would claim those digests were proven against a release nobody has verified.
 * Dropping back to `not_verified` is the honest state.
 */
export function useApplyForgePromote(projectId: string | null | undefined) {
  const queryClient = useQueryClient();

  const mutation = useMutation<ApplyPromoteResult, Error, ForgePromotePlan>({
    mutationFn: async (plan: ForgePromotePlan) => {
      const token = confirmationTokenFor(plan);
      // Unreachable through the UI, which disables confirm without a token. It
      // throws rather than defaulting because every available default is a claim
      // the user never made: asserting unbound would ask to blind-overwrite a
      // bound env, and a guessed release would ask the guard to match something
      // nobody saw.
      if (!token) {
        throw new Error(
          "This plan does not say what the environment is currently bound to, so no promote can be authorised from it."
        );
      }
      return forgeGrpc.applyPromote({
        projectId: projectId as string,
        env: plan.env as string,
        release: (plan.target?.release ?? plan.release) as string,
        token,
      });
    },
    retry: false,
    onSuccess: (result) => {
      // A refusal wrote nothing, so nothing is invalidated: the cached topology
      // is still an accurate picture of a binding this call did not touch.
      if (result.kind !== "applied") return;
      void queryClient.invalidateQueries({ queryKey: forgeKeys.topology(projectId ?? "") });
      void queryClient.invalidateQueries({ queryKey: forgeKeys.audit(projectId ?? "") });
      // A hosted env's binding and workloads live on the control plane too,
      // and the Overview/Environment screens read them from there.
      void queryClient.invalidateQueries({ queryKey: [...forgeKeys.all, "cloud-status"] });
      void queryClient.invalidateQueries({ queryKey: [...forgeKeys.all, "cloud-promotions"] });
    },
  });

  return mutation;
}

// ── Deploy ──────────────────────────────────────────────────────────────────

/**
 * useForgeDeployPlan previews a deploy. READ-ONLY — nothing is applied.
 *
 * staleTime and gcTime are both 0, following the promote plan and for a sharper
 * version of the same reason: this document becomes a CONFIRMATION TOKEN
 * authorising manifests onto a live cluster, and the claim it carries includes
 * WHICH CLUSTER — a fact that lives in a KCL file anyone can edit. A cached plan
 * is a claim about a declared context that may have moved since, so the plan a
 * reviewer confirms from is always freshly computed and the server's guard is
 * left as the backstop it should be rather than the only check.
 *
 * retry is 1, as everywhere on this surface: the meaningful non-successes (not a
 * forge project, forge too old, cluster unreachable) arrive as SUCCESSFUL
 * responses carrying data, so a thrown error is a transport or daemon problem.
 */
export function useForgeDeployPlan(
  projectId: string | null | undefined,
  env: string | null | undefined
) {
  return useQuery<ForgeOutcome<ForgeDeployReport>>({
    queryKey: forgeKeys.deployPlan(projectId ?? "", env ?? ""),
    queryFn: () => forgeGrpc.planDeploy({ projectId: projectId as string, env: env as string }),
    enabled: !!projectId && !!env,
    staleTime: 0,
    gcTime: 0,
    retry: forgeRetry,
  });
}

/**
 * useStartForgeDeploy STARTS A REAL DEPLOY.
 *
 * THE MUTATION TAKES A PLAN, NOT AN ENV AND A CLUSTER NAME. That signature is
 * the safety property: the confirmation token — including expected_declared_context,
 * the field that decides where bytes land — is derived here from the plan
 * document that was rendered, by the single producer in the data layer. No call
 * site can supply a token of its own, assemble one from component state, or reach
 * this write without a plan in hand, so "the cluster in the token is the cluster
 * on screen" holds by construction. The env is read off the same document for
 * the same reason.
 *
 * retry is DISABLED outright, and here that is not merely prudent. A start whose
 * response was lost may have got a deploy underway; a second attempt would
 * either be refused as already-running (harmless) or, worse, race a converging
 * cluster with a second apply stream. The user re-plans instead, which shows them
 * what is actually happening.
 *
 * On a successful start the topology and audit caches are INVALIDATED. Every
 * image cell in that env's row was verified against the cluster as it was before
 * this apply, so leaving `match` cells in place would claim digests were proven
 * against a state that is now changing underneath them. Dropping back to
 * `not_verified` is the honest position — and note that it is honest precisely
 * because a started deploy has not yet established anything.
 */
/**
 * What a deploy needs: the TARGET claim and the CONTENT claim, from two
 * different documents.
 *
 * guardPlan is the instant preview, which names the declared cluster and the
 * current binding — the target. approval is derived from the plan-only
 * document, which names the change set. BOTH are required and neither
 * substitutes for the other: the target says where bytes land, the approval
 * says what ships, and the interim that carried only the first is what this
 * replaced.
 */
export interface StartDeployArgs {
  /** The preview the target token is derived from. */
  guardPlan: ForgeDeployReport;
  /** The approval derived from the plan the operator read. */
  approval: PlanApproval;
  /** The checkout this deploy builds from. */
  checkoutPath?: string;
}

export function useStartForgeDeploy(projectId: string | null | undefined) {
  const queryClient = useQueryClient();

  const mutation = useMutation<StartDeployResult, Error, StartDeployArgs>({
    mutationFn: async ({ guardPlan, approval, checkoutPath }: StartDeployArgs) => {
      const token = deployTokenFor(guardPlan);
      // Unreachable through the UI, which does not render a confirm without a
      // token. It throws rather than defaulting because every available default
      // is a claim the user never made — and the most dangerous of them would be
      // a guessed cluster name.
      if (!token) {
        throw new Error(
          "This plan cannot authorise a deploy: it does not name a declared cluster, or forge's own guard refused it."
        );
      }
      return forgeGrpc.startDeploy({
        projectId: projectId as string,
        env: guardPlan.env as string,
        token,
        approval,
        checkoutPath,
      });
    },
    retry: false,
    onSuccess: (result) => {
      // A refusal applied nothing and a not-started reply started nothing, so in
      // both cases the cached topology still describes a cluster this call did
      // not touch.
      if (result.kind !== "started") return;
      void queryClient.invalidateQueries({ queryKey: forgeKeys.topology(projectId ?? "") });
      void queryClient.invalidateQueries({ queryKey: forgeKeys.audit(projectId ?? "") });
      // A hosted env's binding and workloads live on the control plane too,
      // and the Overview/Environment screens read them from there.
      void queryClient.invalidateQueries({ queryKey: [...forgeKeys.all, "cloud-status"] });
      void queryClient.invalidateQueries({ queryKey: [...forgeKeys.all, "cloud-promotions"] });
    },
  });

  return mutation;
}

/**
 * useStartForgeDeployPlan works out WHAT A DEPLOY WOULD SHIP, as a job.
 *
 * It takes no approval, because there is nothing to approve yet — this is the
 * call that produces the thing to approve. It writes no promotion and applies
 * nothing, so no cache describing a live environment becomes stale and nothing
 * is invalidated here. It DOES build, push and cut a release, which is why it
 * is a mutation rather than a query: running it twice is not free, and a query
 * would be free to refetch it on a window focus.
 *
 * retry is disabled. A plan whose response was lost may have a build underway,
 * and a second one would contend for the same environment's claim.
 */
export function useStartForgeDeployPlan(projectId: string | null | undefined) {
  return useMutation<StartDeployResult, Error, { env: string; checkoutPath?: string }>({
    mutationFn: ({ env, checkoutPath }) =>
      forgeGrpc.startDeployPlan({ projectId: projectId as string, env, checkoutPath }),
    retry: false,
  });
}

/**
 * useForgeCheckouts lists the branches a preview may render.
 *
 * Cheap and git-only — no cluster is touched — so unlike the rest of this
 * surface it is allowed a short staleTime rather than refetching on every
 * mount: the set of worktrees changes on a human timescale, and the picker
 * re-rendering its options underneath a click is worse than a few seconds of
 * staleness.
 */
export function useForgeCheckouts(
  projectId: string | null | undefined,
  options?: { withTree?: boolean }
) {
  return useQuery<ForgeOutcome<ForgeCheckoutsReport>>({
    queryKey: forgeKeys.checkouts(projectId ?? "", options?.withTree === true),
    queryFn: () =>
      forgeGrpc.listCheckouts({
        projectId: projectId as string,
        withTree: options?.withTree,
      }),
    enabled: !!projectId,
    staleTime: 30_000,
    retry: forgeRetry,
  });
}

/**
 * useForgeEnvDiff compares a checkout against what is deployed, per
 * environment.
 *
 * `enabled` is the caller's, and it is deliberately not defaulted to true: a
 * diff is a REAL render of every environment, which costs seconds, so it runs
 * when someone is looking at the cards rather than on mount of a page that
 * might only ever show the Live tab.
 */
export function useForgeEnvDiff(
  projectId: string | null | undefined,
  args: { env?: string; all?: boolean; checkoutPath?: string; enabled?: boolean }
) {
  const { env, all, checkoutPath, enabled } = args;
  return useQuery<ForgeOutcome<ForgeEnvDiffReport>>({
    queryKey: forgeKeys.envDiff(projectId ?? "", env ?? "", all === true, checkoutPath ?? ""),
    queryFn: () =>
      forgeGrpc.diffEnv({ projectId: projectId as string, env, all, checkoutPath }),
    enabled: !!projectId && (!!env || all === true) && enabled !== false,
    staleTime: 0,
    gcTime: 0,
    retry: forgeRetry,
  });
}

/**
 * useForgeDeployStatus polls a started deploy by handle.
 *
 * THIS IS THE ONE QUERY ON THIS SURFACE THAT POLLS, and the justification is
 * narrow: it reads a single entry from the daemon's in-memory job registry — it
 * does not probe a cluster — and there is a real apply in flight whose outcome
 * the operator is waiting on. Every other forge query refetches only when asked,
 * because a background poll there would re-probe clusters nobody is looking at.
 *
 * The interval STOPS at any terminal disposition, including `unknown`. Continuing
 * to poll an indeterminate job would imply the answer is still coming, and it is
 * not: a daemon that lost the handle has lost it permanently, and the only thing
 * that can resolve it is someone verifying the environment.
 *
 * retry is 1. An unrecognised handle is a successful response carrying
 * jobStatus `unknown` rather than an error, so a thrown error here is a transport
 * or daemon problem — and hammering a daemon that is not answering only delays
 * telling the user that the deploy's outcome is unknown.
 */
export function useForgeDeployStatus(
  projectId: string | null | undefined,
  handle: string | null | undefined
) {
  return useQuery<DeployStatus>({
    queryKey: forgeKeys.deployStatus(projectId ?? "", handle ?? ""),
    queryFn: () =>
      forgeGrpc.getDeployStatus({ projectId: projectId as string, handle: handle as string }),
    enabled: !!projectId && !!handle,
    // 2s: fast enough that a short deploy does not sit on a stale spinner, slow
    // enough that a twelve-deployment rollout is not hundreds of daemon round
    // trips.
    refetchInterval: (query) =>
      query.state.data && query.state.data.jobStatus !== "running" ? false : 2_000,
    staleTime: 0,
    retry: forgeRetry,
  });
}

// ── Managed secret store ────────────────────────────────────────────────────
//
// These hooks talk to control-plane's SecretStoreService (Connect), not to the
// daemon — so the whole secrets surface works with no daemon at all.
//
// The DECLARED names (which secrets a workload asks for) used to come from a
// second, daemon-sourced axis: `useForgeSecrets`, over `forge.secret_list`.
// That made the screen's most valuable row — "declared, and nobody has ever
// set it", the one that breaks a deploy — vanish whenever a laptop slept. The
// names now ride on the environment's own record as `declared_shape.secrets`
// (services/forge/live.ts), which is the same control plane this store is, so
// both halves of the join arrive together. The join itself is unchanged and
// still lives in services/forge/secretSurface.ts.

/**
 * useManagedSecrets loads the managed store's metadata for one environment.
 *
 * `target` decides whether there is a lookup at all (see managedStoreTarget).
 * A `none` target resolves IMMEDIATELY to its availability with no RPC: a
 * non-hosted env has no managed store, and asking control-plane about it
 * would need an id nobody has. `null` means the environment record that
 * decides the target has not arrived yet, so nothing is decided and nothing
 * is fetched.
 *
 * A FAILURE HERE IS USUALLY NOT AN ERROR. control-plane answers Unavailable
 * when no OpenBao is bound, and Unimplemented when the control plane predates
 * the service; both mean "this deployment has no managed store", which is a
 * state the screen describes rather than a fault it reports. So the hook
 * resolves those into an `availability` the caller can render, and only a
 * genuine transport failure surfaces as `unreachable`.
 *
 * `throwOnError: false` is deliberate: react-query's default of treating every
 * rejection as an error would put a red banner in front of a user whose only
 * crime is running reliant without a control plane.
 *
 * staleTime is short (5s): this is the thing the user is actively changing.
 * They set a secret and come straight back expecting the row to have moved.
 */
export function useManagedSecrets(
  projectId: string | null | undefined,
  env: string | null | undefined,
  target: ManagedStoreTarget | null
) {
  const environmentId = target?.kind === "lookup" ? target.environmentId : "";
  return useQuery<{ availability: ManagedStoreAvailability; secrets: ManagedSecretSummary[] }>({
    queryKey: [...forgeKeys.managedSecrets(projectId ?? "", env ?? ""), target?.kind ?? "", environmentId],
    queryFn: async () => {
      if (!target) return { availability: "unreachable" as const, secrets: [] };
      if (target.kind === "none") return { availability: target.availability, secrets: [] };
      try {
        return {
          availability: "available" as const,
          secrets: await listSecrets(target.environmentId),
        };
      } catch (err) {
        // The error object itself is NOT propagated into the cache or into any
        // message. Only the classification survives — a Connect error's detail
        // string is the kind of place a request echo could end up.
        return { availability: availabilityFromError(err), secrets: [] };
      }
    },
    enabled: !!projectId && !!env && !!target,
    staleTime: 5_000,
    retry: forgeRetry,
  });
}

/**
 * useManagedSecretVersions loads ONE secret's version history.
 *
 * Enabled only when a name is selected AND the env has a lookup target, so
 * opening the detail panel is what fetches the history rather than the list
 * page pre-fetching every secret's — which on a project with fifty secrets
 * would be fifty round trips to render a table nobody has opened yet.
 */
export function useManagedSecretVersions(
  projectId: string | null | undefined,
  env: string | null | undefined,
  environmentId: string | null | undefined,
  name: string | null | undefined
) {
  return useQuery<ManagedSecretHistory>({
    queryKey: forgeKeys.managedSecretVersions(projectId ?? "", env ?? "", name ?? ""),
    queryFn: () => getSecretVersions(environmentId as string, name as string),
    enabled: !!projectId && !!env && !!environmentId && !!name,
    staleTime: 5_000,
    retry: forgeRetry,
  });
}

/**
 * Invalidate both managed views for one (project, env).
 *
 * Every mutation below calls this rather than writing into the cache directly.
 * An optimistic cache write would mean this module CONSTRUCTING a summary, and
 * the only honest source for "what version does this secret have now" is the
 * store — KV-v2's cas can reject a write, another actor can write between our
 * read and our write, and a fabricated row would paper over both.
 */
function useInvalidateManagedSecrets(
  projectId: string | null | undefined,
  env: string | null | undefined
) {
  const queryClient = useQueryClient();
  return useCallback(() => {
    void queryClient.invalidateQueries({
      queryKey: forgeKeys.managedSecrets(projectId ?? "", env ?? ""),
    });
    // The versions queries are per-name; invalidating the prefix catches
    // whichever detail panel happens to be open.
    void queryClient.invalidateQueries({
      queryKey: [...forgeKeys.all, "managed-secret-versions", projectId ?? "", env ?? ""],
    });
    // The declared names ride on the environment record, and setting a secret
    // that was declared-unset changes that row's origin on the next read.
    void queryClient.invalidateQueries({ queryKey: [...forgeKeys.all, "live-view"] });
  }, [queryClient, projectId, env]);
}

/**
 * Every write is keyed on the env's control-plane id. A missing id is a
 * refusal, not a request with an empty field: the server would answer
 * InvalidArgument, and the UI never offers a write without a lookup target
 * anyway (modeSupportsWrite is false unless the store answered).
 */
function requireEnvironmentId(environmentId: string | null | undefined): string {
  if (!environmentId) {
    throw new Error("This environment has no managed store to write to.");
  }
  return environmentId;
}

/**
 * useSetManagedSecret writes a new version.
 *
 * THE VALUE PASSES THROUGH AND IS NOT RETAINED. It is a mutation VARIABLE, so
 * react-query holds it in `mutation.variables` for the lifetime of the
 * mutation — which is why the form calls `reset()` after a successful write
 * rather than leaving it parked in the hook's state. Nothing here logs it, and
 * the success payload is a version number.
 */
export function useSetManagedSecret(
  projectId: string | null | undefined,
  env: string | null | undefined,
  environmentId: string | null | undefined
) {
  const invalidate = useInvalidateManagedSecrets(projectId, env);
  return useMutation<SetSecretResult, Error, { name: string; value: string; cas?: number }>({
    mutationFn: ({ name, value, cas }) =>
      setSecret({ environmentId: requireEnvironmentId(environmentId), name, value, cas }),
    onSuccess: invalidate,
  });
}

/*
 * THERE IS NO "SET A SECRET ON AN ENVIRONMENT THE CONTROL PLANE HAS NEVER
 * SEEN" HOOK (#353, design §10).
 *
 * `useSetManagedSecretEnsuringEnvironment` used to be here. It merged a kind
 * the SET-SECRET FORM had collected with one forge may or may not have
 * reported, and created the environment's row from the result — so an
 * IMMUTABLE field could be set from component state, with an empty string as
 * its floor.
 *
 * Creating the row is now Preview's Register alone (useRegisterEnvironment),
 * from forge's own render, which is the only source that can state the kind
 * without guessing. Every write through this module therefore keys on a row
 * that already exists, which is what `requireEnvironmentId` above asserts.
 */

/** useDeleteManagedSecret soft-deletes. Recoverable — see useUndeleteManagedSecret. */
export function useDeleteManagedSecret(
  projectId: string | null | undefined,
  env: string | null | undefined,
  environmentId: string | null | undefined
) {
  const invalidate = useInvalidateManagedSecrets(projectId, env);
  return useMutation<void, Error, { name: string; versions?: number[] }>({
    mutationFn: ({ name, versions }) =>
      deleteSecret({ environmentId: requireEnvironmentId(environmentId), name, versions }),
    onSuccess: invalidate,
  });
}

export function useUndeleteManagedSecret(
  projectId: string | null | undefined,
  env: string | null | undefined,
  environmentId: string | null | undefined
) {
  const invalidate = useInvalidateManagedSecrets(projectId, env);
  return useMutation<void, Error, { name: string; versions: number[] }>({
    mutationFn: ({ name, versions }) =>
      undeleteSecret({ environmentId: requireEnvironmentId(environmentId), name, versions }),
    onSuccess: invalidate,
  });
}

/**
 * useDestroyManagedSecret permanently destroys versions. IRREVERSIBLE.
 *
 * A separate hook against a separate RPC, never a flag on delete — the
 * distinction is visible at every layer from the Bao path up, and the UI adds a
 * confirmation on top of that rather than relying on it alone.
 */
export function useDestroyManagedSecret(
  projectId: string | null | undefined,
  env: string | null | undefined,
  environmentId: string | null | undefined
) {
  const invalidate = useInvalidateManagedSecrets(projectId, env);
  return useMutation<void, Error, { name: string; versions: number[] }>({
    mutationFn: ({ name, versions }) =>
      destroySecret({ environmentId: requireEnvironmentId(environmentId), name, versions }),
    onSuccess: invalidate,
  });
}
