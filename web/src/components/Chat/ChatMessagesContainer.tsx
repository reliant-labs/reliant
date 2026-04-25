import { type ReactNode, createContext, useContext, useRef, useCallback, useEffect, useMemo, type RefObject } from "react";
import type { VirtuosoHandle } from "react-virtuoso";
import { useIsChatActive } from "../../store/chatStoreHooks";
import { cn } from "../../lib/utils";

interface ChatMessagesContainerProps {
  children: ReactNode;
  className?: string;
  chatId?: string;
  onScrollStateChange?: (state: ScrollContextValue) => void;
  /** Ref to the Virtuoso handle - provided by parent */
  virtuosoRef?: RefObject<VirtuosoHandle | null>;
  /** Whether Virtuoso reports being at the bottom */
  virtuosoAtBottom?: boolean;
  /** Callback that resets follow mode in InterleavedTimeline and scrolls to bottom */
  resumeFollowRef?: RefObject<(() => void) | null>;
}

interface ScrollContextValue {
  isAtBottom: boolean;
  hasScrolledUp: boolean;
  scrollToBottom: () => Promise<void>;
  stopAutoScroll: () => void;
  resumeAutoScroll: () => void;
  pauseAutoScroll: () => void;
  unpauseAutoScroll: () => void;
  /** Ref to the scroll container element */
  scrollRef: RefObject<HTMLDivElement | null>;
  /** Whether this chat is currently active/visible */
  isActive: boolean;
}

const ScrollContext = createContext<ScrollContextValue | null>(null);

/**
 * Stable context that only exposes scrollToBottom.
 * Consuming this does NOT re-render when scroll position changes,
 * making it safe for memoized components like ChatMessage.
 */
type ScrollActionFn = () => void;
const ScrollActionContext = createContext<ScrollActionFn | null>(null);

/**
 * Hook to access scroll state from within ChatMessagesContainer.
 * Must be used inside ChatMessagesContainer or its children.
 */
export const useScrollContext = () => {
  const context = useContext(ScrollContext);
  if (!context) {
    throw new Error('useScrollContext must be used within ChatMessagesContainer');
  }
  return context;
};

/**
 * Hook that returns only the scrollToBottom action.
 * This context value never changes, so it won't cause re-renders
 * in memoized components. Safe for use in ChatMessage, ErrorMessage, etc.
 */
export const useScrollToBottomAction = (): ScrollActionFn | null => {
  return useContext(ScrollActionContext);
};

/**
 * Container for chat messages with Virtuoso-based auto-scroll behavior.
 * Virtuoso handles its own scrolling; this component bridges scroll state
 * to ScrollContext for components like ScrollToBottomButton.
 */
export const ChatMessagesContainer = ({ children, className, chatId, onScrollStateChange, virtuosoRef, virtuosoAtBottom, resumeFollowRef }: ChatMessagesContainerProps) => {
  const isActiveChat = useIsChatActive(chatId || "");
  const containerRef = useRef<HTMLDivElement>(null);

  // Derive scroll state from Virtuoso's atBottom reporting
  const isAtBottom = virtuosoAtBottom ?? true;
  const hasScrolledUp = !isAtBottom;

  // scrollToBottom uses resumeFollowRef when available — this resets
  // userScrolledUpRef inside InterleavedTimeline so followOutput resumes.
  // Falls back to a direct Virtuoso scrollToIndex if no callback is registered.
  const scrollToBottom = useCallback(async () => {
    if (resumeFollowRef?.current) {
      resumeFollowRef.current();
    } else {
      virtuosoRef?.current?.scrollToIndex({
        index: "LAST",
        align: "end",
        behavior: "auto",
      });
    }
  }, [virtuosoRef, resumeFollowRef]);

  const stopAutoScroll = useCallback(() => {}, []);
  const resumeAutoScroll = useCallback(() => {
    scrollToBottom();
  }, [scrollToBottom]);
  const pauseAutoScroll = useCallback(() => {}, []);
  const unpauseAutoScroll = useCallback(() => {}, []);

  const scrollState: ScrollContextValue = useMemo(() => ({
    isAtBottom,
    hasScrolledUp,
    scrollToBottom,
    stopAutoScroll,
    resumeAutoScroll,
    pauseAutoScroll,
    unpauseAutoScroll,
    scrollRef: containerRef,
    isActive: isActiveChat,
  }), [isAtBottom, hasScrolledUp, scrollToBottom, stopAutoScroll, resumeAutoScroll, pauseAutoScroll, unpauseAutoScroll, isActiveChat]);

  // Notify parent of scroll state changes
  useEffect(() => {
    onScrollStateChange?.(scrollState);
  }, [scrollState, onScrollStateChange]);

  return (
    <ScrollContext.Provider value={scrollState}>
      <ScrollActionContext.Provider value={scrollToBottom}>
        <div
          ref={containerRef}
          className={cn(
            "flex-1 overflow-hidden dense-ui chat-background relative",
            className
          )}
          style={{
            zIndex: 0,
          }}
          data-testid="chat-messages"
        >
          {children}
        </div>
      </ScrollActionContext.Provider>
    </ScrollContext.Provider>
  );
};

ChatMessagesContainer.displayName = "ChatMessagesContainer";