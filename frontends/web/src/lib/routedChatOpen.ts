/**
 * Chat opens the router drives. The mobile chat screen (/m/chats/$chatId)
 * opens its chat from the URL and never calls chatStore.selectChat, so before
 * this nothing started a chat.open for it: not a tap in the mobile list, not a
 * reload, not a notification or a pasted link (only 154 of 582 chat streams in
 * 72h had a chat.open, 2026-11-10). This starts one as each navigation that
 * changes the routed chat begins (lib/chatOpenInteraction routeChatOpen):
 * `mobile_route` within the app, `deep_link` for the page the app loaded on.
 */

import { getMessagesFromCache } from "../hooks/message-queries";
import { routeChatOpen } from "./chatOpenInteraction";

interface NavigationEvent {
  /** None on the app's first navigation: the page it loaded on. */
  fromLocation?: { pathname: string };
  toLocation: { pathname: string };
}

/** The part of the router this needs. */
export interface ChatOpenRouter {
  subscribe(event: "onBeforeNavigate", listener: (event: NavigationEvent) => void): () => void;
}

/**
 * Start the chat.open of every navigation onto a routed chat. `chatIdAt`
 * names the chat a path shows, or none. Returns the unsubscribe.
 */
export function watchRoutedChatOpens(
  router: ChatOpenRouter,
  chatIdAt: (pathname: string) => string | undefined,
): () => void {
  return router.subscribe("onBeforeNavigate", ({ fromLocation, toLocation }) => {
    const toChatId = chatIdAt(toLocation.pathname);
    const cachedMessages = toChatId ? getMessagesFromCache(toChatId).length : 0;
    routeChatOpen(
      fromLocation ? chatIdAt(fromLocation.pathname) : undefined,
      toChatId,
      fromLocation ? "mobile_route" : "deep_link",
      { "chat.cached": cachedMessages > 0, "chat.cached_messages": cachedMessages },
    );
  });
}
