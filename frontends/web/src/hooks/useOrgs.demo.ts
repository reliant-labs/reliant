// Copyright (c) 2025 Reliant Labs

/**
 * hooks/useOrgs.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): nobody is signed in, so there are no orgs and
 * the console's org switcher renders nothing. The real hook reads the signed-in
 * user from store/authStore, which the demo bundle must not contain.
 */

import type { MessageInitShape } from "@bufbuild/protobuf";
import type { ConnectClientError } from "@reliantlabs/forge-web-runtime";
import { useMutation, useQuery } from "@tanstack/react-query";

import type {
  SetDefaultOrganizationRequestSchema,
  SetDefaultOrganizationResponse,
} from "@/gen/controlplane/services/organization/v1/organization_pb";
import { organizationServiceQueries } from "@/hooks/controlplane/organization-service-hooks_gen";
import { toMyOrgs } from "@/services/controlPlane/orgs";

import type * as Real from "./useOrgs";

export type ActiveOrg = Real.ActiveOrg;

export const useMyOrgs: typeof Real.useMyOrgs = (client) =>
  useQuery(organizationServiceQueries.listMyOrganizations({}, { enabled: false, select: toMyOrgs }), client);

export const useActiveOrg: typeof Real.useActiveOrg = () => ({
  orgs: [],
  active: null,
  activeOrgId: null,
  defaultOrgId: "",
  canSwitch: false,
  isLoading: false,
  error: null,
  refetch: () => {},
  select: () => {},
});

export const useSetDefaultOrg: typeof Real.useSetDefaultOrg = () =>
  useMutation<SetDefaultOrganizationResponse, ConnectClientError, MessageInitShape<typeof SetDefaultOrganizationRequestSchema>>({
    mutationFn: async () => {
      throw new Error("The forge demo has no organizations.");
    },
  });
