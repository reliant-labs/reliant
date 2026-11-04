/**
 * The expanded width class on `/m/*` (mobile-chat-redesign §4.1): at 840px
 * and up the chats routes render list–detail (`MobileSplitLayout`).
 *
 * Width, never pointer type: a touch iPad in landscape and a desktop window
 * opened on `/m/chats` both have room for two panes. This is a LAYOUT
 * breakpoint only; which surface a device is redirected to is
 * `lib/mobileRedirect.ts`'s question and does not change here.
 *
 * A media query rather than a ResizeObserver on the shell: the shell spans the
 * viewport (less the side safe areas, a few px in landscape), and the query
 * answers synchronously on the first render, so a tablet never paints the
 * single-pane layout for a frame before the split appears.
 */

import { createContext, useContext, useSyncExternalStore } from "react";

export const EXPANDED_MIN_WIDTH = 840;
export const EXPANDED_LAYOUT_QUERY = `(min-width: ${EXPANDED_MIN_WIDTH}px)`;

function subscribe(onChange: () => void): () => void {
  const query = window.matchMedia?.(EXPANDED_LAYOUT_QUERY);
  if (!query) return () => {};
  query.addEventListener("change", onChange);
  return () => query.removeEventListener("change", onChange);
}

function snapshot(): boolean {
  return typeof window !== "undefined" && !!window.matchMedia?.(EXPANDED_LAYOUT_QUERY).matches;
}

export function useExpandedLayout(): boolean {
  return useSyncExternalStore(subscribe, snapshot, () => false);
}

/**
 * True while the chat list is on screen beside the routed chat, so the chat's
 * top bar can drop its back-to-list control: the list it would go back to is
 * already there. False outside `MobileSplitLayout`.
 */
export const ChatListPaneContext = createContext(false);

export function useChatListPaneVisible(): boolean {
  return useContext(ChatListPaneContext);
}
