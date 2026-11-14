// Copyright (c) 2026 Reliant Labs

import { useEffect, useMemo } from "react";

import { memberNamesBySubject } from "@/services/controlPlane/members";

import { useMembers } from "../members/useMembers";
import type { MemberNames } from "./orgUsageModel";

/**
 * Every member's name in the org, by user subject: reads the roster to its
 * last page (a usage view names members of any page, not just the first), and
 * says how far it got, so a name not yet read is "unresolved", not "former".
 */
export function useMemberNames(orgId: string): MemberNames {
  const roster = useMembers(orgId);
  const { hasNextPage, isFetchingNextPage, isError, fetchNextPage } = roster;
  useEffect(() => {
    if (hasNextPage && !isFetchingNextPage && !isError) void fetchNextPage();
  }, [hasNextPage, isFetchingNextPage, isError, fetchNextPage]);

  const pages = roster.data?.pages;
  const names = useMemo(() => memberNamesBySubject(pages?.flatMap((p) => p.members) ?? []), [pages]);
  const state = isError ? "failed" : roster.isPending || hasNextPage || isFetchingNextPage ? "loading" : "complete";
  return { names, state };
}
