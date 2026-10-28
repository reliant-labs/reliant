// Copyright (c) 2025 Reliant Labs

/**
 * react-query hooks for the forge topology screen.
 *
 * THE LOADING STRATEGY IS THE DESIGN, not an optimisation.
 *
 * `GetTopology` without verify reads no workload, so it paints the whole
 * matrix in one forge run (it renders every env's KCL — ~30s on a real
 * project, under a 60s budget). With verify it reads EVERY
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
 *
 * TWO KEY FAMILIES, because two backends answer this surface:
 *
 *   - ForgeService (the daemon, through the api-server) is read through forge's
 *     generated hooks and `forgeServiceKeys`. Each read caches the server's
 *     RESPONSE and `select`s it through forge-model.ts into a ForgeOutcome, so
 *     errors-as-data are resolved once, per observer, without a second cache.
 *   - The control plane's services (environments, Live, status, promotions,
 *     convergences, the managed secret store) have no generated hooks, so
 *     they stay on the hand `forgeCloudKeys` below. A ForgeService write's
 *     generated invalidation cannot reach them, so each write names them.
 */

import { useCallback, useMemo } from "react";
import { useMutation, useQueries, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";

import { ForgeDeployJobStatus, ForgeService } from "../gen/reliant/v1/forge_pb";
import { connectClient } from "../lib/connect";
import {
  appliedPromoteOf,
  applyPromoteRequest,
  deployPlanOutcomeOf,
  deployRefusalOf,
  deployStatusOf,
  envShapeOutcomeOf,
  envStatusOutcomeOf,
  forgeRetry,
  promotePlanOutcomeOf,
  promoteRefusalOf,
  startDeployPlanResultOf,
  startDeployRequest,
  startDeployResultOf,
  topologyOutcomeOf,
  verifyOutcomeOf,
  withVerifiedEnv,
  type ApplyPromoteResult,
  type StartDeployResult,
} from "./forge-model";
import {
  forgeServiceQueries,
  useApplyPromote,
  useGetDeployStatus,
  useGetEnvShape,
  useGetEnvStatus,
  useGetTopology,
  usePlanDeploy,
  usePlanPromote,
  useStartDeploy,
} from "./forge-service-hooks_gen";
import { releaseKeys } from "./release-queries";
import {
  cloudAvailabilityFromError,
  cloudErrorDetail,
  deleteEnvironment,
  getEnvironmentStatus,
  hasCloudControlPlane,
  listEnvironmentPromotions,
  listProjectEnvironments,
  setEnvironmentRunState,
  setWorkloadRunState,
  type RunStateRequest,
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
import { registerEnvironment, type RegisterCandidate } from "../services/forge/register";
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
import { useProjectStore } from "../store/projectStore";
import { useProject } from "./project-queries";
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
  type ManagedSecretListing,
  type ManagedStoreAvailability,
  type ManagedStoreTarget,
  type SetSecretResult,
} from "../services/forge/secretStore";
import { isLocalLifecycle } from "../services/forge/status";
import { forgeEnvRoster } from "../services/forge/roster";
import { environments, type ForgeOutcome, type ForgeVerifyReport } from "../services/forge/topology";

/**
 * The ForgeService client, for the two writes whose generated hook would
 * refetch reads they did not move: VerifyEnv patches the topology it observed,
 * StartDeployPlan changes no environment (see each hook).
 */
const forgeClient = connectClient(ForgeService);

// ── Control-plane key factory ───────────────────────────────────────────────

/**
 * One control-plane read's key, called per request, with the read's scope on
 * it as `.all` — the shape forge's generated keys have, so an invalidation
 * names a scope rather than spelling a tuple.
 */
function cloudKey<Args extends readonly string[]>(name: string) {
  const all = ["forge", name] as const;
  return Object.assign((...args: Args) => [...all, ...args] as const, { all });
}

/**
 * The control plane's reads, which have no generated hooks. Keyed by the FORGE
 * project name and the control plane's environment id — not the Reliant
 * project id — because that is what the control plane knows them by. Under
 * ["forge"], beside the Releases (`releaseKeys`) and Domains (`domainKeys`)
 * families, which are control-plane reads too.
 */
export const forgeCloudKeys = {
  all: ["forge"] as const,
  cloudEnvs: cloudKey<[forgeProject: string]>("cloud-envs"),
  // Every project's environments at once: the Apps list (hooks/apps-queries.ts).
  orgEnvs: cloudKey<[]>("org-envs"),
  // LIVE. Keyed by the forge project name, like cloudEnvs — one entry holds
  // every environment's whole Live answer, because GetLiveView returns them
  // together (see services/forge/live.ts on why it is one round trip).
  liveView: cloudKey<[forgeProject: string]>("live-view"),
  cloudStatus: cloudKey<[environmentId: string]>("cloud-status"),
  cloudPromotions: cloudKey<[environmentId: string]>("cloud-promotions"),
  convergences: cloudKey<[environmentId: string]>("convergences"),
  // The managed secret store (control-plane SecretStoreService), keyed by the
  // Reliant project and env the screen shows it under.
  managedSecrets: cloudKey<[projectId: string, env: string]>("managed-secrets"),
  managedSecretVersions: cloudKey<[projectId: string, env: string, name: string]>("managed-secret-versions"),
};

/**
 * A hosted env's binding and workloads live on the control plane too, and the
 * Overview/Environment screens read them from there — so a ForgeService write
 * that moved a binding refetches them. ForgeService's own reads are the
 * generated mutation's to refetch.
 */
function invalidateCloudAfterWrite(queryClient: QueryClient) {
  void queryClient.invalidateQueries({ queryKey: forgeCloudKeys.cloudStatus.all });
  void queryClient.invalidateQueries({ queryKey: forgeCloudKeys.cloudPromotions.all });
}

/** The topology request every reader of one project's topology shares: unverified, every env. */
function topologyRequest(projectId: string | null | undefined) {
  return { projectId: projectId ?? "" };
}

/** One env's status request — shared by the env page, which asks, and the roster, which only listens. */
function envStatusRequest(projectId: string | null | undefined, env: string | null | undefined) {
  return { projectId: projectId ?? "", env: env ?? "" };
}

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
export function useForgeTopology(projectId: string | null | undefined, options?: { enabled?: boolean }) {
  // `enabled: false` with a real project id still READS the cache — another
  // surface (the sidebar's roster) may already have asked — it just never
  // asks itself.
  return useGetTopology(topologyRequest(projectId), {
    select: topologyOutcomeOf,
    enabled: !!projectId && options?.enabled !== false,
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
 *
 * NOT THE GENERATED useVerifyEnv, and that is the merge's whole point: forge
 * generates a custom mutation that invalidates every ForgeService read, and
 * that would refetch the very topology this patches — the unverified ledger
 * would land over the merge and walk the env's observed cells back to
 * `not_verified`, and every mounted env status would re-probe its cluster.
 * So the call goes through the generated client and writes only the one entry
 * it observed.
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
      outcome: verifyOutcomeOf(await forgeClient.verifyEnv({ projectId: projectId as string, env })),
    }),
    onSuccess: ({ env, outcome }) => {
      if (outcome.kind !== "report") return;
      queryClient.setQueryData(forgeServiceQueries.getTopology(topologyRequest(projectId)).queryKey, (previous) =>
        previous ? withVerifiedEnv(previous, env, outcome.report) : previous
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
    queryKey: forgeCloudKeys.cloudEnvs(forgeProject ?? ""),
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
  return useQuery<LiveViewResult>({ ...liveViewQuery(forgeProject ?? ""), enabled });
}

export interface LiveViewResult {
  availability: LiveAvailability;
  envs: LiveEnv[];
  detail: string;
}

/**
 * The Live query's key and fetch, shared by useLiveView and by the queued-
 * deploy watcher (QueuedDeployNotifier), which polls the SAME cache entry
 * while a deploy this session saw queued is on its way out — so a page that
 * is open settles with it, and neither issues a second request.
 */
export function liveViewQuery(forgeProject: string) {
  return {
    queryKey: forgeCloudKeys.liveView(forgeProject),
    queryFn: async (): Promise<LiveViewResult> => {
      try {
        return {
          availability: "available" as const,
          envs: await getLiveView(forgeProject),
          detail: "",
        };
      } catch (err) {
        return { availability: liveAvailabilityFromError(err), envs: [], detail: liveErrorDetail(err) };
      }
    },
    staleTime: 15_000,
    retry: false,
  };
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
    void queryClient.invalidateQueries({ queryKey: forgeCloudKeys.liveView.all });
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
    queryKey: forgeCloudKeys.cloudStatus(environmentId),
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
    queryKey: forgeCloudKeys.cloudPromotions(environmentId ?? ""),
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
    queryKey: forgeCloudKeys.convergences(environmentId ?? ""),
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
/**
 * The forge name Reliant persisted on a project row: the selected project's
 * when it is the one asked about, else the project list's row.
 */
function usePersistedForgeProjectName(projectId: string | null | undefined): string | null {
  const current = useProjectStore((state) =>
    projectId && state.currentProject?.id === projectId ? (state.currentProject.forge_project_name ?? null) : undefined,
  );
  const listed = useProject(projectId);
  if (!projectId) return null;
  return current !== undefined ? current : (listed?.forge_project_name ?? null);
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
  const persistedName = usePersistedForgeProjectName(projectId);
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

/**
 * THE ROSTER: backend environments, then the ones only the checkout declares.
 *
 * The backend half (GetLiveView) renders on its own; the daemon's topology is
 * queried in PARALLEL and only ever ADDS labelled "not registered" entries, so
 * an asleep daemon costs the list nothing it could show. See
 * services/forge/roster.ts.
 *
 * Lifecycle hints come from env-status reports ALREADY IN THE CACHE — this
 * hook never asks the daemon for one. An env page that read dev's status
 * (forge says `lifecycle: local`) re-files dev under "Local" too. The topology
 * row itself carries the declared lifecycle (forge >= f219804d), so a fresh
 * load already labels envs correctly without either.
 */
export function useForgeRoster(projectId: string | null | undefined, forgeProject?: string | null) {
  const persistedName = usePersistedForgeProjectName(projectId);
  const topology = useForgeTopology(projectId);
  // A forge project the URL names directly (see components/Forge/Console/consoleContext)
  // is the control plane's own key: nothing to resolve, and no daemon to ask
  // (the caller passes no project id then).
  const projectName: ForgeProjectName = forgeProject
    ? { name: forgeProject, source: "project" }
    : resolveForgeProjectName(persistedName, topology.data);
  const live = useLiveView(projectName.name);

  // SUBSCRIBE to each declared env's cached status without ever fetching one
  // (`enabled: false`), so the sidebar re-files dev under Local the moment its
  // page has read forge's answer.
  const topologyNames = topology.data?.kind === "report"
    ? environments(topology.data.report).map((env) => env.env)
    : [];
  const cachedStatuses = useQueries({
    queries: topologyNames.map((env) =>
      forgeServiceQueries.getEnvStatus(envStatusRequest(projectId, env), {
        select: envStatusOutcomeOf,
        enabled: false,
      })
    ),
  });
  const localKey = cachedStatuses
    .map((result, index) =>
      result.data?.kind === "report" && isLocalLifecycle(result.data.report) ? topologyNames[index] : null
    )
    .filter((name): name is string => !!name)
    .sort()
    .join(",");

  const envs = useMemo(
    () =>
      forgeEnvRoster(
        live.data?.envs ?? [],
        topology.data?.kind === "report" ? environments(topology.data.report) : [],
        { localByForge: new Set(localKey === "" ? [] : localKey.split(",")) }
      ),
    [live.data, topology.data, localKey]
  );

  return {
    envs,
    live,
    topology,
    projectName,
    daemon: daemonSideOf(topology.data, topology.error),
    /** Nothing from the backend yet. The daemon's half never blocks. */
    isLoading: !!projectName.name && live.isLoading && !live.data,
    /** No forge project name is known at all — neither persisted nor reported yet. */
    resolvingName: !projectName.name && topology.isLoading,
  };
}

/** Invalidate everything the control plane reports for a project after a write moved it. */
export function useInvalidateCloudEnvironments() {
  const queryClient = useQueryClient();
  return useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: forgeCloudKeys.cloudEnvs.all });
    void queryClient.invalidateQueries({ queryKey: forgeCloudKeys.orgEnvs.all });
    void queryClient.invalidateQueries({ queryKey: forgeCloudKeys.cloudStatus.all });
    void queryClient.invalidateQueries({ queryKey: forgeCloudKeys.cloudPromotions.all });
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
  return useGetEnvShape({ projectId: projectId ?? "", env: env ?? "" }, {
    select: envShapeOutcomeOf,
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
  return useGetEnvStatus(envStatusRequest(projectId, env), {
    select: envStatusOutcomeOf,
    enabled: !!projectId && !!env,
    staleTime: 10_000,
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
  return usePlanPromote({ projectId: projectId ?? "", env: env ?? "", release: release ?? "" }, {
    select: promotePlanOutcomeOf,
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
 * Dropping back to `not_verified` is the honest state. The generated
 * useApplyPromote invalidates every ForgeService read, the topology among them;
 * the control-plane reads it cannot reach are named here.
 *
 * A REFUSAL IS DATA. It arrives as a FailedPrecondition carrying a
 * ForgePromoteRefusal detail (promoteRefusalOf), and resolves this mutation as
 * `{ kind: "refused" }` — and because the generated write rejected, nothing is
 * invalidated: the cached topology is still an accurate picture of a binding
 * this call did not touch.
 */
export function useApplyForgePromote(projectId: string | null | undefined) {
  const queryClient = useQueryClient();
  const { mutateAsync: applyPromote } = useApplyPromote({
    retry: false,
    onSuccess: () => {
      invalidateCloudAfterWrite(queryClient);
      // The Releases pages: every ledger and rollout, and the live view, moved.
      void queryClient.invalidateQueries({ queryKey: releaseKeys.root });
      void queryClient.invalidateQueries({ queryKey: forgeCloudKeys.liveView.all });
    },
  });

  return useMutation<ApplyPromoteResult, Error, ForgePromotePlan>({
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
      const request = applyPromoteRequest({
        projectId: projectId as string,
        env: plan.env as string,
        release: (plan.target?.release ?? plan.release) as string,
        token,
      });
      try {
        return appliedPromoteOf(await applyPromote(request));
      } catch (error) {
        const refusal = promoteRefusalOf(error);
        if (refusal) return { kind: "refused", refusal };
        throw error;
      }
    },
    retry: false,
  });
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
  return usePlanDeploy({ projectId: projectId ?? "", env: env ?? "" }, {
    select: deployPlanOutcomeOf,
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
}

export function useStartForgeDeploy(projectId: string | null | undefined) {
  const queryClient = useQueryClient();
  // The generated write invalidates every ForgeService read (the topology
  // among them) whenever the rpc answers. A refusal rejects it, so a refusal
  // invalidates nothing.
  const { mutateAsync: startDeploy } = useStartDeploy({ retry: false });

  return useMutation<StartDeployResult, Error, StartDeployArgs>({
    mutationFn: async ({ guardPlan, approval }: StartDeployArgs) => {
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
      const request = startDeployRequest({
        projectId: projectId as string,
        env: guardPlan.env as string,
        token,
        approval,
      });
      try {
        return startDeployResultOf(await startDeploy(request));
      } catch (error) {
        const refusal = deployRefusalOf(error);
        if (refusal) return { kind: "refused", refusal };
        throw error;
      }
    },
    retry: false,
    onSuccess: (result) => {
      // A refusal applied nothing and a not-started reply started nothing, so in
      // both cases the control plane still describes a cluster this call did
      // not touch.
      if (result.kind !== "started") return;
      invalidateCloudAfterWrite(queryClient);
    },
  });
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
 *
 * NOT THE GENERATED useStartDeployPlan, for that reason: its service-wide
 * invalidation would refetch the topology and re-probe every mounted env
 * status at the moment a build starts, for a call that changed neither. So the
 * call goes through the generated client and invalidates nothing.
 *
 * A refusal (another deploy of the env in flight) arrives as StartDeploy's
 * does, and resolves as `{ kind: "refused" }`.
 */
export function useStartForgeDeployPlan(projectId: string | null | undefined) {
  return useMutation<StartDeployResult, Error, { env: string }>({
    mutationFn: async ({ env }) => {
      try {
        return startDeployPlanResultOf(await forgeClient.startDeployPlan({ projectId: projectId as string, env }));
      } catch (error) {
        const refusal = deployRefusalOf(error);
        if (refusal) return { kind: "refused", refusal };
        throw error;
      }
    },
    retry: false,
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
  return useGetDeployStatus({ projectId: projectId ?? "", handle: handle ?? "" }, {
    select: deployStatusOf,
    enabled: !!projectId && !!handle,
    // 2s: fast enough that a short deploy does not sit on a stale spinner, slow
    // enough that a twelve-deployment rollout is not hundreds of daemon round
    // trips.
    // The interval reads the cached RESPONSE, before select: RUNNING is the
    // one status deployStatusOf maps to `running`.
    refetchInterval: (query) =>
      query.state.data && query.state.data.jobStatus !== ForgeDeployJobStatus.RUNNING ? false : 2_000,
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
export function managedSecretsQueryOptions(
  projectId: string | null | undefined,
  env: string | null | undefined,
  target: ManagedStoreTarget | null
) {
  const environmentId = target?.kind === "lookup" ? target.environmentId : "";
  return {
    queryKey: [...forgeCloudKeys.managedSecrets(projectId ?? "", env ?? ""), target?.kind ?? "", environmentId],
    queryFn: async (): Promise<{ availability: ManagedStoreAvailability } & ManagedSecretListing> => {
      // `protected: false` when nothing was read: an environment whose store
      // could not be read offers no write at all, so there is nothing for a
      // protection badge to qualify.
      if (!target) return { availability: "unreachable" as const, secrets: [], protected: false };
      if (target.kind === "none") return { availability: target.availability, secrets: [], protected: false };
      try {
        return { availability: "available" as const, ...(await listSecrets(target.environmentId)) };
      } catch (err) {
        // The error object itself is NOT propagated into the cache or into any
        // message. Only the classification survives — a Connect error's detail
        // string is the kind of place a request echo could end up.
        return { availability: availabilityFromError(err), secrets: [], protected: false };
      }
    },
    // A cloud-only project has no local project id. Its live environment id
    // is the lookup authority; keep project/env/org cache dimensions intact.
    enabled: !!env && !!target && (target.kind !== "lookup" || !!environmentId),
    staleTime: 5_000,
    retry: forgeRetry,
  };
}

export function useManagedSecrets(
  projectId: string | null | undefined,
  env: string | null | undefined,
  target: ManagedStoreTarget | null
) {
  return useQuery(managedSecretsQueryOptions(projectId, env, target));
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
    queryKey: forgeCloudKeys.managedSecretVersions(projectId ?? "", env ?? "", name ?? ""),
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
export function useInvalidateManagedSecrets(
  projectId: string | null | undefined,
  env: string | null | undefined
) {
  const queryClient = useQueryClient();
  return useCallback(() => {
    void queryClient.invalidateQueries({
      queryKey: forgeCloudKeys.managedSecrets(projectId ?? "", env ?? ""),
    });
    // The versions queries are per-name; invalidating the prefix catches
    // whichever detail panel happens to be open.
    void queryClient.invalidateQueries({
      queryKey: [...forgeCloudKeys.managedSecretVersions.all, projectId ?? "", env ?? ""],
    });
    // The declared names ride on the environment record, and setting a secret
    // that was declared-unset changes that row's origin on the next read.
    void queryClient.invalidateQueries({ queryKey: forgeCloudKeys.liveView.all });
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


// ── Stop / start / delete (hosted environments) ─────────────────────────────

/** A run-state write changes what GetStatus and GetLiveView report, so both are refetched. */
function useInvalidateAfterRunStateWrite() {
  const queryClient = useQueryClient();
  return useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: forgeCloudKeys.cloudStatus.all });
    void queryClient.invalidateQueries({ queryKey: forgeCloudKeys.liveView.all });
  }, [queryClient]);
}

export function useSetEnvironmentRunState(environmentId: string | null | undefined) {
  const invalidate = useInvalidateAfterRunStateWrite();
  return useMutation<void, Error, RunStateRequest>({
    mutationFn: (state) => setEnvironmentRunState(environmentId ?? "", state),
    onSuccess: invalidate,
  });
}

/** Stop or start one hosted workload (its deployment's run state). */
export function useSetWorkloadRunState() {
  const invalidate = useInvalidateAfterRunStateWrite();
  return useMutation<void, Error, { deploymentId: string; state: RunStateRequest }>({
    mutationFn: ({ deploymentId, state }) => setWorkloadRunState(deploymentId, state),
    onSuccess: invalidate,
  });
}

export function useDeleteEnvironment(environmentId: string | null | undefined) {
  const queryClient = useQueryClient();
  return useMutation<void, Error, void>({
    mutationFn: () => deleteEnvironment(environmentId ?? ""),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: forgeCloudKeys.liveView.all });
      void queryClient.invalidateQueries({ queryKey: forgeCloudKeys.cloudEnvs.all });
      void queryClient.removeQueries({ queryKey: forgeCloudKeys.cloudStatus(environmentId ?? "") });
    },
  });
}
