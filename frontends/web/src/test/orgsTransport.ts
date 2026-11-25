/**
 * OrganizationService's membership list at the transport, for tests of code
 * that reads it through the generated hooks (useMyOrgs, currentOrgId, the org
 * routes: services/controlPlane/orgs.ts).
 *
 *   const listMyOrganizations = vi.fn();
 *   beforeEach(() => setTransport(mockTransport(myOrgsHandlers({ listMyOrganizations }))));
 *   afterEach(() => setTransport(null));
 *
 * Each fake speaks the web's model (`MyOrgs`, an org id), so a test keeps
 * stating its fixture as the page sees it; a fake that rejects answers with
 * that error.
 */

import type { MockHandlers } from "@/lib/test-utils";
import type { MyOrgs } from "@/services/controlPlane/orgs";
import { serviceHandlers } from "./serviceTransport";

export interface MyOrgsFakes {
  listMyOrganizations?: () => MyOrgs | Promise<MyOrgs>;
  setDefaultOrganization?: (orgId: string) => unknown;
}

/** ListMyOrganizations (and SetDefaultOrganization) answered by fakes in the web's terms. */
export function myOrgsHandlers({ listMyOrganizations, setDefaultOrganization }: MyOrgsFakes): MockHandlers {
  const fakes: Record<string, (request: never) => unknown> = {};
  if (listMyOrganizations) {
    fakes.listMyOrganizations = async () => {
      const mine = await listMyOrganizations();
      return { organizations: mine.orgs, defaultOrgId: mine.defaultOrgId };
    };
  }
  if (setDefaultOrganization) {
    fakes.setDefaultOrganization = async (request: { orgId: string }) => {
      await setDefaultOrganization(request.orgId);
      return {};
    };
  }
  return serviceHandlers("OrganizationService", fakes);
}
