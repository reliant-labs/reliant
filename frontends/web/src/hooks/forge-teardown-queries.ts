// Copyright (c) 2025 Reliant Labs

/**
 * The environment teardown flows the env header and the Environments page
 * run over existing control-plane rpcs (control-plane
 * docs/design/unified-shell-and-projects.md, Lifecycle, PR L1):
 *
 *   - the databases a keep-data delete left behind (ListRetainedDatabases),
 *     and purging one (DeleteRetainedDatabase);
 *   - "Unbind domains and delete": DeleteEnvironment is refused while a custom
 *     domain is bound, so this unbinds each one (DeleteDomainBinding) and then
 *     deletes, one step at a time, reporting each step.
 *
 * The domains stay in the organization, unbound: the delete removes the
 * environment, not the org's domains or their verification.
 */

import { useCallback, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  cloudErrorDetail,
  deleteEnvironment,
  hasCloudControlPlane,
  listRetainedDatabases,
  purgeRetainedDatabase,
  type RetainedDatabase,
} from "@/services/forge/cloudEnvs";
import { listDomains, unbindDomain } from "@/services/forge/domains";
import { orgQueryEnabled, useOrgScope, whenOrgScopeReady, withOrgFailure } from "@/lib/orgScope";

import { domainKeys } from "./forge-domain-queries";
import { forgeRetry } from "./forge-model";
import { forgeCloudKeys, useInvalidateAfterEnvironmentDelete, type DeleteEnvironmentVars } from "./forge-queries";

// ── Retained databases ──────────────────────────────────────────────────────

/**
 * Every database the tab's org kept from a deleted environment: read in that
 * org, keyed by it, and not before it is known (lib/orgScope). A refusal (no
 * access, an older control plane) is the query's error.
 */
export function useRetainedDatabases({
  enabled = true,
  refetchInterval = false,
}: { enabled?: boolean; refetchInterval?: number | false } = {}) {
  const org = useOrgScope();
  return useQuery<RetainedDatabase[]>(
    withOrgFailure(
      {
        queryKey: forgeCloudKeys.retainedDatabases(org.orgId),
        queryFn: () => listRetainedDatabases(org.orgId),
        enabled: enabled && hasCloudControlPlane() && orgQueryEnabled(org),
        staleTime: 30_000,
        // While a keep-data delete is being confirmed (keptDataChecks.ts).
        refetchInterval,
        // A control plane without the rpc answers the same on a retry.
        retry: (failureCount: number, error: Error) =>
          !(error instanceof ConnectError && error.code === Code.Unimplemented) && forgeRetry(failureCount, error),
      },
      org
    )
  );
}

/** Purge one kept database. The row stays listed until the control plane has destroyed its volumes. */
export function usePurgeRetainedDatabase() {
  const queryClient = useQueryClient();
  return useMutation<void, Error, { id: string }>({
    mutationFn: ({ id }) => purgeRetainedDatabase(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: forgeCloudKeys.retainedDatabases.all });
    },
  });
}

// ── Unbind domains, then delete ─────────────────────────────────────────────

export type TeardownStepState = "pending" | "running" | "done" | "failed";

export interface TeardownStep {
  id: string;
  label: string;
  state: TeardownStepState;
  /** The server's words, when this step failed. */
  error?: string;
}

/**
 * Unbind every custom domain bound to the environment, then delete it.
 *
 * The bindings are read AFRESH rather than from the Domains cache: the cache
 * can predate a bind, and a stale list would unbind too little and leave the
 * delete refused again. The steps run one at a time and stop at the first
 * failure, so `steps` says exactly what happened: which domains were unbound,
 * and whether the delete ran.
 */
export function useUnbindDomainsAndDelete(environmentId: string, envName: string) {
  const queryClient = useQueryClient();
  const invalidateEnv = useInvalidateAfterEnvironmentDelete(environmentId);
  const [steps, setSteps] = useState<TeardownStep[]>([]);

  const mutation = useMutation<void, Error, DeleteEnvironmentVars>({
    mutationFn: async ({ keepData }) => {
      const patch = (id: string, change: Partial<TeardownStep>) =>
        setSteps((prev) => prev.map((step) => (step.id === id ? { ...step, ...change } : step)));
      const run = async (id: string, work: () => Promise<void>) => {
        patch(id, { state: "running" });
        try {
          await work();
          patch(id, { state: "done" });
        } catch (err) {
          patch(id, { state: "failed", error: cloudErrorDetail(err) || "The request failed." });
          throw err;
        }
      };

      const read: TeardownStep = { id: "read", label: `Find the custom domains bound to ${envName}`, state: "pending" };
      setSteps([read]);
      let bound: { id: string; hostname: string }[] = [];
      await run("read", async () => {
        // In the tab's org, never unscoped: an unscoped list is every org's.
        const scope = await whenOrgScopeReady();
        bound = (await listDomains(scope.orgId))
          .filter((domain) => domain.binding?.environmentId === environmentId)
          .sort((a, b) => a.hostname.localeCompare(b.hostname));
      });

      setSteps([
        { ...read, state: "done" },
        ...bound.map((domain) => ({ id: `unbind:${domain.id}`, label: `Unbind ${domain.hostname}`, state: "pending" as const })),
        { id: "delete", label: `Delete ${envName}`, state: "pending" },
      ]);
      for (const domain of bound) {
        await run(`unbind:${domain.id}`, () => unbindDomain(domain.id));
      }
      await run("delete", () => deleteEnvironment(environmentId, { keepData }));
    },
    onSuccess: invalidateEnv,
    // Some domains may be unbound even when a later step failed.
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: domainKeys.all });
    },
  });

  const { reset: resetMutation } = mutation;
  const reset = useCallback(() => {
    resetMutation();
    setSteps([]);
  }, [resetMutation]);

  return { ...mutation, steps, reset };
}
