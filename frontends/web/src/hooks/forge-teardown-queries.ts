// Copyright (c) 2025 Reliant Labs

/**
 * The environment teardown flows the env header and the Environments page
 * run over existing control-plane rpcs (control-plane
 * docs/design/unified-shell-and-projects.md, Lifecycle, PR L1):
 *
 *   the databases a keep-data delete left behind (ListRetainedDatabases), and
 *   purging one (DeleteRetainedDatabase).
 *
 * There is deliberately NO combined "unbind domains and delete". The backend
 * unbinds by domain id, and a rebind updates the binding in place, so a domain
 * rebound to another environment between the list and the unbind would be
 * taken offline there. Until the unbind can name the environment it expects
 * (a control-plane change), unbinding stays on the Domains page.
 */

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  hasCloudControlPlane,
  listRetainedDatabases,
  purgeRetainedDatabase,
  retainedReadFailureOf,
  type RetainedDatabase,
} from "@/services/forge/cloudEnvs";
import { orgQueryEnabled, useOrgScope, withOrgFailure } from "@/lib/orgScope";

import { forgeCloudKeys } from "./forge-queries";

// ── Retained databases ──────────────────────────────────────────────────────

/**
 * Retries of a transient failure of the kept-databases read before it is
 * reported. A refusal (no access, no rpc) is never retried: asking again
 * gets the same answer. Each poll (refetchInterval) asks afresh, so a failure
 * that clears is picked up on the next one.
 */
export const RETAINED_READ_RETRIES = 2;

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
        retry: (failureCount: number, error: Error) =>
          retainedReadFailureOf(error) === "unavailable" && failureCount < RETAINED_READ_RETRIES,
        retryDelay: (attempt: number) => Math.min(500 * 2 ** attempt, 2_000),
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
