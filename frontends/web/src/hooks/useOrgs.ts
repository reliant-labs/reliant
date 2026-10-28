// Copyright (c) 2025 Reliant Labs

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { hasControlPlane } from "@/services/controlPlane/config";
import {
  listMyOrganizations,
  orgKeys,
  resolveActiveOrg,
  setDefaultOrganization,
  type MyOrg,
  type MyOrgs,
} from "@/services/controlPlane/orgs";
import { chooseActiveOrg, useActiveOrgStore } from "@/store/activeOrgStore";

/** Every org the caller belongs to (human sessions; a machine credential gets none). */
export function useMyOrgs() {
  return useQuery<MyOrgs>({
    queryKey: orgKeys.mine,
    queryFn: listMyOrganizations,
    enabled: hasControlPlane,
    staleTime: 60_000,
    // Leaving an org elsewhere must show up when the person comes back.
    refetchOnWindowFocus: true,
  });
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
  /** Act in `orgId` in this app (not the server-side default). */
  select: (orgId: string) => void;
}

/** The active org, resolved against the live membership list. */
export function useActiveOrg(): ActiveOrg {
  const q = useMyOrgs();
  const chosen = useActiveOrgStore((s) => s.chosenId);
  const data = q.data;
  const activeOrgId = data ? resolveActiveOrg(data, chosen) : null;
  return {
    orgs: data?.orgs ?? [],
    active: data?.orgs.find((o) => o.id === activeOrgId) ?? null,
    activeOrgId,
    defaultOrgId: data?.defaultOrgId ?? "",
    canSwitch: (data?.orgs.length ?? 0) > 1,
    isLoading: q.isLoading,
    error: q.error,
    select: chooseActiveOrg,
  };
}

/** Make `orgId` the server-side default: what the CLI and other devices act in too. */
export function useSetDefaultOrg() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: setDefaultOrganization,
    onSuccess: () => qc.invalidateQueries({ queryKey: orgKeys.mine }),
  });
}
