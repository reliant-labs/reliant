/**
 * ChatInterface - Simplified without tabs
 *
 * Just shows the active chat from the navigation queue.
 * No tab management, no create/close logic.
 */

import { ChatContainer } from "./ChatContainer";
import { NewChatView } from "./NewChatView";
import { useActiveChatId, useActiveChat } from "../../store/chatStoreHooks";
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

  // Render the active chat
  // IMPORTANT: key={activeChatId} forces React to create a new ChatContainer instance
  // when switching chats. Without this, hooks inside ChatContainer (like useWorkflowExecutions)
  // would maintain stale state briefly, causing thread headers from other chats to flash.
  // NOTE: min-h-0 is critical for flex scrolling - without it, the flex child won't shrink
  // below its content height, breaking overflow-y-auto in ChatMessagesContainer.
  // `data-context="chat-view"`: a conversation is on screen, which is what
  // makes Cmd+F search it (lib/keyboard/contexts). The new-chat view above
  // has nothing to search, so there Cmd+F stays the browser's.
  return (
    <div className="h-full w-full min-h-0" data-context="chat-view">
      <ChatContainer
        key={activeChatId}
        tabId={activeChatId}  // Use chatId as tabId for compatibility
        isFocused={isFocused}
      />
    </div>
  );
}
