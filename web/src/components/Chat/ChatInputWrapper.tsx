import { useEffect, useState, useCallback, forwardRef } from "react";
import { ChatInput } from "./ChatInput";
import type { ComponentProps } from "react";

interface ChatInputWrapperProps
  extends Omit<
    ComponentProps<typeof ChatInput>,
    "showScrollButton" | "onScrollToBottom" | "unreadMessageCount"
  > {
  scrollState?: {
    isAtBottom: boolean;
    hasScrolledUp: boolean;
    scrollToBottom: () => Promise<void>;
    resumeAutoScroll: () => void;
  } | null;
}

/**
 * Wrapper for ChatInput that integrates with scroll state.
 * Provides scroll state and controls to ChatInput via props.
 */
export const ChatInputWrapper = forwardRef<
  HTMLDivElement,
  ChatInputWrapperProps
>(function ChatInputWrapper({ scrollState, onSend, ...props }, ref) {
  const isAtBottom = scrollState?.isAtBottom ?? true;
  const hasScrolledUp = scrollState?.hasScrolledUp ?? false;
  const scrollToBottom = scrollState?.scrollToBottom;
  // resumeAutoScroll available via scrollState if needed in future
  const [unreadCount, setUnreadCount] = useState(0);

  // Reset unread count when returning to bottom or when stuck-to-bottom is active
  useEffect(() => {
    if (isAtBottom || !hasScrolledUp) {
      setUnreadCount(0);
    }
  }, [isAtBottom, hasScrolledUp]);

  // TODO: Increment unread count when new messages arrive while scrolled up
  // This would require tracking message count from props or context
  // For now, we just show the button without a count

  const handleScrollToBottom = useCallback(async () => {
    if (scrollToBottom) {
      await scrollToBottom();
      setUnreadCount(0);
    }
  }, [scrollToBottom]);

  // When user sends a message, scroll to bottom so they can see the response
  const handleSend = useCallback(
    async (
      message: string,
      attachmentIds?: string[],
      workflow?: string | null,
      workflowParams?: Record<string, unknown>,
      targetThread?: string | null,
      selectedPresets?: Record<string, string | null>
    ) => {
      // Reset unread count immediately
      setUnreadCount(0);

      // Send the message first (this adds the optimistic message to DOM)
      await onSend(message, attachmentIds, workflow, workflowParams, targetThread, selectedPresets);

      // THEN scroll to bottom to include the new message
      // Use scrollToBottom directly since we want to scroll AFTER content is added
      if (scrollToBottom) {
        await scrollToBottom();
      }
    },
    [onSend, scrollToBottom]
  );

  // Show scroll button only when user has manually scrolled up
  // Hide it when we're "stuck to bottom" (auto-scrolling with new content)
  const showScrollButton = hasScrolledUp;

  return (
    <ChatInput
      ref={ref}
      {...props}
      onSend={handleSend}
      showScrollButton={showScrollButton}
      onScrollToBottom={handleScrollToBottom}
      unreadMessageCount={unreadCount}
    />
  );
});
