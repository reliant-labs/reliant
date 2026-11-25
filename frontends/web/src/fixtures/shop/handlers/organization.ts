// Copyright (c) 2025 Reliant Labs

/**
 * The org the sample visitor acts in: shop's (shopStory.ts DEMO_ORG), the one
 * every scoped read names. ListMyOrganizations is what the console's own
 * `currentOrgId()` (services/controlPlane/orgs.ts) asks outside the org tree,
 * so the Users page and Data's "Open in Users" link find their org through
 * the real code path. Every other OrganizationService call is refused (the
 * transport's read-only guard) or not served (Unimplemented).
 */
import { create } from "@bufbuild/protobuf";
import { mockService } from "@reliantlabs/forge-web-runtime/mock-transport";

import { ListMyOrganizationsResponseSchema, OrganizationService } from "@/gen/controlplane/services/organization/v1/organization_pb";

import { DEMO_ORG } from "../shopStory";
import { completed } from "./defaults";

export function organizationScenarioHandlers() {
  return completed(OrganizationService, mockService(OrganizationService, {
    // An admin, as the identity status says (callerCanManage): the verbs show, disabled by the demo's read-only layer.
    listMyOrganizations: () => create(ListMyOrganizationsResponseSchema, {
      organizations: [{ id: DEMO_ORG, name: "Acme", slug: "acme", role: "admin", isOwner: false, isDefault: true }],
      defaultOrgId: DEMO_ORG,
    }),
  }));
}
