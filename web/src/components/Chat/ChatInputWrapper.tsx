import { useCallback, forwardRef } from "react";
import { ChatInput } from "./ChatInput";
import type { ComponentProps } from "react";

interface ChatInputWrapperProps
  extends Omit<
    ComponentProps<typeof ChatInput>,
    never
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
 * Scrolls to bottom when the user sends a message.
 */
export const ChatInputWrapper = forwardRef<
  HTMLDivElement,
  ChatInputWrapperProps
>(function ChatInputWrapper({ scrollState, onSend, ...props }, ref) {
  const scrollToBottom = scrollState?.scrollToBottom;

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
      await onSend(message, attachmentIds, workflow, workflowParams, targetThread, selectedPresets);

      if (scrollToBottom) {
        await scrollToBottom();
      }
    },
    [onSend, scrollToBottom]
  );

  return (
    <ChatInput
      ref={ref}
      {...props}
      onSend={handleSend}
    />
  );
});
