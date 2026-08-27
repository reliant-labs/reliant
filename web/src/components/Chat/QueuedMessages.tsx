/**
 * The pending-queue strip above the composer.
 *
 * A message queued to a running agent lands in agent_messages, not the
 * transcript — the agent reads it on its next turn, when call_llm drains the
 * mailbox before assembling history. Until then it is invisible unless this
 * strip shows what is still waiting, how long it has waited, and gives the user
 * two ways to act on it: drop an entry, or interrupt the agent so it reads the
 * queue now instead of after the work in flight.
 *
 * The pending treatment (dashed border, muted surface) is deliberate: these
 * are NOT conversation entries yet, and styling them like transcript messages
 * would claim they are.
 */

import { useCallback, useEffect, useState } from "react";
import { Clock, OctagonX, Paperclip, X } from "lucide-react";
import { cn } from "../../lib/utils";
import { toast } from "../../lib/toast-manager";
import { logger } from "../../lib/logger";
import { Tooltip } from "../ui/Tooltip";
import {
  chatGrpc,
  QUEUED_SENDER_KIND_HUMAN,
  type QueuedAgentMessageView,
} from "../../api/chat-grpc";


const AGE_TICK_MS = 1_000;

/**
 * Bodies longer than this collapse to a few lines with a "Show more" toggle.
 * Chosen to be comfortably longer than an ordinary steering message ("check
 * the logs first") and shorter than the pasted logs and diffs that are what
 * actually overwhelm the strip.
 */
const LONG_BODY_CHARS = 280;

/** "queued 12s ago" — coarse on purpose; the exact second stops mattering fast. */
export function formatQueuedAge(createdAt: string, now: number): string {
  const started = Date.parse(createdAt);
  if (Number.isNaN(started)) return "queued";

  const seconds = Math.max(0, Math.round((now - started) / 1000));
  if (seconds < 60) return `queued ${seconds}s ago`;

  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `queued ${minutes}m ago`;

  const hours = Math.floor(minutes / 60);
  return `queued ${hours}h ago`;
}

interface QueuedMessagesProps {
  chatId?: string;
  /** The thread whose mailbox these entries are addressed to. */
  threadId?: string;
  messages: QueuedAgentMessageView[];
  /** Re-read the mailbox from the server. */
  onRefresh: () => Promise<void> | void;
  /**
   * Drop one entry for good once the server has confirmed it is ours. Durable
   * against a poll that predates the removal, which would otherwise answer
   * with a snapshot that still contains the row and put it back on screen.
   */
  onForget?: (messageId: string) => void;
  /**
   * Called after a successful interrupt, so the composer can refresh whatever
   * it derives from the run's state. Optional: interrupt is complete without
   * it. Its PRESENCE is also what enables the interrupt controls, since a
   * caller that cannot observe the result has no business triggering one.
   */
  onInterrupted?: () => void | Promise<void>;
  /**
   * Whether the agent is currently working. Interrupting only makes sense
   * while it is: an idle agent has no work in flight to stop.
   */
  isRunning?: boolean;
}

export function QueuedMessages({
  chatId,
  threadId,
  messages,
  onRefresh,
  onForget,
  onInterrupted,
  isRunning = false,
}: QueuedMessagesProps) {
  // Only the user's own queued messages are actionable here. A peer agent's
  // spawn_send message is not the human's to revoke.
  const pending = messages.filter(
    (m) => m.sender_kind === QUEUED_SENDER_KIND_HUMAN,
  );

  // Which message ids have an RPC in flight, so their buttons can't be
  // double-fired while the round-trip is open.
  const [busyIds, setBusyIds] = useState<ReadonlySet<string>>(() => new Set());
  const [now, setNow] = useState(() => Date.now());

  const hasPending = pending.length > 0;

  // The age labels are the only thing that needs a clock, so the interval only
  // runs while something is actually queued.
  useEffect(() => {
    if (!hasPending) return;
    setNow(Date.now());
    const id = setInterval(() => setNow(Date.now()), AGE_TICK_MS);
    return () => clearInterval(id);
  }, [hasPending]);

  /**
   * Cancel is a race against the agent's own drain, so the backend answers
   * honestly: `success: false` means it was already delivered and the row
   * still stands. Re-reading rather than swallowing the verdict is what keeps
   * the strip from claiming a message is gone when the agent has it.
   */
  const cancel = useCallback(
    async (messageId: string): Promise<boolean> => {
      if (!chatId) return false;
      const response = await chatGrpc.cancelQueuedAgentMessage(chatId, messageId);
      if (!response.success) {
        // Already delivered. Re-read rather than pretending the message is
        // gone when the agent has it.
        await onRefresh();
        return false;
      }
      onForget?.(messageId);
      await onRefresh();
      return true;
    },
    [chatId, onRefresh, onForget],
  );

  const withBusy = useCallback(
    async (messageId: string, work: () => Promise<void>) => {
      setBusyIds((prev) => new Set(prev).add(messageId));
      try {
        await work();
      } finally {
        setBusyIds((prev) => {
          const next = new Set(prev);
          next.delete(messageId);
          return next;
        });
      }
    },
    [],
  );

  const handleCancel = useCallback(
    (messageId: string) =>
      withBusy(messageId, async () => {
        try {
          await cancel(messageId);
        } catch (error) {
          logger.error("[QueuedMessages] Failed to cancel queued message", {
            error,
            chatId,
            messageId,
          });
          toast.error(error);
        }
      }),
    [cancel, chatId, withBusy],
  );

  /**
   * Interrupt the agent so it reads the queue now.
   *
   * This does NOT pull messages back out of the mailbox. It stops the work in
   * flight — cancelling the thread's executing tool calls — and the agent's
   * next call_llm delivers the whole queue, oldest first, exactly as an
   * uninterrupted turn would have.
   *
   * That is why there is one button rather than a per-message one. The old
   * design claimed a row and resent it as a fresh turn, which meant "send this
   * one now" had to answer "what about the other two?" and raced the agent's
   * own delivery to do it. Interrupting answers both: everything queued
   * arrives, in the order it was typed, with no row to claim and no race to
   * lose.
   */
  const [interrupting, setInterrupting] = useState(false);

  const handleInterrupt = useCallback(async () => {
    if (!chatId || !threadId || !onInterrupted) return;
    setInterrupting(true);
    try {
      await chatGrpc.interruptThread(chatId, threadId);

      await onInterrupted();
      await onRefresh();
    } catch (error) {
      logger.error("[QueuedMessages] Failed to interrupt the agent", {
        error,
        chatId,
        threadId,
      });
      toast.error(error);
      await onRefresh();
    } finally {
      setInterrupting(false);
    }
  }, [chatId, threadId, onInterrupted, onRefresh]);

  // Which long bodies the user has opened. Keyed by message id so an entry
  // being claimed or cancelled simply drops out.
  const [expandedIds, setExpandedIds] = useState<ReadonlySet<string>>(
    () => new Set(),
  );

  const toggleExpanded = useCallback((messageId: string) => {
    setExpandedIds((prev) => {
      const next = new Set(prev);
      if (!next.delete(messageId)) next.add(messageId);
      return next;
    });
  }, []);

  // The client-side auto-flush that used to live here is gone.
  //
  // It existed because delivery happened at the agent loop's step boundary, so
  // a run that ended before the next boundary left its queue stranded with
  // nothing on either side to send it — and the strip papered over that by
  // silently claiming and resending the rows when the agent went idle.
  //
  // Both halves are now handled where they belong. A live agent delivers in
  // call_llm, which every LLM-calling workflow reaches regardless of graph
  // shape. An idle one is swept by SendMessage's absorbQueuedMailbox on the
  // user's next turn, and anything genuinely undeliverable (the thread really
  // exited) is marked by the reconciler's resolveOrphanedAgentMessages rather
  // than being silently resent from a stale client view.

  // Nothing queued means nothing to say. An empty container here would be a
  // permanent gap above the composer.
  if (!hasPending) return null;

  return (
    <div
      className="flex flex-col gap-1 pb-1.5"
      data-testid="queued-messages"
      aria-label="Messages queued for the running agent"
    >
      {/* One control for the whole queue, and only while the agent is actually
          working — interrupting an idle agent would stop nothing, and its
          queue is swept on the user's next turn anyway. */}
      {onInterrupted && isRunning && (
        <div className="flex justify-end">
          <Tooltip
            content="Stop what the agent is doing so it reads your queued messages now"
            placement="top"
          >
            <button
              type="button"
              aria-label="Interrupt the agent and deliver queued messages now"
              disabled={interrupting}
              onClick={() => void handleInterrupt()}
              className={cn(
                "flex h-6 items-center gap-1 rounded-full px-2 text-2xs font-medium transition-colors",
                "text-muted-foreground hover:bg-accent hover:text-foreground",
                interrupting && "cursor-default opacity-60 hover:bg-transparent",
              )}
            >
              <OctagonX className="h-3 w-3" />
              {interrupting ? "Interrupting…" : "Interrupt & send now"}
            </button>
          </Tooltip>
        </div>
      )}

      {/* The strip is a fixed-size dock, not a list that grows with its
          contents. It sits directly above the composer, so an uncapped queue
          — a few long messages, or many short ones — pushed the transcript
          off screen and made the conversation unscrollable. Capping here and
          scrolling inside means the composer never moves, whatever is queued. */}
      <div
        className={cn(
          "flex flex-col gap-1",
          "max-h-[30vh] overflow-y-auto overscroll-contain",
        )}
        data-testid="queued-messages-list"
      >
        {pending.map((message) => {
        // An interrupt in flight is about to deliver every row, so the per-row
        // cancel goes busy with it rather than racing that delivery.
        const isBusy = busyIds.has(message.id) || interrupting;
        const isLong = message.body.length > LONG_BODY_CHARS;
        const isExpanded = expandedIds.has(message.id);
        return (
          <div
            key={message.id}
            data-testid="queued-message"
            className={cn(
              "flex items-start gap-2 rounded-md border border-dashed border-border bg-muted/50 px-2.5 py-1.5",
              isBusy && "opacity-60",
            )}
          >
            <Clock className="mt-0.5 h-3 w-3 flex-shrink-0 text-muted-foreground" />

            <div className="min-w-0 flex-1">
              {/* One pasted stack trace should not be able to fill the dock
                  and hide every other queued message behind it. Collapsed is
                  the default; the full text is one click away and scrolls
                  within its own bounded box rather than growing the strip. */}
              <p
                className={cn(
                  "whitespace-pre-wrap break-words text-xs text-muted-foreground",
                  isLong && !isExpanded && "line-clamp-3",
                  isLong && isExpanded && "max-h-[40vh] overflow-y-auto overscroll-contain",
                )}
              >
                {message.body}
              </p>
              {isLong && (
                <button
                  type="button"
                  onClick={() => toggleExpanded(message.id)}
                  aria-expanded={isExpanded}
                  className="mt-0.5 text-2xs font-medium text-muted-foreground/90 underline-offset-2 hover:text-foreground hover:underline"
                >
                  {isExpanded ? "Show less" : "Show more"}
                </button>
              )}
              <span className="flex items-center gap-1 text-2xs text-muted-foreground/80">
                {formatQueuedAge(message.created_at, now)} · waiting for the
                agent's next turn
                {message.attachments.length > 0 && (
                  <span className="flex items-center gap-0.5">
                    <Paperclip className="h-2.5 w-2.5" />
                    {message.attachments.length}
                  </span>
                )}
              </span>
            </div>

            {/* No per-message send. Delivery is per-THREAD: the agent reads
                its whole mailbox on its next turn, in order. A button offering
                to send just this one would have to either reorder the queue or
                quietly send the rest too — and the version that claimed a
                single row raced the agent's own delivery to do it. */}
            <div className="flex flex-shrink-0 items-center gap-1">
              <Tooltip content="Remove this message from the queue" placement="top">
                <button
                  type="button"
                  aria-label="Cancel queued message"
                  disabled={isBusy}
                  onClick={() => void handleCancel(message.id)}
                  className={cn(
                    "flex h-6 w-6 items-center justify-center rounded-full transition-colors",
                    "text-muted-foreground hover:bg-accent hover:text-destructive",
                    isBusy && "cursor-default opacity-60 hover:bg-transparent",
                  )}
                >
                  <X className="h-3 w-3" />
                </button>
              </Tooltip>
            </div>
          </div>
        );
        })}
      </div>
    </div>
  );
}
