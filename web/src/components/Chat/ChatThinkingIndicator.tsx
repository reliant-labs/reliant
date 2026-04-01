import { useState, useEffect } from "react";
import { thinkingMessages } from "../../lib/thinking-messages";
import { 
  useIsThreadActive,
  useChatCurrentActivity,
  getActivityDisplayText 
} from "../../store/threadActivityStore";

interface ChatThinkingIndicatorProps {
  chatId?: string;
  /** 
   * Thread ID to filter activity for. 
   * - null: Show activity for any thread (All view)
   * - chatId: Main thread - shows if any child is active
   * - specific ID: Only show if that thread is active
   */
  filterThreadId?: string | null;
}

export function ChatThinkingIndicator({ 
  chatId, 
  filterThreadId = null,
}: ChatThinkingIndicatorProps) {
  const [thinkingMessage, setThinkingMessage] = useState("Thinking");

  // Use thread activity store for per-thread active checks
  const isActive = useIsThreadActive(chatId || "", filterThreadId ?? null);

  // Thread-level activity detail from threadActivityStore
  const currentActivity = useChatCurrentActivity(chatId || "");
  
  // Get user-friendly activity text
  const activityText = getActivityDisplayText(currentActivity);

  useEffect(() => {
    // If there's a specific activity, don't cycle through messages
    if (activityText) {
      setThinkingMessage(activityText);
      return;
    }

    // Otherwise, immediately reset to default and cycle through thinking messages
    setThinkingMessage(thinkingMessages[0]);
    let messageIndex = 0;
    const interval = setInterval(() => {
      messageIndex = (messageIndex + 1) % thinkingMessages.length;
      setThinkingMessage(thinkingMessages[messageIndex]);
    }, 6000);

    return () => clearInterval(interval);
  }, [activityText]);

  // Don't render if not active
  // Exception: if no chatId, show indicator (loading state)
  if (chatId && !isActive) {
    return null;
  }

  return (
    <div data-testid="thinking-indicator" data-active="true">
      <style>{`
        @keyframes bounce-wave {
          0%, 60%, 100% {
            transform: translateY(0);
          }
          30% {
            transform: translateY(-8px);
          }
        }
        .thinking-dot-1 {
          animation: bounce-wave 1.4s ease-in-out infinite;
          animation-delay: 0s;
        }
        .thinking-dot-2 {
          animation: bounce-wave 1.4s ease-in-out infinite;
          animation-delay: 0.15s;
        }
        .thinking-dot-3 {
          animation: bounce-wave 1.4s ease-in-out infinite;
          animation-delay: 0.3s;
        }
      `}</style>
      <div className="flex items-center gap-2">
        <span className="text-sm text-muted-foreground">{thinkingMessage}</span>
        <div className="flex items-center gap-1.5">
          <div className="w-1.5 h-1.5 rounded-full thinking-dot-1" style={{ backgroundColor: 'hsl(var(--primary))' }} />
          <div className="w-1.5 h-1.5 rounded-full thinking-dot-2" style={{ backgroundColor: 'hsl(var(--primary))' }} />
          <div className="w-1.5 h-1.5 rounded-full thinking-dot-3" style={{ backgroundColor: 'hsl(var(--primary))' }} />
        </div>
      </div>
    </div>
  );
}
