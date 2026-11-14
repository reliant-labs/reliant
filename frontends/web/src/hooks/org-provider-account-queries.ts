// Copyright (c) 2025 Reliant Labs

/**
 * Reads of an org's own AI accounts (OrgProviderAccountService) as frontend
 * values. Writes are the generated mutation hooks
 * (org-provider-account-service-hooks_gen.ts), which invalidate the service's
 * reads on success. Every request names its org, so the cache never mixes two
 * orgs' answers.
 */

import { hasControlPlane } from "../services/controlPlane/config";
import { useAuthStore } from "../store/authStore";
import type { ListOrgProviderAccountsResponse, ListOrgProviderWorkspacesResponse } from "../gen/reliant/v1/org_provider_account_pb";
import {
  orgAccountsFromProto,
  orgWorkspacesFromProto,
} from "../types/orgProviderAccount";
import { useListOrgProviderAccounts, useListOrgProviderWorkspaces } from "./org-provider-account-service-hooks_gen";

const accountsOf = (res: ListOrgProviderAccountsResponse) => orgAccountsFromProto(res);
const workspacesOf = (res: ListOrgProviderWorkspacesResponse) => orgWorkspacesFromProto(res);

/** The org's accounts: whole to an owner or admin, narrowed to provider, label and status for a member. */
export function useOrgProviderAccounts(orgId: string | undefined) {
  const signedIn = useAuthStore((s) => s.user !== null);
  return useListOrgProviderAccounts(
    { orgId: orgId ?? "" },
    { enabled: hasControlPlane && signedIn && !!orgId, staleTime: 30_000, select: accountsOf, retry: 1 },
  );
}

/** The team workspaces the org pays for and the seats they matched. Owners and admins only. */
export function useOrgProviderWorkspaces(orgId: string | undefined, enabled: boolean) {
  return useListOrgProviderWorkspaces(
    { orgId: orgId ?? "" },
    { enabled: hasControlPlane && enabled && !!orgId, staleTime: 30_000, select: workspacesOf, retry: 1 },
  );
}
