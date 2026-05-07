import { useEffect, useState } from "react";
import { ArrowUp, Paperclip, MessageCircle } from "lucide-react";
import { ChatButton } from "./ChatButton";

interface UseChatButtonsProps {
  // Send/Stop
  onSend: () => void;
  onStop?: () => void;
  canSend: boolean;
  isStreaming: boolean;
  disabled: boolean;

  // File actions
  onAttach: () => void;
  uploading: boolean;

  // Discuss
  isDiscussMode?: boolean;
  onToggleDiscuss?: () => void;
  isPaused?: boolean;

  // Responsiveness
  compact?: boolean;
}

export function useChatButtons({
  onSend,
  onStop,
  canSend,
  isStreaming,
  disabled,
  onAttach,
  uploading,
  isDiscussMode = false,
  onToggleDiscuss,
  isPaused = false,
  compact = false,
}: UseChatButtonsProps) {
  const effectiveStreaming = isStreaming;
  const safeOnStop = onStop ?? (() => {});



  // Stable streaming state that doesn't flicker during transitions
  const [stableStreaming, setStableStreaming] = useState(effectiveStreaming);

  useEffect(() => {
    if (effectiveStreaming) {
      // Immediately show streaming state
      setStableStreaming(true);
    } else {
      // Debounce the transition back to non-streaming to prevent flashing
      const timeout = setTimeout(() => setStableStreaming(false), 100);
      return () => clearTimeout(timeout);
    }
  }, [effectiveStreaming]);

  // Individual button definitions
  const buttons = {
    attach: (
      <ChatButton
        key="attach"
        onClick={onAttach}
        disabled={disabled || uploading}
        tooltip="Attach files or images"
        compact={compact}
        className={
          stableStreaming
            ? "bg-[var(--chat-button-bg-streaming)] text-[var(--chat-button-text-streaming)] border-[var(--chat-border-streaming)]"
            : "bg-[var(--chat-button-bg)] text-[var(--chat-button-text)] hover:bg-[var(--chat-button-hover)] border-[var(--chat-border)]"
        }
      >
        <Paperclip className={compact ? "w-2.5 h-2.5" : "w-3 h-3"} />
      </ChatButton>
    ),

    // Discuss button - shown when workflow is paused
    discuss: isPaused && onToggleDiscuss ? (
      <ChatButton
        key="discuss"
        onClick={onToggleDiscuss}
        tooltip={isDiscussMode ? "Exit discussion mode" : "Discuss without resuming"}
        compact={compact}
        className={
          isDiscussMode
            ? "bg-blue-500/20 text-blue-600 dark:text-blue-400 border-blue-500/30 hover:bg-blue-500/30"
            : "bg-blue-500/10 text-blue-600 dark:text-blue-400 border-blue-500/20 hover:bg-blue-500/20"
        }
      >
        <span className={compact ? "" : "inline-flex items-center gap-1"}>
          {!compact && <span className="text-[10px]">{isDiscussMode ? "End Discuss" : "Discuss"}</span>}
          <MessageCircle className={compact ? "w-3 h-3" : "w-3 h-3"} />
        </span>
      </ChatButton>
    ) : null,



    // Combined send/stop button that transitions smoothly
    sendStop: (() => {
      return (
        <ChatButton
          key="sendStop"
          onClick={effectiveStreaming ? safeOnStop : onSend}
          disabled={effectiveStreaming ? false : !canSend || disabled}
          tooltip={effectiveStreaming ? "Stop generation (Esc)" : "Send message (Enter)"}
          testId={effectiveStreaming ? undefined : "send-button"}
          compact={compact}
          className={`h-7 w-7 p-0 rounded-full border transition-colors duration-150 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background ${
            stableStreaming
              ? "bg-[var(--chat-button-bg)] text-destructive border-[var(--chat-border)] hover:bg-[var(--chat-button-hover)] hover:border-destructive/30"
              : canSend && !disabled
              ? "bg-[var(--chat-button-bg)] text-primary border-[var(--chat-border)] hover:bg-[var(--chat-button-hover)] hover:border-primary/30"
              : "bg-[var(--chat-button-bg)] text-[var(--chat-button-text)] border-[var(--chat-border)] opacity-60"
          }`}
        >
          <span className="inline-flex items-center justify-center">
            {effectiveStreaming ? (
              <span className="h-2.5 w-2.5 rounded-[2px] bg-current" />
            ) : (
              <ArrowUp className="h-4 w-4" />
            )}
          </span>
        </ChatButton>
      );
    })(),

    divider: (
      <div
        key="divider"
        className={`w-px h-6 mx-1 ${
          stableStreaming
            ? "bg-[var(--chat-border-streaming)]"
            : "bg-[var(--chat-border)]"
        }`}
        role="separator"
        aria-orientation="vertical"
        aria-hidden="true"
      />
    ),
  };

  // Filter out null buttons
  const availableButtons = Object.fromEntries(
    Object.entries(buttons).filter(([, button]) => button !== null)
  );

  return { buttons: availableButtons };
}