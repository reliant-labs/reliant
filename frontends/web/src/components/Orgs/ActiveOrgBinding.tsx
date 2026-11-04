// Copyright (c) 2025 Reliant Labs

import { useEffect, useRef } from "react";
import { useQueryClient } from "@tanstack/react-query";

import { useActiveOrg } from "@/hooks/useOrgs";
import { useAuthStore } from "@/store/authStore";
import {
  ACTIVE_ORG_STORAGE_PREFIX,
  ORG_SCOPED_QUERY_ROOTS,
  adoptPersistedActiveOrg,
  bindActiveOrgUser,
  useActiveOrgStore,
  forgetChosenOrg,
  setEffectiveOrg,
} from "@/store/activeOrgStore";

/**
 * Keeps the transport's active org true. Mounted once at the root; renders nothing.
 *
 *   1. binds the store to the signed-in user, so their saved choice loads
 *      before the first org-scoped query fires (during render, not in an effect:
 *      children's queries run in their own effects, and the transport must
 *      already see the org);
 *   2. reconciles the saved choice against the live membership list. A person
 *      who left the org they had selected falls back to the server's default
 *      rather than keep sending an org the control plane would refuse with
 *      `not_org_member`;
 *   3. when the effective org changes (a switch, a reconcile, another tab),
 *      drops the org-scoped query caches so no screen keeps showing the
 *      previous org's answers.
 *
 * The effective org is written to the store, not just derived: the transport
 * reads the store, so a reconciled choice must land there too.
 */
export function ActiveOrgBinding() {
  const userId = useAuthStore((s) => s.user?.id ?? null);
  bindActiveOrgUser(userId);

  const qc = useQueryClient();
  const { activeOrgId, orgs, isLoading, error } = useActiveOrg();
  const chosenId = useActiveOrgStore((s) => s.chosenId);
  const settled = useRef<string | null>(null);
  const previousUser = useRef(userId);

  useEffect(() => {
    if (previousUser.current !== userId) {
      previousUser.current = userId;
      settled.current = null;
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
    const switched = settled.current !== null && settled.current !== activeOrgId;
    const reconciled = used !== null && used !== activeOrgId;
    if (switched || reconciled) {
      for (const root of ORG_SCOPED_QUERY_ROOTS) void qc.resetQueries({ queryKey: [root] });
    }
    settled.current = activeOrgId;
  }, [userId, activeOrgId, chosenId, orgs.length, isLoading, error, qc]);

  // Another tab chose a different org: adopt it, so this tab does not keep
  // acting in the one the person just left.
  useEffect(() => {
    const onStorage = (e: StorageEvent) => {
      if (e.key?.startsWith(ACTIVE_ORG_STORAGE_PREFIX)) adoptPersistedActiveOrg();
    };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, []);

  return null;
}
