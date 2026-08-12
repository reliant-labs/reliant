/**
 * The pending-queue strip above the composer.
 *
 * A message queued to a running agent lands in agent_messages, not the
 * transcript — the agent reads it at its next step boundary. Until then it is
 * invisible: the send toast fires and nothing else happens, which reads as
 * "did that work?". This strip is the answer. It shows what is still waiting,
 * how long it has waited, and gives the user a way back out.
 *
 * The pending treatment (dashed border, muted surface) is deliberate: these
 * are NOT conversation entries yet, and styling them like transcript messages
 * would claim they are.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { Clock, Paperclip, Send, X } from "lucide-react";
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
   * The ordinary send path. "Send now" is cancel-then-send, so this is what
   * turns a revoked queue entry back into a real user turn.
   */
  onSendNow?: (body: string, attachmentIds?: string[]) => void | Promise<void>;
  /**
   * Whether the agent is currently working. An idle agent will never drain its
   * own mailbox, so queued rows under an idle agent are stranded — see the
   * auto-flush below.
   */
  isRunning?: boolean;
  /**
   * Whether the chat's workflow run is still LIVE — running, pending, or
   * paused. Distinct from isRunning, and the auto-flush keys off this one.
   *
   * isRunning is the composer's notion of "busy", which is deliberately false
   * in discuss mode and while a question is pending so the input stays
   * enabled. Both of those states sit on a PAUSED run, which resumes and
   * drains its mailbox normally. Auto-flushing on isRunning alone would claim
   * rows out from under a live agent that was going to read them — the exact
   * theft the mailbox exists to prevent. Mirrors WorkflowStatusIsLive in
   * internal/db/core/chat.go.
   */
  isWorkflowLive?: boolean;
}

export function QueuedMessages({
  chatId,
  threadId,
  messages,
  onRefresh,
  onForget,
  onSendNow,
  isRunning = false,
  isWorkflowLive = false,
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
   * still stands. Returning that verdict — rather than swallowing it — is what
   * lets "send now" know whether it is allowed to send.
   */
  const cancel = useCallback(
    async (messageId: string): Promise<boolean> => {
      if (!chatId) return false;
      const response = await chatGrpc.cancelQueuedAgentMessage(chatId, messageId);
      if (!response.success) {
        // Already delivered. Say so and re-read, rather than pretending the
        // message is gone when the agent has it.
        toast.info(response.message);
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
   * "Send now" and "Send all" are both a single atomic claim followed by a
   * send of exactly what the claim returned.
   *
   * This used to be cancel-then-send from the client: cancel, check whether
   * the cancel took, and only then send. That left a real window between the
   * two calls, and a bulk version would have multiplied it by the size of the
   * queue. The claim takes the rows and returns them in one statement, so
   * whatever comes back is provably ours and the agent provably never got it.
   *
   * Sending exactly the returned bodies — never the local `pending` view — is
   * the load-bearing half. A message the agent drained first is absent from
   * the result, and resending it from the stale local list is precisely the
   * double-send this replaced.
   */
  const claimAndSend = useCallback(
    async (messageId?: string, options?: { silentWhenEmpty?: boolean }) => {
      if (!chatId || !threadId || !onSendNow) return;
      try {
        const { messages: claimed } = await chatGrpc.claimQueuedAgentMessages(
          chatId,
          threadId,
          messageId,
        );
        if (claimed.length === 0) {
          // The drain won. Say so, and re-read so the strip stops showing
          // something the agent already has. The auto-flush suppresses this:
          // nobody asked it to do anything, so "too late to pull back" would
          // be answering a question the user never posed.
          if (!options?.silentWhenEmpty) {
            toast.info("Already delivered to the agent — too late to pull back.");
          }
          await onRefresh();
          return;
        }
        for (const message of claimed) {
          onForget?.(message.id);
        }
        // Sequential, because these are conversation turns and their order is
        // their meaning.
        for (const message of claimed) {
          await onSendNow(message.body, message.attachments);
        }
        await onRefresh();
      } catch (error) {
        logger.error("[QueuedMessages] Failed to claim and send queued messages", {
          error,
          chatId,
          threadId,
          messageId,
        });
        toast.error(error);
        await onRefresh();
      }
    },
    [chatId, threadId, onSendNow, onRefresh, onForget],
  );

  const handleSendNow = useCallback(
    (message: QueuedAgentMessageView) =>
      withBusy(message.id, async () => {
        await claimAndSend(message.id);
      }),
    [claimAndSend, withBusy],
  );

  const [sendingAll, setSendingAll] = useState(false);

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

  const handleSendAll = useCallback(async () => {
    setSendingAll(true);
    try {
      await claimAndSend();
    } finally {
      setSendingAll(false);
    }
  }, [claimAndSend]);

  /**
   * Flush a stranded queue when the agent is idle.
   *
   * A queued message is delivered by exactly one thing: the agent's own drain
   * at its next loop-step boundary. If the run ends before that boundary comes
   * — and the last boundary of a run always does — the row simply stays queued
   * and nothing on either side will ever send it. The strip shows it, the poll
   * stops, and the user's message sits there until they type something else to
   * shake it loose. That is the whole defect, and the fix is to do here what
   * the user would otherwise have to do by hand: press "Send all".
   *
   * It is deliberately the SAME claim-then-send-what-came-back path. Sending
   * the local view instead would resend a message the agent drained on its way
   * out, and an idle-edge flush is precisely when that race is live.
   */
  const claimAndSendRef = useRef(claimAndSend);
  claimAndSendRef.current = claimAndSend;

  // Once per idle transition. A ref, not state, because two renders inside one
  // transition must not both see "not flushed yet" — and re-arming only when
  // the agent picks back up is what makes a transition the unit.
  const autoFlushedRef = useRef(false);

  useEffect(() => {
    if (isRunning || isWorkflowLive) {
      // A live run drains its own mailbox correctly, whether it is executing
      // right now or paused mid-question waiting to resume. Stay out of its
      // way, and arm the guard for the idle edge it will eventually reach.
      autoFlushedRef.current = false;
      return;
    }
    if (autoFlushedRef.current) return;
    if (!hasPending || !chatId || !threadId || !onSendNow) return;
    // The user is already flushing this queue by hand. Claiming underneath
    // them would be a second claim against rows they are mid-send with —
    // harmless server-side, but the effect re-runs when they finish, and by
    // then the queue reflects what they actually did.
    if (sendingAll || busyIds.size > 0) return;

    autoFlushedRef.current = true;
    void claimAndSendRef.current(undefined, { silentWhenEmpty: true });
  }, [
    isRunning,
    isWorkflowLive,
    hasPending,
    chatId,
    threadId,
    onSendNow,
    sendingAll,
    busyIds,
  ]);

  // Nothing queued means nothing to say. An empty container here would be a
  // permanent gap above the composer.
  if (!hasPending) return null;

  return (
    <div
      className="flex flex-col gap-1 pb-1.5"
      data-testid="queued-messages"
      aria-label="Messages queued for the running agent"
    >
      {/* Only worth offering once there is more than one thing to flush —
          with a single entry this is just a second "Send now". */}
      {onSendNow && pending.length > 1 && (
        <div className="flex justify-end">
          <button
            type="button"
            aria-label="Send all queued messages now"
            disabled={sendingAll}
            onClick={() => void handleSendAll()}
            className={cn(
              "flex h-6 items-center gap-1 rounded-full px-2 text-[10px] font-medium transition-colors",
              "text-muted-foreground hover:bg-accent hover:text-foreground",
              sendingAll && "cursor-default opacity-60 hover:bg-transparent",
            )}
          >
            <Send className="h-3 w-3" />
            Send all {pending.length} now
          </button>
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
        // A send-all in flight owns every row, so the per-row controls go
        // busy with it — otherwise a click could try to claim a message the
        // bulk call has already taken.
        const isBusy = busyIds.has(message.id) || sendingAll;
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
                  className="mt-0.5 text-[10px] font-medium text-muted-foreground/90 underline-offset-2 hover:text-foreground hover:underline"
                >
                  {isExpanded ? "Show less" : "Show more"}
                </button>
              )}
              <span className="flex items-center gap-1 text-[10px] text-muted-foreground/80">
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

            <div className="flex flex-shrink-0 items-center gap-1">
              {onSendNow && (
                <Tooltip
                  content="Pull this back out of the queue and send it as a new message now"
                  placement="top"
                >
                  <button
                    type="button"
                    aria-label="Send now"
                    disabled={isBusy}
                    onClick={() => void handleSendNow(message)}
                    className={cn(
                      "flex h-6 items-center gap-1 rounded-full px-2 text-[10px] font-medium transition-colors",
                      "text-muted-foreground hover:bg-accent hover:text-foreground",
                      isBusy && "cursor-default opacity-60 hover:bg-transparent",
                    )}
                  >
                    <Send className="h-3 w-3" />
                    Send now
                  </button>
                </Tooltip>
              )}

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
