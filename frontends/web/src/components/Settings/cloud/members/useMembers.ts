// Copyright (c) 2025 Reliant Labs

import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  listInvitations,
  listMembers,
  listMembershipActivity,
  memberKeys,
} from "@/services/controlPlane/members";

/** The roster, a page at a time (oldest member first); page one carries the seat count. */
export function useMembers(orgId: string) {
  return useInfiniteQuery({
    queryKey: memberKeys.members(orgId),
    queryFn: ({ pageParam }) => listMembers(orgId, pageParam),
    initialPageParam: "",
    getNextPageParam: (last) => last.nextPageToken || undefined,
    enabled: !!orgId,
  });
}

export type MembersQuery = ReturnType<typeof useMembers>;

/** Pending invitations, newest first. Admin/owner only: pass enabled=false otherwise. */
export function useInvitations(orgId: string, enabled = true) {
  return useQuery({
    queryKey: memberKeys.invitations(orgId),
    queryFn: () => listInvitations(orgId),
    enabled: !!orgId && enabled,
  });
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
  return () => qc.invalidateQueries({ queryKey: [...memberKeys.all, orgId] });
}
