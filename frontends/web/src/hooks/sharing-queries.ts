// Copyright (c) 2025 Reliant Labs

/**
 * Read-only sharing (control-plane docs/design/org-sharing.md): the "Shared
 * with the org" list, the org feed that keeps it current, the owner names it
 * shows, and the share toggle's write.
 *
 * Shared chats never appear in the person's own chat list (ListChats answers
 * only their own); they come from ListSharedChats, for the active org.
 */

import { useEffect, useMemo } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";

import { OrgFeedStream } from "@/api/org-feed-stream";
import { ResourceVisibility } from "@/gen/reliant/v1/common_pb";
import { OrgUpdateType, type OrgUpdate } from "@/gen/reliant/v1/streaming_pb";
import { chatKeys, patchChatCaches } from "@/hooks/chat-queries";
import { leaveSharedChat } from "@/store/openChat";
import {
  sharingServiceKeys,
  useListSharedChats,
  useSetChatVisibility,
} from "@/hooks/sharing-service-hooks_gen";
import { useMembers } from "@/components/Settings/cloud/members/useMembers";
import { hasControlPlane } from "@/services/controlPlane/config";

/** How long a burst of feed rows may wait before the list is re-read. */
export const ORG_FEED_REFRESH_DEBOUNCE_MS = 250;

/** The chats other members shared with `orgId`, most recently active first. */
export function useSharedChats(orgId: string | null) {
  return useListSharedChats(
    { orgId: orgId ?? undefined },
    {
      enabled: hasControlPlane && !!orgId,
      // The org feed keeps it current; a refocus re-reads it in case the
      // feed was down (the server answers a fresh open with no replay).
      refetchOnWindowFocus: true,
      select: (res) => res.chats,
    },
  );
}

/**
 * What one batch of feed rows changes in the cache: the shared lists are
 * re-read (a row is coarse; the list read is one indexed query), and a chat
 * that was made private or deleted is closed if a viewer has it open
 * (leaveSharedChat), else its detail is re-read.
 */
export function applyOrgUpdates(client: QueryClient, updates: readonly OrgUpdate[]): void {
  void client.invalidateQueries({ queryKey: sharingServiceKeys.all });
  for (const u of updates) {
    if (u.resourceKind !== "chat") continue;
    if (u.type === OrgUpdateType.UNSHARED || u.type === OrgUpdateType.DELETED) {
      // Another member's chat that is open here closes now; the chat
      // stream's re-check would close it too, within its interval.
      if (!leaveSharedChat(u.resourceId)) {
        void client.invalidateQueries({ queryKey: chatKeys.detail(u.resourceId), exact: true });
      }
    }
  }
}

/**
 * Follow `orgId`'s feed while mounted: each batch of rows refreshes the shared
 * list (debounced, so a burst of activity is one read). A refusal (the person
 * left the org, or the credential is not a person's) stops the feed; the
 * list's own read reports the same refusal.
 */
export function useOrgFeed(orgId: string | null): void {
  const client = useQueryClient();
  useEffect(() => {
    if (!hasControlPlane || !orgId) return;
    let pending: OrgUpdate[] = [];
    let timer: ReturnType<typeof setTimeout> | null = null;
    const flush = () => {
      timer = null;
      const batch = pending;
      pending = [];
      applyOrgUpdates(client, batch);
    };
    const stream = new OrgFeedStream({
      onUpdates: (updates) => {
        pending.push(...updates);
        timer ??= setTimeout(flush, ORG_FEED_REFRESH_DEBOUNCE_MS);
      },
      onCaughtUp: () => {},
      onRefused: () => {},
    });
    stream.start(orgId);
    return () => {
      stream.stop();
      if (timer) clearTimeout(timer);
    };
  }, [client, orgId]);
}

/**
 * Display names by the id this app keys a person's records by: a chat's
 * `userId` is their sign-in subject, which the roster carries as
 * `externalId` (its `userId` is control-plane's own id, which matches
 * nothing here). The name they set, else their email. A control plane that
 * predates `externalId` names nobody, and the callers say "the person who
 * shared it" instead.
 */
export function memberNamesBySubject(
  members: Iterable<{ externalId: string; name: string; email: string }>,
): Map<string, string> {
  const names = new Map<string, string>();
  for (const m of members) {
    if (m.externalId) names.set(m.externalId, m.name || m.email);
  }
  return names;
}

/**
 * Display names of `orgId`'s members, keyed as a chat's `userId` is
 * (memberNamesBySubject). Any member may read the roster (ListMembers).
 */
export function useOrgMemberNames(orgId: string | null): ReadonlyMap<string, string> {
  const members = useMembers(hasControlPlane && orgId ? orgId : "");
  return useMemo(
    () => memberNamesBySubject((members.data?.pages ?? []).flatMap((page) => page.members)),
    [members.data],
  );
}

/**
 * Share a chat with its org, or make it private: the owner's toggle, and an
 * org admin's "make private". On success the chat's own caches carry the new
 * visibility at once (the header and the sidebar row read them), and every
 * shared list is re-read by the generated hook.
 */
export function useSetChatShared() {
  const mutation = useSetChatVisibility({
    onSuccess: (res, req) => {
      const chat = res.chat;
      if (!chat || !req.chatId) return;
      patchChatCaches(chat.projectId, req.chatId, {
        visibility: chat.visibility,
        sharedAt: chat.sharedAt,
      });
    },
  });
  return {
    ...mutation,
    share: (chatId: string) => mutation.mutateAsync({ chatId, visibility: ResourceVisibility.ORG }),
    unshare: (chatId: string) => mutation.mutateAsync({ chatId, visibility: ResourceVisibility.PRIVATE }),
  };
}
