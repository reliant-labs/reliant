// Copyright (c) 2025 Reliant Labs

import { useEffect, useRef } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";

import { useActiveOrg } from "@/hooks/useOrgs";
import { useAuthStore } from "@/store/authStore";
import {
  ORG_SCOPED_QUERY_ROOTS,
  bindActiveOrgUser,
  useActiveOrgStore,
  forgetChosenOrg,
  setEffectiveOrg,
} from "@/store/activeOrgStore";

/** What an `onSwitch` call is about. */
export interface OrgChange {
  /**
   * The tab's first resolution, from naming no org to `orgId`: nothing was
   * shown in another org, so only what was read with no org is stale.
   */
  firstLoad: boolean;
}

/**
 * Keeps the transport's active org true. Mounted once at the root; renders nothing.
 *
 *   1. binds the store to the signed-in user, so this tab's saved choice
 *      loads before the first org-scoped query fires (during render, not in an
 *      effect: children's queries run in their own effects, and the transport
 *      must already see the org);
 *   2. reconciles the saved choice against the live membership list. A person
 *      who left the org they had selected falls back to the server's default
 *      rather than keep sending an org the control plane would refuse with
 *      `not_org_member`. The answer marks the org resolved, which is what the
 *      product's lists wait for (lib/orgScope);
 *   3. when the effective org changes (a switch or a reconcile), drops the
 *      org-scoped query caches so no screen keeps showing the previous org's
 *      answers, then calls `onSwitch`: the product app's chats, projects,
 *      workflows, triggers and runs are the active org's too
 *      (components/Orgs/followActiveOrg.ts), which a shell without them (the
 *      forge shell) does not pass. The first resolution of a tab with nothing
 *      saved calls it too, with `firstLoad`, so nothing read with no org stays
 *      cached.
 *
 * The org is per tab (store/activeOrgStore): a switch in another tab never
 * reaches this one.
 *
 * The effective org is written to the store, not just derived: the transport
 * reads the store, so a reconciled choice must land there too.
 */
export function ActiveOrgBinding({
  onSwitch,
}: { onSwitch?: (orgId: string | null, client: QueryClient, change: OrgChange) => void } = {}) {
  const userId = useAuthStore((s) => s.user?.id ?? null);
  bindActiveOrgUser(userId);

  const qc = useQueryClient();
  const { activeOrgId, orgs, isLoading, error } = useActiveOrg();
  const chosenId = useActiveOrgStore((s) => s.chosenId);
  // The org the last answer settled on; undefined until the first answer.
  const settled = useRef<string | null | undefined>(undefined);
  const previousUser = useRef(userId);

  useEffect(() => {
    if (previousUser.current !== userId) {
      previousUser.current = userId;
      settled.current = undefined;
      // Never reuse another person's membership list, even across an auth
      // transition that did not go through the explicit Sign out action.
      void qc.resetQueries({ queryKey: ["orgs"] });
    }
  }, [userId, qc]);

  useEffect(() => {
    if (!userId || isLoading || error) return;
    // What the transport has been naming up to now: the saved choice, or
    // nothing. If that is not where we land, what it already fetched is stale.
    const used = useActiveOrgStore.getState().effectiveId;
    setEffectiveOrg(activeOrgId);
    // They left the org they had picked: stop remembering it.
    if (chosenId && chosenId !== activeOrgId) forgetChosenOrg();
    const switched = settled.current !== undefined && settled.current !== activeOrgId;
    const reconciled = used !== null && used !== activeOrgId;
    if (switched || reconciled) {
      for (const root of ORG_SCOPED_QUERY_ROOTS) void qc.resetQueries({ queryKey: [root] });
      onSwitch?.(activeOrgId, qc, { firstLoad: false });
    } else if (settled.current === undefined && used === null && activeOrgId !== null) {
      // The first answer of a tab that had nothing saved. The product's lists
      // waited for it (lib/orgScope), but anything that went out naming no
      // org meanwhile holds every org's rows: the product app drops it.
      onSwitch?.(activeOrgId, qc, { firstLoad: true });
    }
    settled.current = activeOrgId;
  }, [userId, activeOrgId, chosenId, orgs.length, isLoading, error, qc, onSwitch]);

  return null;
}
