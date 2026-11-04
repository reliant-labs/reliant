/**
 * Layout for the chats routes (`/m/chats`, `/m/chats/$chatId`): single pane
 * below 840px, list–detail at and above it (mobile-chat-redesign §4.1, §9).
 *
 * Expanded, the chat list is a persistent 320px pane and the routed screen
 * fills the rest: opening a chat replaces the right pane, not the whole
 * screen, and `/m/chats` alone shows a calm empty detail. Compact, it is the
 * phone layout it always was: the list at `/m/chats`, the chat at
 * `/m/chats/$chatId`.
 *
 * Only these routes split. Settings, Inbox, Machines and the rest are
 * full-screen destinations of the drawer with nothing to list beside them,
 * and the new-chat screen and a chat's workflow view are full-screen tasks.
 *
 * ## Crossing 840 does not remount the chat
 *
 * Rotating an iPad, or resizing a split-view window, crosses the breakpoint.
 * The detail slot is always the second child of the same row, and the list
 * pane is a conditional in the first slot, so React keeps the detail subtree
 * (`ChatContainer`, its scroll position, an open sheet, the composer's
 * uploads) when the list appears or disappears. Below 840 with no chat routed
 * the detail is hidden rather than unmounted, for the same reason.
 */

import { Link, Outlet, useParams } from "@tanstack/react-router";
import { MessageSquarePlus, MessagesSquare } from "lucide-react";
import { cn } from "../../lib/utils";
import { MobileChatList } from "./MobileChatList";
import { MOBILE_PRIMARY_ACTION, MobileEmptyState } from "./MobileChrome";
import { ChatListPaneContext, useExpandedLayout } from "./mobileSplitPane";

export function MobileSplitLayout() {
  const expanded = useExpandedLayout();
  const { chatId } = useParams({ strict: false });
  const showList = expanded || !chatId;

  return (
    <ChatListPaneContext.Provider value={expanded}>
      <div className="flex h-full min-h-0" data-testid="mobile-chats-layout" data-layout={expanded ? "split" : "single"}>
        {showList && (
          <aside
            aria-label="Chats"
            className={cn(
              "flex h-full min-h-0 flex-col",
              expanded ? "w-80 shrink-0 border-r border-border" : "min-w-0 flex-1",
            )}
          >
            <MobileChatList />
          </aside>
        )}
        <div
          className={cn("flex h-full min-h-0 min-w-0 flex-1 flex-col", !expanded && !chatId && "hidden")}
          data-testid="mobile-chat-detail"
        >
          <Outlet />
        </div>
      </div>
    </ChatListPaneContext.Provider>
  );
}

/**
 * `/m/chats` with no chat selected. Only ever visible beside the list, so it
 * stays quiet: one line saying what the pane is for, and the one action that
 * fills it without a selection.
 */
export function MobileChatsEmptyDetail() {
  return (
    <MobileEmptyState
      icon={MessagesSquare}
      title="Select a chat"
      description="Pick a chat from the list, or start a new one."
      action={
        <Link to="/m/new" className={MOBILE_PRIMARY_ACTION}>
          <MessageSquarePlus className="h-4 w-4" />
          New chat
        </Link>
      }
    />
  );
}
