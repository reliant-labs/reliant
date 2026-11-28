// Copyright (c) 2025 Reliant Labs

/**
 * components/Settings/cloud/members/useMembers.ts in the forge demo's bundle (a
 * demo twin, vite-plugin-demo-bundle.ts): nobody is signed in to the demo, so
 * the roster is never read and the sharing views name people by what the share
 * itself says. The real module belongs to Settings → Members, which the demo
 * bundle must not contain.
 */

import type { InfiniteData } from "@tanstack/react-query";

import type { ListMembersResponse } from "@/gen/controlplane/services/organization/v1/organization_pb";
import { useListMembersInfinite } from "@/hooks/controlplane/organization-service-hooks_gen";
import { toMemberPage } from "@/services/controlPlane/members";

import type * as Real from "./useMembers";

const memberPages = (data: InfiniteData<ListMembersResponse, string>) => ({
  ...data,
  pages: data.pages.map(toMemberPage),
});

export const useMembers: typeof Real.useMembers = (orgId) =>
  useListMembersInfinite({ orgId, pageSize: 200 }, { enabled: false, select: memberPages });
