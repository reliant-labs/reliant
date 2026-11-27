// Copyright (c) 2026 Reliant Labs

/**
 * Whether this org may see GTM: the nav's GTM row (ShellNavBody →
 * `NavCtx.features.gtm`) and every /gtm page (GtmShell) ask this one hook.
 *
 * The answer is the server's, GtmService.GetGtmAccess{org_id} (plan A.2): a
 * static org allowlist behind featuregate.Gate. It fails closed here too:
 * while the org or the answer is loading, when either fails, and when the tab
 * names no org, GTM is not enabled. Nothing in the page decides it; a preview
 * on fixture data sees GTM only because its transport answers GetGtmAccess.
 */

import { QueryClientContext, useQuery } from "@tanstack/react-query";
import { useContext } from "react";

import { gtmServiceQueries } from "@/hooks/gtm-service-hooks_gen";
import { useOrgScope } from "@/lib/orgScope";
import { queryClient as appQueryClient } from "@/lib/query-client";

export interface GtmAccess {
  /** The server said yes for this org. False while loading, on error and on no. */
  enabled: boolean;
  /** No answer yet: the org is resolving or GetGtmAccess is in flight. */
  loading: boolean;
  /** The org asked about; null when the tab names none (then GTM is off). */
  orgId: string | null;
}

const ACCESS_STALE_MS = 5 * 60_000;

/**
 * GTM's gate for the tab's org. `ask: false` asks nothing and answers "not
 * enabled" (a nav with no GTM area). Outside a QueryClientProvider (a nav
 * rendered on its own) it reads the app's one client, as useMachineOrgs does.
 */
export function useGtmAccess({ ask = true }: { ask?: boolean } = {}): GtmAccess {
  const client = useContext(QueryClientContext) ?? appQueryClient;
  const org = useOrgScope();
  const orgId = org.ready ? org.orgId : null;
  const asking = ask && orgId !== null;
  const q = useQuery(
    gtmServiceQueries.getGtmAccess(
      { orgId: orgId ?? "" },
      { enabled: asking, staleTime: ACCESS_STALE_MS, refetchOnWindowFocus: false },
    ),
    client,
  );
  if (!ask) return { enabled: false, loading: false, orgId };
  const orgLoading = !org.ready && org.error === null;
  return {
    enabled: asking && q.isSuccess && q.data.enabled === true,
    loading: orgLoading || (asking && q.isPending),
    orgId,
  };
}
