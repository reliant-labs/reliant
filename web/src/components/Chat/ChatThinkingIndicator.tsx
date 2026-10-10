import { useState, useEffect } from "react";
import { thinkingMessages } from "../../lib/thinking-messages";
import {
  QUEUED_FOR_MACHINE,
  STILL_WORKING,
  STILL_WORKING_AFTER_MS,
  WAITING_FOR_MACHINE,
} from "../../lib/daemon-wait";
import { formatWaitElapsed } from "../../lib/chatMachineNotice";
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
  /**
   * The run is held until its machine comes up (ChatActivity.WAITING_FOR_DAEMON):
   * a run that needs a machine waits for one that is waking or starting rather
   * than failing. Say so instead of cycling "Thinking…" over a run that is not
   * thinking yet.
   */
  waitingOnMachine?: boolean;
  /**
   * While held for the machine, the run has not read the user's latest
   * message yet: that message is queued, and says so.
   */
  messageQueuedForMachine?: boolean;
  /**
   * Changes whenever the run shows the user something: streamed output, a new
   * message, run output. The indicator times "no response yet" from the last
   * change (and from the last change of the thread's activity).
   */
  progressKey?: string;
  /** "Run status": show what the run is doing. Omitted where there is no such view. */
  onShowRunStatus?: () => void;
  /** "Stop": stop the run. The user's next message starts it again. */
  onStop?: () => void;
}

/** Activities a run can legitimately sit in for minutes; they narrate themselves. */
function isGenericThinking(activityText: string | null): boolean {
  return activityText === null || activityText === "Thinking";
}

export function ChatThinkingIndicator({ 
  chatId, 
  filterThreadId = null,
  waitingOnMachine = false,
  messageQueuedForMachine = false,
  progressKey,
  onShowRunStatus,
  onStop,
}: ChatThinkingIndicatorProps) {
  const [thinkingMessage, setThinkingMessage] = useState("Thinking");

  // Use thread activity store for per-thread active checks. A run held for
  // its machine is not RUNNING, so the thread store reports nothing active —
  // yet the run is live and the user is owed a line under their message.
  const isThreadActive = useIsThreadActive(chatId || "", filterThreadId ?? null);
  const isActive = isThreadActive || waitingOnMachine;

  // Thread-level activity detail from threadActivityStore
  const currentActivity = useChatCurrentActivity(chatId || "");
  
  // Get user-friendly activity text
  const activityText = waitingOnMachine
    ? messageQueuedForMachine
      ? QUEUED_FOR_MACHINE
      : WAITING_FOR_MACHINE
    : getActivityDisplayText(currentActivity);

  // "No response yet": how long since the run last showed anything. Only for
  // the generic thinking line — a tool or an approval says what it is doing,
  // and a long build is not a stall.
  const timesSilence = isActive && !waitingOnMachine && isGenericThinking(activityText);
  const [lastProgressAt, setLastProgressAt] = useState(() => Date.now());
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const at = Date.now();
    setLastProgressAt(at);
    setNow(at);
  }, [progressKey, currentActivity]);
  useEffect(() => {
    if (!timesSilence) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [timesSilence]);
  const silentFor = now - lastProgressAt;
  const stillWorking = timesSilence && silentFor >= STILL_WORKING_AFTER_MS;

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
    <div data-testid="thinking-indicator" data-active="true" data-still-working={stillWorking || undefined}>
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
      <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
        <span className="text-sm text-muted-foreground">{stillWorking ? STILL_WORKING : thinkingMessage}</span>
        <div className="flex items-center gap-1.5">
          <div className="w-1.5 h-1.5 rounded-full thinking-dot-1" style={{ backgroundColor: 'hsl(var(--primary))' }} />
          <div className="w-1.5 h-1.5 rounded-full thinking-dot-2" style={{ backgroundColor: 'hsl(var(--primary))' }} />
          <div className="w-1.5 h-1.5 rounded-full thinking-dot-3" style={{ backgroundColor: 'hsl(var(--primary))' }} />
        </div>
        {stillWorking && (
          <>
            <span className="text-xs tabular-nums text-muted-foreground">· {formatWaitElapsed(silentFor)}</span>
            {onShowRunStatus && (
              <button
                type="button"
                onClick={onShowRunStatus}
                className="text-xs font-medium text-primary hover:underline"
              >
                Run status
              </button>
            )}
            {onStop && (
              <button
                type="button"
                onClick={onStop}
                title="Stop this run. Sending a message starts it again."
                className="text-xs font-medium text-primary hover:underline"
              >
                Stop
              </button>
            )}
          </>
        )}
      </div>
    </div>
  );
}
