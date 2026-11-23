// Copyright (c) 2025 Reliant Labs

/**
 * THE URL FOLLOWS A CHAT THE APP OPENED OR CLOSED BY ITSELF.
 *
 * The URL is the authority for the open chat (lib/chatRoute.ts): everything a
 * person does to open a chat navigates (store/openChat.ts), and the stores
 * follow the route (hooks/useChatRoute.ts). A few changes come from the app
 * instead, with no navigation of their own: a draft's first message makes
 * it a chat, a branch opens the new chat, the open chat is archived or its
 * access lost, the workspace restore reopens the chat the home page left
 * open. Each moves `chatStore.activeChatId`; this moves the URL after it:
 *
 *   another chat became active, from a chat's URL   → its URL, a push: Back
 *                                                     returns to the chat it
 *                                                     came from (a branch)
 *   a chat became active, from the home or a draft  → its URL, a replace: the
 *                                                     page became that chat
 *   no chat is active, from a chat's URL            → the new-chat view, a
 *                                                     replace: the chat is gone
 *
 * Only on the chat shell's own pages: a run's page, the Inbox or Settings
 * select a chat as background context (RunRouteLoader), and stay put. And
 * never over a navigation still loading: the route it loads decides.
 */

import { getChatFromCache } from "@/hooks/chat-queries";
import { useChatStore } from "@/store/chatStore";

import { chatShellRouteOf, latestPathname, navigateToChatUrl, navigateToNewChatUrl } from "./chatRoute";

interface RouterLike {
  state: { status: string };
}

/** Follow the active chat with the URL of `router`'s tree; returns the unsubscribe. */
export function followActiveChat(router: RouterLike): () => void {
  let previous = useChatStore.getState().activeChatId;
  return useChatStore.subscribe((state) => {
    const next = state.activeChatId;
    if (next === previous) return;
    const closed = previous;
    previous = next;
    const route = chatShellRouteOf(latestPathname());
    if (!route || router.state.status === "pending") return;
    if (next) {
      if (route.kind === "chat" && route.chatId === next) return;
      navigateToChatUrl(next, { orgId: getChatFromCache(next)?.orgId, replace: route.kind !== "chat" });
    } else if (route.kind === "chat" && closed === route.chatId) {
      // Only the URL's own chat closing leaves its page: another one
      // closing before the URL's chat was opened leaves the URL to open it.
      navigateToNewChatUrl({}, { replace: true });
    }
  });
}
