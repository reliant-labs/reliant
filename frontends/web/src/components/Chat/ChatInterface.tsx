/**
 * ChatInterface - Simplified without tabs
 *
 * Just shows the active chat from the navigation queue.
 * No tab management, no create/close logic.
 */

import { useDeferredValue, useMemo } from "react";
import { ChatContainer } from "./ChatContainer";
import { NewChatView } from "./NewChatView";
import { useActiveChatId, useActiveChat, useChat } from "../../store/chatStoreHooks";
import { logger } from "../../lib/logger";

interface ChatInterfaceProps {
  onNavigateToWorktrees?: () => void;
  paneId?: string;
  worktreeId?: string;
  tabId?: string;  // Legacy - ignored in new implementation
  isFocused?: boolean;
}

export function ChatInterface({
  onNavigateToWorktrees,
  isFocused = true,
}: ChatInterfaceProps = {}) {
  // Get active chat from navigation queue
  const activeChatId = useActiveChatId();
  // Use chats Map lookup (via useActiveChat) - populated by initChatState
  // which runs immediately during branchChat
  const activeChat = useActiveChat();

  // The chat on screen trails the active chat by one background render.
  //
  // A switch mounts a whole new ChatContainer (the transcript's visible rows,
  // their markdown, the composer, the panels): 75-250ms of work for a cached
  // transcript of a few hundred messages. activeChatId is store state, and a
  // store change always renders synchronously, in a microtask at the end of
  // the click. Mounted there, that work was one long task, and every request
  // of the open waited behind it: the chat stream's fetch is a few promise
  // hops past chatStore.selectChat (auth, the interceptor chain), so it left
  // the browser only after the new chat had rendered.
  //
  // Deferred, the click's own render is cheap (the element below is reused
  // as is, so React skips the old chat's subtree), the stream request goes
  // out within the click's task, and the new chat renders in a transition:
  // in slices that yield to the browser, with the old chat still on screen
  // until it commits. The open's span (lib/chatOpenInteraction) is keyed by
  // chat id and ends from the NEW chat's presenter, so it still runs from the
  // click to the new chat's usable timeline.
  const shownChatId = useDeferredValue(activeChatId);
  const shownChat = useChat(shownChatId ?? undefined);
  const shownView = useMemo(
    () => shownChatId ? chatView(shownChatId, isFocused) : null,
    [shownChatId, isFocused],
  );

  // If no active chat, show new chat view
  if (!activeChatId || !activeChat) {
    logger.info("[ChatInterface] Showing NewChatView", {
      activeChatId,
      activeChatExists: !!activeChat,
    });
    return (
      <NewChatView
        tabId="single-chat"  // Dummy ID for compatibility
        onNavigateToWorktrees={onNavigateToWorktrees}
        isFocused={isFocused}
        showProjectChip
      />
    );
  }

  // The deferred chat, while it is still a chat to show; otherwise (coming
  // from the new-chat view, or the previous chat is gone) the active chat,
  // rendered at once as before.
  if (shownView && shownChat) return shownView;
  return chatView(activeChatId, isFocused);
}

// IMPORTANT: key={chatId} forces React to create a new ChatContainer instance
// when switching chats. Without this, hooks inside ChatContainer (like useWorkflowExecutions)
// would maintain stale state briefly, causing thread headers from other chats to flash.
// NOTE: min-h-0 is critical for flex scrolling - without it, the flex child won't shrink
// below its content height, breaking overflow-y-auto in ChatMessagesContainer.
// `data-context="chat-view"`: a conversation is on screen, which is what
// makes Cmd+F search it (lib/keyboard/contexts). The new-chat view above
// has nothing to search, so there Cmd+F stays the browser's.
function chatView(chatId: string, isFocused: boolean) {
  return (
    <div className="h-full w-full min-h-0" data-context="chat-view">
      <ChatContainer
        key={chatId}
        tabId={chatId}  // Use chatId as tabId for compatibility
        isFocused={isFocused}
      />
    </div>
  );
}
