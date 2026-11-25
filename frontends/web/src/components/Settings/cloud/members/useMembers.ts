// Copyright (c) 2025 Reliant Labs

import { useInfiniteQuery, useQueryClient, type InfiniteData } from "@tanstack/react-query";

import type { ListMembersResponse } from "@/gen/controlplane/services/organization/v1/organization_pb";
import {
  useListInvitations,
  useListMembersInfinite,
} from "@/hooks/controlplane/organization-service-hooks_gen";
import { keyNamesOrg } from "@/lib/orgScope";
import {
  listMembershipActivity,
  memberKeys,
  ORG_MEMBER_READS,
  toInvitations,
  toMemberPage,
} from "@/services/controlPlane/members";

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

/** The membership audit trail, newest first. */
export function useMembershipActivity(orgId: string) {
  return useInfiniteQuery({
    queryKey: memberKeys.activity(orgId),
    queryFn: ({ pageParam }) => listMembershipActivity(orgId, pageParam),
    initialPageParam: "",
    getNextPageParam: (last) => last.nextPageToken || undefined,
    enabled: !!orgId,
  });
}

/** Re-read everything the members page shows for `orgId` after a change. */
export function useRefreshMembers(orgId: string): () => Promise<void> {
  const qc = useQueryClient();
  return async () => {
    await Promise.all([
      qc.invalidateQueries({ queryKey: [...memberKeys.all, orgId] }),
      ...ORG_MEMBER_READS.map((queryKey) =>
        qc.invalidateQueries({ queryKey, predicate: (query) => keyNamesOrg(query.queryKey, orgId) }),
      ),
    ]);
  };
}
