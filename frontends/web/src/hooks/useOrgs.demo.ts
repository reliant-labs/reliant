// Copyright (c) 2025 Reliant Labs

/**
 * hooks/useOrgs.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): nobody signs in to the demo, so the org it acts
 * in is the sample shop's (fixtures/shop/shopStory.ts DEMO_ORG, "Acme", the org
 * the shop pack's OrganizationService names), answered at once from these
 * constants: no request, nothing to switch to (`canSwitch` false; `select` a
 * no-op), so the nav's org row names Acme the way the signed-in app names a
 * person's org. The real hook reads the signed-in user from lib/auth, which
 * the demo bundle must not contain.
 *
 * The membership list itself (useMyOrgs) stays unread: what reads it is the
 * funding and billing prompts, which have nothing to say on the demo.
 */

import type { MessageInitShape } from "@bufbuild/protobuf";
import type { ConnectClientError } from "@reliantlabs/forge-web-runtime";
import { useMutation, useQuery } from "@tanstack/react-query";

import { DEMO_ORG } from "@/fixtures/shop/shopStory";
import type {
  SetDefaultOrganizationRequestSchema,
  SetDefaultOrganizationResponse,
} from "@/gen/controlplane/services/organization/v1/organization_pb";
import { organizationServiceQueries } from "@/hooks/controlplane/organization-service-hooks_gen";
import { toMyOrgs, type MyOrg } from "@/services/controlPlane/orgs";

import type * as Real from "./useOrgs";

export type ActiveOrg = Real.ActiveOrg;

/** The sample org: the visitor is an admin in it, as the shop pack's identity says. */
const DEMO_MY_ORG: MyOrg = Object.freeze({ id: DEMO_ORG, name: "Acme", slug: "acme", role: "admin", isOwner: false, isDefault: true });

const DEMO_ACTIVE_ORG: ActiveOrg = Object.freeze({
  orgs: [DEMO_MY_ORG],
  active: DEMO_MY_ORG,
  activeOrgId: DEMO_ORG,
  defaultOrgId: DEMO_ORG,
  canSwitch: false,
  isLoading: false,
  error: null,
  refetch: () => {},
  select: () => {},
});

export const useMyOrgs: typeof Real.useMyOrgs = (client) =>
  useQuery(organizationServiceQueries.listMyOrganizations({}, { enabled: false, select: toMyOrgs }), client);

export const useActiveOrg: typeof Real.useActiveOrg = () => DEMO_ACTIVE_ORG;

export const useSetDefaultOrg: typeof Real.useSetDefaultOrg = () =>
  useMutation<SetDefaultOrganizationResponse, ConnectClientError, MessageInitShape<typeof SetDefaultOrganizationRequestSchema>>({
    mutationFn: async () => {
      throw new Error("The forge demo's organization cannot be changed.");
    },
  });
