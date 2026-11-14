// Copyright (c) 2025 Reliant Labs

import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";

import { readSetting, upsertStringSetting } from "../lib/settingsPersistence";
import { settingsSync } from "../services/settingsSync";
import {
  ORG_FUNDING_KEY,
  orgFundingFor,
  parseOrgFunding,
  resetOrgFunding,
  withOrgFunding,
  type OnOrgLimit,
  type OrgFundingPreference,
  type OrgFundingSettings,
} from "../lib/orgFunding";

export const orgFundingKey = ["provider-org-funding"] as const;

/**
 * The person's per-org funding choices (the setting `provider.org_funding`):
 * one org's preference, and setters that write the whole setting back. `orgIds`
 * are the orgs the person is in now: an entry for any other org is dropped on
 * the next write, as the server does (§10.4).
 */
export function useOrgFunding(orgId: string | undefined, orgIds: readonly string[]) {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: orgFundingKey,
    queryFn: async () => {
      const read = await readSetting(ORG_FUNDING_KEY);
      return parseOrgFunding(read.status === "found" ? read.value : undefined);
    },
    staleTime: 30_000,
  });
  const keep = (id: string) => orgIds.includes(id);
  const m = useMutation({
    mutationFn: async (next: OrgFundingSettings) => {
      await upsertStringSetting(ORG_FUNDING_KEY, JSON.stringify(next));
    },
    onMutate: async (next) => {
      await qc.cancelQueries({ queryKey: orgFundingKey });
      const prev = qc.getQueryData<OrgFundingSettings>(orgFundingKey);
      qc.setQueryData<OrgFundingSettings>(orgFundingKey, next);
      return { prev };
    },
    // A refused write (an agent's credential, or a failed request) shows what the server holds.
    onError: (_e, _next, ctx) => {
      qc.setQueryData(orgFundingKey, ctx?.prev);
      void qc.invalidateQueries({ queryKey: orgFundingKey });
    },
  });
  const settings = q.data ?? {};
  return {
    loading: q.isLoading,
    preference: orgId ? orgFundingFor(settings, orgId) : undefined,
    set: (patch: Partial<OrgFundingPreference>) =>
      orgId ? m.mutateAsync(withOrgFunding(settings, orgId, patch, keep)) : Promise.resolve(),
    reset: () => (orgId ? m.mutateAsync(resetOrgFunding(settings, orgId, keep)) : Promise.resolve()),
    saving: m.isPending,
    error: m.error,
  };
}

/**
 * After "Don't ask again" in the chat's question, the server wrote the
 * preference itself (ResolvePersonalFundingChoice with remember): mirror it
 * here, so Settings and the local settings cache show it without a reload.
 */
export function rememberOrgFundingLocally(qc: QueryClient, orgId: string, onOrgLimit: OnOrgLimit): void {
  const cached = settingsSync.getSettingFromCache(ORG_FUNDING_KEY)?.value;
  const current = qc.getQueryData<OrgFundingSettings>(orgFundingKey) ?? parseOrgFunding(cached);
  const next = withOrgFunding(current, orgId, { onOrgLimit });
  settingsSync.updateCache(ORG_FUNDING_KEY, JSON.stringify(next));
  qc.setQueryData<OrgFundingSettings>(orgFundingKey, next);
}
