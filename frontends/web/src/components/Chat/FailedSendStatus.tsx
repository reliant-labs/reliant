/**
 * The status line under a message that was not sent.
 *
 * A send that failed used to leave its bubble dimmed ("Waiting for message to
 * save") until some later message or reopen silently took it away — and since
 * the composer clears on send, re-sending meant retyping. A refused send
 * leaves nothing on the server, so this bubble is the only copy of the text.
 * It now says so, and offers to send it again or to drop it.
 */

import { useState } from "react";
import { AlertCircle, Loader2, RotateCcw, X } from "lucide-react";
import { canRetryFailedSend, useChatStore } from "../../store/chatStore";
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

  const retry = async () => {
    setRetrying(true);
    try {
      await useChatStore.getState().retryFailedSend(chatId, clientMessageId);
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
      {canRetry && (
        <button
          type="button"
          onClick={() => void retry()}
          disabled={retrying}
          className="flex items-center gap-1 rounded px-1 font-medium text-primary hover:underline disabled:cursor-default disabled:no-underline disabled:opacity-60"
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
