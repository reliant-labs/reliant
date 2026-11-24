// Copyright (c) 2025 Reliant Labs

/**
 * A chat's members (control-plane docs/design/chat-collaboration.md §3): who
 * holds access, as the caller may see it, and the writes that grant, change
 * and take it away. The org roster names them (ListMembers), keyed by sign-in
 * subject as a chat's user ids are.
 */

import { useEffect, useMemo } from "react";

import { useMembers } from "@/components/Settings/cloud/members/useMembers";
import type { ChatMemberRole } from "@/gen/reliant/v1/sharing_pb";
import { useListChatMembers, useRemoveChatMember, useSetChatMember } from "@/hooks/sharing-service-hooks_gen";
import type { OrgMember } from "@/services/controlPlane/members";
import { hasControlPlane } from "@/services/controlPlane/config";

/**
 * ListChatMembers for `chatId`, read only for a chat of an org (a chat with
 * none has no members) on a deployment with a control plane. Its
 * `grantsEnabled` is the deployment's chat_collaboration.enabled: a server
 * that predates it answers false, and one that predates the rpc fails, and
 * either way the UI offers no grant.
 */
export function useChatMembers(chatId: string | null | undefined, orgId: string | null | undefined) {
  return useListChatMembers(
    { chatId: chatId ?? "" },
    {
      enabled: hasControlPlane && !!chatId && !!orgId,
      staleTime: 30_000,
      // A refusal (an older server, a credential that is not a person's) is
      // an answer, not a blip: retrying it only delays hiding the panel.
      retry: false,
    },
  );
}

/**
 * The live members of `orgId`, every page of the roster: the people a grant
 * may name. The roster lists only current members, so someone who left is
 * never offered. Pages load while `enabled`.
 */
export function useOrgRoster(orgId: string | null | undefined, enabled = true) {
  const query = useMembers(hasControlPlane && enabled && orgId ? orgId : "");
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = query;
  useEffect(() => {
    if (hasNextPage && !isFetchingNextPage) void fetchNextPage();
  }, [hasNextPage, isFetchingNextPage, fetchNextPage]);
  const members = useMemo<OrgMember[]>(() => (query.data?.pages ?? []).flatMap((page) => page.members), [query.data]);
  return { members, isLoading: query.isLoading || !!hasNextPage, error: query.error };
}

/** Grant a person a level on a chat, or change the level they hold. */
export function useGrantChatMember() {
  const mutation = useSetChatMember();
  return {
    ...mutation,
    grant: (chatId: string, userId: string, role: ChatMemberRole) => mutation.mutateAsync({ chatId, userId, role }),
  };
}

/** Take a person's grant on a chat away; a member may take their own. */
export function useRevokeChatMember() {
  const mutation = useRemoveChatMember();
  return {
    ...mutation,
    revoke: (chatId: string, userId: string) => mutation.mutateAsync({ chatId, userId }),
  };
}
