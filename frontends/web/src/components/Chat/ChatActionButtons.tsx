import { ArrowUp } from "lucide-react";
import { cn } from "../../lib/utils";
import Tooltip from "../forge-ui/tooltip";
import { COMPOSER_ROUND_BUTTON } from "./composerControl";

interface ComposerActionSlotProps {
  onSend: () => void;
  onStop?: () => void;
  /** Queue the text into the running agent's mailbox. Absent: running means stop only. */
  onQueue?: () => void;
  isStreaming: boolean;
  /** Text, attachments or file references: something a send would carry. */
  hasContent: boolean;
  /** Typed text. Only text can be queued, so it alone arms the queue state. */
  hasText: boolean;
  /** Blocks send and queue (uploads pending, a send in flight). Stop is never blocked. */
  disabled: boolean;
  /** Label of the key that sends, for the tooltip ("Enter", "⌘↵"). */
  sendKeyHint: string;
}

/**
 * The composer's one action slot (design doc §5.3), four states:
 *
 *   idle, empty            → Send, disabled
 *   idle, with content     → Send (filled)
 *   running, empty         → Stop
 *   running, with text     → ghost Stop, then Queue (filled)
 *
 * Running-with-text is the only state with two controls, and only while it
 * applies: queueing is a Reliant feature with its own visible state rather
 * than a hidden keyboard behavior.
 */
export function ComposerActionSlot({
  onSend,
  onStop,
  onQueue,
  isStreaming,
  hasContent,
  hasText,
  disabled,
  sendKeyHint,
}: ComposerActionSlotProps) {
  const stop = (ghost: boolean) => (
    <Tooltip content="Stop the agent (Esc)" side="top" delay={300} triggerClassName="inline-flex">
      <button
        type="button"
        onClick={() => onStop?.()}
        aria-label="Stop the agent"
        data-testid="stop-button"
        className={cn(
          COMPOSER_ROUND_BUTTON,
          ghost
            ? "text-muted-foreground hover:bg-foreground/5 hover:text-foreground"
            : "bg-foreground text-background hover:bg-foreground/85",
        )}
      >
        <span className="h-2.5 w-2.5 rounded-[2px] bg-current" aria-hidden />
      </button>
    </Tooltip>
  );

  if (isStreaming) {
    if (!hasText || !onQueue) return stop(false);
    return (
      <div className="flex items-center gap-1">
        {stop(true)}
        <Tooltip content={`Queue for the agent's next turn (${sendKeyHint})`} side="top" delay={300} triggerClassName="inline-flex">
          <button
            type="button"
            onClick={onQueue}
            disabled={disabled}
            aria-label="Queue message for the agent's next turn"
            data-testid="queue-send-button"
            className={cn(COMPOSER_ROUND_BUTTON, "bg-primary text-primary-foreground hover:bg-primary/90 disabled:opacity-50")}
          >
            <ArrowUp className="h-4 w-4" aria-hidden />
          </button>
        </Tooltip>
      </div>
    );
  }

  const canSend = hasContent && !disabled;
  return (
    <Tooltip content={`Send message (${sendKeyHint})`} side="top" delay={300} triggerClassName="inline-flex">
      <button
        type="button"
        onClick={onSend}
        disabled={!canSend}
        aria-label="Send message"
        data-testid="send-button"
        className={cn(
          COMPOSER_ROUND_BUTTON,
          canSend
            ? "bg-primary text-primary-foreground hover:bg-primary/90"
            : "cursor-not-allowed bg-foreground/10 text-muted-foreground",
        )}
      >
        <ArrowUp className="h-4 w-4" aria-hidden />
      </button>
    </Tooltip>
  );
}
