// Copyright (c) 2025 Reliant Labs

/**
 * The org the sample visitor acts in: shop's (shopStory.ts DEMO_ORG), the one
 * every scoped read names. ListMyOrganizations is what the console's own
 * `currentOrgId()` (services/controlPlane/orgs.ts) asks outside the org tree,
 * so the Users page and Data's "Open in Users" link find their org through
 * the real code path. GetMyOrgEntitlements says the org has ML (granted on
 * the story's clock), so the ML pages show the demo's data rather than the
 * "Reach out to Reliant for access" page; it names an org, so another org's
 * is answered empty (scope.ts). Every other OrganizationService call is
 * refused (the transport's read-only guard: RequestOrgEntitlement among
 * them) or not served (Unimplemented).
 */
import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { mockService } from "@reliantlabs/forge-web-runtime/mock-transport";

import { GetMyOrgEntitlementsResponseSchema, ListMyOrganizationsResponseSchema, OrganizationService } from "@/gen/controlplane/services/organization/v1/organization_pb";

import { DAY, DEMO_ORG, demoNow } from "../shopStory";
import { completed, withMessageRequests } from "./defaults";
import { DEMO_SCOPE, scopedToDemo, type PackScope } from "./scope";

/** When someone in shop's org asked for ML, and when Reliant granted it: before the first model version (model/ml.ts, 12 days ago). */
export const ML_REQUESTED_AGO = 16 * DAY;
export const ML_GRANTED_AGO = 14 * DAY;

export function organizationScenarioHandlers(now: () => number = demoNow, scope: PackScope = DEMO_SCOPE) {
  const handlers = mockService(OrganizationService, {
    // An admin, as the identity status says (callerCanManage): the verbs show, disabled by the demo's read-only layer.
    listMyOrganizations: () => create(ListMyOrganizationsResponseSchema, {
      organizations: [{ id: DEMO_ORG, name: "Acme", slug: "acme", role: "admin", isOwner: false, isDefault: true }],
      defaultOrgId: DEMO_ORG,
    }),
    getMyOrgEntitlements: () => create(GetMyOrgEntitlementsResponseSchema, {
      features: [{ feature: "ml", enabled: true, requestedAt: timestampFromMs(now() - ML_REQUESTED_AGO), grantedAt: timestampFromMs(now() - ML_GRANTED_AGO) }],
    }),
  });
  return completed(OrganizationService, withMessageRequests(scopedToDemo(handlers, [OrganizationService], scope), [OrganizationService]));
}
