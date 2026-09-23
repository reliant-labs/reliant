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
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { forgeGrpc, type ApplyPromoteResult, type DeployStatus, type StartDeployResult } from "../api/forge-grpc";
import type { ForgeAuditReport } from "../services/forge/audit";
import { deployTokenFor, type ForgeDeployReport } from "../services/forge/deploy";
import { confirmationTokenFor, type ForgePromotePlan } from "../services/forge/promote";
import type { ForgeSecretsReport } from "../services/forge/secrets";
import {
  availabilityFromError,
  deleteSecret,
  destroySecret,
  getSecretVersions,
  hasControlPlane,
  listSecrets,
  setSecret,
  undeleteSecret,
  type ManagedSecretHistory,
  type ManagedSecretSummary,
  type ManagedStoreAvailability,
  type SetSecretResult,
} from "../services/forge/secretStore";
import type { ForgeEnvStatusReport } from "../services/forge/status";
import {
  mergeVerifyIntoTopology,
  type ForgeOutcome,
  type ForgeTopologyReport,
  type ForgeVerifyReport,
} from "../services/forge/topology";

// ── Key factory ─────────────────────────────────────────────────────────────

export const forgeKeys = {
  all: ["forge"] as const,
  topology: (projectId: string) => [...forgeKeys.all, "topology", projectId] as const,
  audit: (projectId: string) => [...forgeKeys.all, "audit", projectId] as const,
  secrets: (projectId: string, env: string) =>
    [...forgeKeys.all, "secrets", projectId, env] as const,
  envStatus: (projectId: string, env: string) =>
    [...forgeKeys.all, "env-status", projectId, env] as const,
  promotePlan: (projectId: string, env: string, release: string) =>
    [...forgeKeys.all, "promote-plan", projectId, env, release] as const,
  deployPlan: (projectId: string, env: string) =>
    [...forgeKeys.all, "deploy-plan", projectId, env] as const,
  deployStatus: (projectId: string, handle: string) =>
    [...forgeKeys.all, "deploy-status", projectId, handle] as const,
  managedSecrets: (projectId: string, env: string) =>
    [...forgeKeys.all, "managed-secrets", projectId, env] as const,
  managedSecretVersions: (projectId: string, env: string, name: string) =>
    [...forgeKeys.all, "managed-secret-versions", projectId, env, name] as const,
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
    retry: 1,
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

// ── Secrets ─────────────────────────────────────────────────────────────────

/**
 * useForgeSecrets loads one environment's secret PRESENCE report.
 *
 * retry is 1 for the same reason as the topology hook — every meaningful
 * non-success arrives as a successful response carrying data — but here there is
 * a second reason that is specific to this surface: a retry path is a place where
 * a response body tends to end up logged or attached to an error for diagnosis.
 * Nothing on this path may do that. The response is passed straight to the cache
 * and never stringified, and the query has no onError side channel.
 *
 * staleTime is short (5s) rather than the topology's 30s because presence is the
 * thing a developer is actively changing: they read this screen, run
 * `forge secret set`, and come back expecting the row to have moved. A long
 * stale window would show them the answer from before their edit.
 */
export function useForgeSecrets(projectId: string | null | undefined, env: string | null | undefined) {
  return useQuery<ForgeOutcome<ForgeSecretsReport>>({
    queryKey: forgeKeys.secrets(projectId ?? "", env ?? ""),
    queryFn: () =>
      forgeGrpc.listSecrets(projectId as string, env as string) as Promise<
        ForgeOutcome<ForgeSecretsReport>
      >,
    enabled: !!projectId && !!env,
    staleTime: 5_000,
    retry: 1,
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
    retry: 1,
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
    retry: 1,
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
    retry: 1,
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
    retry: 1,
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
export function useStartForgeDeploy(projectId: string | null | undefined) {
  const queryClient = useQueryClient();

  const mutation = useMutation<StartDeployResult, Error, ForgeDeployReport>({
    mutationFn: async (plan: ForgeDeployReport) => {
      const token = deployTokenFor(plan);
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
        env: plan.env as string,
        token,
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
    },
  });

  return mutation;
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
    retry: 1,
  });
}

// ── Managed secret store ────────────────────────────────────────────────────
//
// These hooks talk to control-plane's SecretStoreService (Connect), not to the
// daemon. They are a separate axis from useForgeSecrets, which reads forge's
// own declaration report — see services/forge/secretSurface.ts for why the
// screen needs both and how they are joined.

/**
 * useManagedSecrets loads the managed store's metadata for one (project, env).
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
 * staleTime is short (5s) for the same reason useForgeSecrets uses 5s — this is
 * the thing the user is actively changing. They set a secret and come straight
 * back expecting the row to have moved.
 */
export function useManagedSecrets(
  projectId: string | null | undefined,
  env: string | null | undefined
) {
  return useQuery<{ availability: ManagedStoreAvailability; secrets: ManagedSecretSummary[] }>({
    queryKey: forgeKeys.managedSecrets(projectId ?? "", env ?? ""),
    queryFn: async () => {
      if (!hasControlPlane()) {
        return { availability: "no-control-plane" as const, secrets: [] };
      }
      try {
        return {
          availability: "available" as const,
          secrets: await listSecrets(projectId as string, env as string),
        };
      } catch (err) {
        // The error object itself is NOT propagated into the cache or into any
        // message. Only the classification survives — a Connect error's detail
        // string is the kind of place a request echo could end up.
        return { availability: availabilityFromError(err), secrets: [] };
      }
    },
    enabled: !!projectId && !!env,
    staleTime: 5_000,
    retry: 1,
  });
}

/**
 * useManagedSecretVersions loads ONE secret's version history.
 *
 * Enabled only when a name is selected, so opening the detail panel is what
 * fetches the history rather than the list page pre-fetching every secret's —
 * which on a project with fifty secrets would be fifty round trips to render a
 * table nobody has opened yet.
 */
export function useManagedSecretVersions(
  projectId: string | null | undefined,
  env: string | null | undefined,
  name: string | null | undefined
) {
  return useQuery<ManagedSecretHistory>({
    queryKey: forgeKeys.managedSecretVersions(projectId ?? "", env ?? "", name ?? ""),
    queryFn: () => getSecretVersions(projectId as string, env as string, name as string),
    enabled: !!projectId && !!env && !!name,
    staleTime: 5_000,
    retry: 1,
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
    // forge's own declaration report can also move: setting a secret that was
    // declared-unset changes the row's origin on the next read.
    void queryClient.invalidateQueries({
      queryKey: forgeKeys.secrets(projectId ?? "", env ?? ""),
    });
  }, [queryClient, projectId, env]);
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
  env: string | null | undefined
) {
  const invalidate = useInvalidateManagedSecrets(projectId, env);
  return useMutation<SetSecretResult, Error, { name: string; value: string; cas?: number }>({
    mutationFn: ({ name, value, cas }) =>
      setSecret({ projectId: projectId as string, env: env as string, name, value, cas }),
    onSuccess: invalidate,
  });
}

/** useDeleteManagedSecret soft-deletes. Recoverable — see useUndeleteManagedSecret. */
export function useDeleteManagedSecret(
  projectId: string | null | undefined,
  env: string | null | undefined
) {
  const invalidate = useInvalidateManagedSecrets(projectId, env);
  return useMutation<void, Error, { name: string; versions?: number[] }>({
    mutationFn: ({ name, versions }) =>
      deleteSecret({ projectId: projectId as string, env: env as string, name, versions }),
    onSuccess: invalidate,
  });
}

export function useUndeleteManagedSecret(
  projectId: string | null | undefined,
  env: string | null | undefined
) {
  const invalidate = useInvalidateManagedSecrets(projectId, env);
  return useMutation<void, Error, { name: string; versions: number[] }>({
    mutationFn: ({ name, versions }) =>
      undeleteSecret({ projectId: projectId as string, env: env as string, name, versions }),
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
  env: string | null | undefined
) {
  const invalidate = useInvalidateManagedSecrets(projectId, env);
  return useMutation<void, Error, { name: string; versions: number[] }>({
    mutationFn: ({ name, versions }) =>
      destroySecret({ projectId: projectId as string, env: env as string, name, versions }),
    onSuccess: invalidate,
  });
}
