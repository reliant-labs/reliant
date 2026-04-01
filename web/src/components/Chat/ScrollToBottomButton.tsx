import { useScrollContext } from "./ChatMessagesContainer";

interface ScrollToBottomButtonProps {
  className?: string;
}

/**
 * Scroll-to-bottom button that uses scroll context.
 * Shows when user has scrolled up from bottom, hides when at bottom.
 */
export function ScrollToBottomButton({ className }: ScrollToBottomButtonProps) {
  const { isAtBottom, scrollToBottom } = useScrollContext();

  if (isAtBottom) {
    return null;
  }

  return (
    <button
      onClick={() => scrollToBottom()}
      className={className}
      aria-label="Scroll to bottom"
    >
      ↓ Scroll to Bottom
    </button>
  );
}
