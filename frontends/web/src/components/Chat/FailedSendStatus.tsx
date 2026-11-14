/**
 * The status line under a message that was not sent.
 *
 * A send that failed used to leave its bubble dimmed ("Waiting for message to
 * save") until some later message or reopen silently took it away — and since
 * the composer clears on send, re-sending meant retyping. A refused send
 * leaves nothing on the server, so this bubble is the only copy of the text.
 * It now says so, and offers to send it again or to drop it.
 *
 * A message refused because several agents run in its run
 * (run_recipient_ambiguous) would be refused again by a plain Retry, so it
 * offers the agents the refusal listed instead: each sends the message to
 * that agent's thread (reliant#2465).
 */

import { useState } from "react";
import { AlertCircle, Loader2, RotateCcw, X } from "lucide-react";
import { canRetryFailedSend, failedSendRecipients, useChatStore } from "../../store/chatStore";
import { runRecipientLabel } from "../../lib/runRecipient";
import { toast } from "../../lib/toast-manager";
import { cn } from "../../lib/utils";

export interface FailedSendStatusProps {
  chatId: string;
  clientMessageId: string;
  className?: string;
}

export function FailedSendStatus({ chatId, clientMessageId, className }: FailedSendStatusProps) {
  const [retrying, setRetrying] = useState(false);
  // After a reload the payload is gone: the text is still here to copy, but
  // there is nothing to resend from.
  const canRetry = canRetryFailedSend(clientMessageId);
  const recipients = canRetry ? failedSendRecipients(clientMessageId) : [];

  const retry = async (targetThread?: string) => {
    setRetrying(true);
    try {
      const store = useChatStore.getState();
      await (targetThread
        ? store.retryFailedSend(chatId, clientMessageId, targetThread)
        : store.retryFailedSend(chatId, clientMessageId));
    } catch (error) {
      toast.error(error);
    } finally {
      setRetrying(false);
    }
  };

  return (
    <div
      role="status"
      data-testid="failed-send-status"
      className={cn("mt-1 flex items-center justify-end gap-2 text-xs", className)}
    >
      <span className="flex items-center gap-1 font-medium text-destructive">
        <AlertCircle className="h-3.5 w-3.5" aria-hidden="true" />
        Not sent
      </span>
      {recipients.length > 0 && (
        <span role="group" aria-label="Send to" className="flex flex-wrap items-center justify-end gap-1">
          <span className="text-muted-foreground">Send to</span>
          {recipients.map((recipient) => (
            <button
              key={recipient.threadId}
              type="button"
              onClick={() => void retry(recipient.threadId)}
              disabled={retrying}
              title={recipient.node ? `${recipient.node} · thread ${recipient.threadId}` : `thread ${recipient.threadId}`}
              className="max-w-[12rem] truncate rounded border border-border px-1.5 font-medium text-primary-ink hover:bg-foreground/5 disabled:cursor-default disabled:opacity-60"
            >
              {runRecipientLabel(recipient)}
            </button>
          ))}
        </span>
      )}
      {canRetry && recipients.length === 0 && (
        <button
          type="button"
          onClick={() => void retry()}
          disabled={retrying}
          className="flex items-center gap-1 rounded px-1 font-medium text-primary-ink hover:underline disabled:cursor-default disabled:no-underline disabled:opacity-60"
        >
          {retrying ? (
            <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" />
          ) : (
            <RotateCcw className="h-3 w-3" aria-hidden="true" />
          )}
          Retry
        </button>
      )}
      <button
        type="button"
        onClick={() => useChatStore.getState().discardFailedSend(chatId, clientMessageId)}
        disabled={retrying}
        className="flex items-center gap-1 rounded px-1 text-muted-foreground hover:text-foreground disabled:cursor-default disabled:opacity-60"
      >
        <X className="h-3 w-3" aria-hidden="true" />
        Remove
      </button>
    </div>
  );
}
