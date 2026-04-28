import { useEffect, useState } from "react";
import { CornerDownLeft, Sparkles, Square, Paperclip, GitBranch, Minimize2, MessageCircle } from "lucide-react";
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

  // Recent changes
  onToggleRecentChanges?: () => void;
  isRecentChangesOpen?: boolean;
  hasWorktree?: boolean;

  // Compact
  onCompact?: () => void;
  isCompacting?: boolean;

  // Dev mode
  isDev?: boolean;
  forceStreaming?: boolean;
  onToggleForceStreaming?: () => void;

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
  onToggleRecentChanges,
  isRecentChangesOpen = false,
  hasWorktree = false,
  onCompact,
  isCompacting = false,
  isDev = false,
  forceStreaming = false,
  onToggleForceStreaming,
  isDiscussMode = false,
  onToggleDiscuss,
  isPaused = false,
  compact = false,
}: UseChatButtonsProps) {
  const effectiveStreaming = isStreaming || forceStreaming;
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
    recentChanges: onToggleRecentChanges && hasWorktree ? (
      <ChatButton
        key="recentChanges"
        onClick={onToggleRecentChanges}
        tooltip={
          isRecentChangesOpen
            ? "Close recent changes"
            : "View recent file changes"
        }
        compact={compact}
        className={`${
          isRecentChangesOpen
            ? "bg-[var(--chat-button-hover)] text-[var(--chat-button-text)]"
            : stableStreaming
            ? "bg-[var(--chat-button-bg-streaming)] text-[var(--chat-button-text-streaming)] hover:bg-[var(--chat-button-hover-streaming)]"
            : "bg-[var(--chat-button-bg)] text-[var(--chat-button-text)] hover:bg-[var(--chat-button-hover)]"
        }`}
      >
        <GitBranch className={compact ? "w-2.5 h-2.5" : "w-3 h-3"} />
      </ChatButton>
    ) : null,

    compact: onCompact ? (
      <ChatButton
        key="compact"
        onClick={onCompact}
        disabled={disabled || stableStreaming || isCompacting}
        tooltip={
          isCompacting
            ? "Compacting context..."
            : stableStreaming
            ? "Cannot compact while running"
            : "Compact context (summarize conversation)"
        }
        compact={compact}
        className={`${
          isCompacting
            ? "bg-primary/20 text-primary border-primary/30 animate-pulse"
            : stableStreaming
            ? "bg-[var(--chat-button-bg-streaming)] text-[var(--chat-button-text-streaming)] opacity-50 cursor-not-allowed"
            : "bg-[var(--chat-button-bg)] text-[var(--chat-button-text)] hover:bg-[var(--chat-button-hover)]"
        }`}
      >
        <Minimize2 className={compact ? "w-2.5 h-2.5" : "w-3 h-3"} />
      </ChatButton>
    ) : null,

    devTool:
      isDev && onToggleForceStreaming ? (
        <ChatButton
          key="devTool"
          onClick={onToggleForceStreaming}
          tooltip={`Streaming UI: ${forceStreaming ? "ON" : "OFF"} (Test mode)`}
          compact={compact}
          className={
            forceStreaming
              ? "bg-[var(--chat-button-hover)] text-[var(--chat-button-text)]"
              : "bg-[var(--chat-button-bg)] text-[var(--chat-button-text)] hover:bg-[var(--chat-button-hover)]"
          }
        >
          <Sparkles className={compact ? "w-2.5 h-2.5" : "w-3 h-3"} />
        </ChatButton>
      ) : null,

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
          className={`${
            stableStreaming
              ? forceStreaming
                ? "bg-primary/20 text-primary border-primary/30 hover:bg-primary/30"
                : "bg-destructive/10 text-destructive border-destructive/20 hover:bg-destructive/20"
              : "bg-[var(--chat-button-bg)] text-[var(--chat-button-text)] border-[var(--chat-border)] hover:bg-[var(--chat-button-hover)]"
          }`}
        >
          <span className={compact ? "" : "inline-flex items-center justify-center"} style={compact ? {} : { width: '46px' }}>
            {effectiveStreaming ? (
              <>
                {compact ? (
                  <Square className="w-3 h-3" />
                ) : (
                  <>
                    <Square className="w-3.5 h-3.5" />
                    <span className="ml-1 text-[10px]">Esc</span>
                  </>
                )}
              </>
            ) : (
              <>
                {compact ? (
                  <CornerDownLeft className="w-3.5 h-3.5" />
                ) : (
                  <>
                    <span className="text-[10px]">Send</span>
                    <CornerDownLeft className="w-3 h-3 ml-0.5" />
                  </>
                )}
              </>
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