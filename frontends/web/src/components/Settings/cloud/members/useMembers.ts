// Copyright (c) 2025 Reliant Labs

import { useQueryClient, type InfiniteData } from "@tanstack/react-query";

import type { ListActivityResponse } from "@/gen/controlplane/services/activity/v1/activity_pb";
import type { ListMembersResponse } from "@/gen/controlplane/services/organization/v1/organization_pb";
import { useListMembershipActivityInfinite } from "@/hooks/controlplane/activity-service-hooks_gen";
import {
  useListInvitations,
  useListMembersInfinite,
} from "@/hooks/controlplane/organization-service-hooks_gen";
import { keyNamesOrg } from "@/lib/orgScope";
import {
  ORG_MEMBER_READS,
  toInvitations,
  toMemberPage,
} from "@/services/controlPlane/members";
import { toActivityList } from "@/services/controlPlane/observability/activityApi";

const memberPages = (data: InfiniteData<ListMembersResponse, string>) => ({
  ...data,
  pages: data.pages.map(toMemberPage),
});

/** The roster, a page at a time (oldest member first); page one carries the seat count. */
export function useMembers(orgId: string) {
  return useListMembersInfinite({ orgId, pageSize: 200 }, { enabled: !!orgId, select: memberPages });
}

export type MembersQuery = ReturnType<typeof useMembers>;

/** Pending invitations, newest first. Admin/owner only: pass enabled=false otherwise. */
export function useInvitations(orgId: string, enabled = true) {
  return useListInvitations({ orgId, pageSize: 200 }, { enabled: !!orgId && enabled, select: toInvitations });
}

const activityPages = (data: InfiniteData<ListActivityResponse, string>) => ({
  ...data,
  pages: data.pages.map(toActivityList),
});

/** The membership audit trail, newest first. Admin/owner. */
export function useMembershipActivity(orgId: string) {
  return useListMembershipActivityInfinite({ orgId, pageSize: 50 }, {
    enabled: !!orgId,
    select: activityPages,
  });
}

/** Re-read everything the members page shows for `orgId` after a change. */
export function useRefreshMembers(orgId: string): () => Promise<void> {
  const qc = useQueryClient();
  return async () => {
    await Promise.all(
      ORG_MEMBER_READS.map((queryKey) =>
        qc.invalidateQueries({ queryKey, predicate: (query) => keyNamesOrg(query.queryKey, orgId) }),
      ),
    );
  };
}
