import { type ReactNode } from "react";
import { cn } from "../../lib/utils";

interface ChatMessagesContainerProps {
  children: ReactNode;
  className?: string;
  /**
   * Vestigial: this frame renders identically for every chat. Still declared
   * because a compact host passes it.
   */
  chatId?: string;
  /** The timeline is being replaced (catch-up overlay up). */
  busy?: boolean;
}

/**
 * Clipping frame for the chat timeline. The timeline owns scrolling entirely, so
 * this contributes the background, the overflow boundary, and the flex sizing
 * the timeline is measured against — nothing more.
 */
export const ChatMessagesContainer = ({
  children,
  className,
  busy = false,
}: ChatMessagesContainerProps) => (
  <div
    className={cn(
      "flex-1 overflow-hidden dense-ui chat-background relative",
      className
    )}
    style={{
      zIndex: 0,
    }}
    data-testid="chat-messages"
    aria-busy={busy || undefined}
  >
    {children}
  </div>
);

ChatMessagesContainer.displayName = "ChatMessagesContainer";
