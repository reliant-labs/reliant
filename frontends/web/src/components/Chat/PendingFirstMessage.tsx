/**
 * The first message of a chat that is still being created.
 *
 * A new chat has no id until StartChat answers, so there is no transcript to
 * put the message in yet — and the composer has already let go of it. Without
 * this, the user pressed send and watched an unchanged new-chat screen over an
 * empty box for as long as StartChat took (p50 2.3s, up to 6.4s measured in
 * prod), which reads as "my message vanished".
 *
 * So the new-chat screen renders the message here, styled as the transcript's
 * own pending user bubble (ChatMessage: `optimistic-*` → opacity-60), and swaps
 * to the real chat when it exists. The chat then shows the same message in the
 * same place, so the swap reads as the chat appearing around it.
 */

import { Loader2, Paperclip } from "lucide-react";
import { cn } from "../../lib/utils";

export interface PendingFirstMessageProps {
  content: string;
  attachmentCount?: number;
  className?: string;
}

export function PendingFirstMessage({
  content,
  attachmentCount = 0,
  className,
}: PendingFirstMessageProps) {
  return (
    <div
      data-testid="pending-first-message"
      className={cn("flex w-full flex-col items-end gap-1.5", className)}
    >
      <div className="max-w-[85%] rounded-2xl border border-primary/25 bg-primary/15 px-3.5 py-2.5 text-sm leading-relaxed text-foreground opacity-60 shadow-sm sm:max-w-2xl">
        {content && (
          <p className="line-clamp-12 whitespace-pre-wrap break-words">{content}</p>
        )}
        {attachmentCount > 0 && (
          <span className="mt-1 flex items-center gap-1 text-xs text-muted-foreground">
            <Paperclip className="h-3 w-3" aria-hidden="true" />
            {attachmentCount === 1 ? "1 attachment" : `${attachmentCount} attachments`}
          </span>
        )}
      </div>
      <p role="status" className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" />
        Starting your chat…
      </p>
    </div>
  );
}
