// Copyright (c) 2025 Reliant Labs

import { useCallback } from "react";
import { useQuery, type QueryClient } from "@tanstack/react-query";

import {
  organizationServiceQueries,
  useSetDefaultOrganization,
} from "@/hooks/controlplane/organization-service-hooks_gen";
import { hasControlPlane } from "@/services/controlPlane/config";
import { orgKeys, resolveActiveOrg, toMyOrgs, type MyOrg } from "@/services/controlPlane/orgs";
import { switchTabOrg } from "@/lib/orgSwitch";
import { chooseActiveOrg, isMachineCredentialUser, useActiveOrgStore } from "@/store/activeOrgStore";
import { useAuthStore } from "@/store/authStore";

/**
 * Every org the caller belongs to (human sessions; a machine credential gets
 * none). `client` names the QueryClient for a caller that may render outside
 * a QueryClientProvider (useMachineOrgs).
 */
export function useMyOrgs(client?: QueryClient) {
  // ListMyOrganizations is authenticated: signed out it can only answer 401.
  // It is a person's list: a machine credential (an API-key session) is
  // refused, and acts in its own org with none named (store/activeOrgStore).
  const person = useAuthStore((s) => s.user !== null && !isMachineCredentialUser(s.user.id));
  return useQuery(
    organizationServiceQueries.listMyOrganizations(
      {},
      {
        enabled: hasControlPlane && person,
        staleTime: 60_000,
        // Leaving an org elsewhere must show up when the person comes back.
        refetchOnWindowFocus: true,
        select: toMyOrgs,
      },
    ),
    client,
  );
}

export interface ActiveOrg {
  orgs: MyOrg[];
  /** The org this session acts in; null until the list has answered. */
  active: MyOrg | null;
  activeOrgId: string | null;
  defaultOrgId: string;
  /** More than one org: the only case that earns switcher chrome. */
  canSwitch: boolean;
  isLoading: boolean;
  error: Error | null;
  /** Re-read the membership list (a Retry after it failed). */
  refetch: () => void;
  /**
   * Act in `orgId` in this app (not the server-side default). On an org page
   * that is a navigation to the same page in it (lib/orgSwitch.ts).
   */
  select: (orgId: string) => void;
}

/** The active org, resolved against the live membership list. */
export function useActiveOrg(client?: QueryClient): ActiveOrg {
  const q = useMyOrgs(client);
  const chosen = useActiveOrgStore((s) => s.chosenId);
  const data = q.data;
  const activeOrgId = data ? resolveActiveOrg(data, chosen) : null;
  const { refetch } = q;
  const refetchOrgs = useCallback(() => void refetch(), [refetch]);
  const select = useCallback(
    (orgId: string) => {
      const org = data?.orgs.find((o) => o.id === orgId);
      if (org) switchTabOrg(org);
      else chooseActiveOrg(orgId);
    },
    [data],
  );
  return {
    orgs: data?.orgs ?? [],
    active: data?.orgs.find((o) => o.id === activeOrgId) ?? null,
    activeOrgId,
    defaultOrgId: data?.defaultOrgId ?? "",
    canSwitch: (data?.orgs.length ?? 0) > 1,
    isLoading: q.isLoading,
    error: q.error,
    refetch: refetchOrgs,
    select,
  };
}

/**
 * Make `{ orgId }` the server-side default: what the CLI and other devices act
 * in too. PermissionDenied for an org the caller is not in. Refreshes the
 * membership list (its `isDefault` flags), nothing else.
 */
export function useSetDefaultOrg() {
  return useSetDefaultOrganization({
    invalidate: (qc) => void qc.invalidateQueries({ queryKey: orgKeys.mine }),
  });
}
