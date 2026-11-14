// Copyright (c) 2025 Reliant Labs

/**
 * hooks/useOrgs.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): nobody is signed in, so there are no orgs and
 * the console's org switcher renders nothing. The real hook reads the signed-in
 * user from store/authStore, which the demo bundle must not contain.
 */

import { useMutation, useQuery } from "@tanstack/react-query";

import { orgKeys } from "@/services/controlPlane/orgs";

import type * as Real from "./useOrgs";

export type ActiveOrg = Real.ActiveOrg;

export const useMyOrgs: typeof Real.useMyOrgs = (client) =>
  useQuery({ queryKey: orgKeys.mine, queryFn: () => Promise.reject(new Error("The forge demo has no organizations.")), enabled: false }, client);

export const useActiveOrg: typeof Real.useActiveOrg = () => ({
  orgs: [],
  active: null,
  activeOrgId: null,
  defaultOrgId: "",
  canSwitch: false,
  isLoading: false,
  error: null,
  select: () => {},
});

export const useSetDefaultOrg: typeof Real.useSetDefaultOrg = () =>
  useMutation({
    mutationFn: async (_orgId: string): Promise<void> => {
      throw new Error("The forge demo has no organizations.");
    },
  });
