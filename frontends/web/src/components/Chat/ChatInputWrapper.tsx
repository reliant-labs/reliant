import { useCallback, forwardRef } from "react";
import { ChatInput } from "./ChatInput";
import type { ComponentProps } from "react";

interface ChatInputWrapperProps
  extends Omit<
    ComponentProps<typeof ChatInput>,
    never
  > {
  onScrollToBottom?: () => void;
}

/**
 * Wrapper for ChatInput that scrolls the timeline to the bottom once the
 * user's message has been sent, so the reply lands in view.
 */
export const ChatInputWrapper = forwardRef<
  HTMLTextAreaElement,
  ChatInputWrapperProps
>(function ChatInputWrapper({ onScrollToBottom, onSend, ...props }, ref) {
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

      onScrollToBottom?.();
    },
    [onSend, onScrollToBottom]
  );

  return (
    <ChatInput
      ref={ref}
      {...props}
      onSend={handleSend}
    />
  );
});
