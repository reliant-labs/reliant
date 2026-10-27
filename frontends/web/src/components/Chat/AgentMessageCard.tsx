import { memo, useMemo, useState } from "react";
import { ChevronDown, ChevronUp, Bot } from "lucide-react";
import { ContentBlockType } from "../../gen/reliant/v1/chat_pb";
import type { Message } from "../../api/client";
import { cn } from "../../lib/utils";
import { MarkdownRenderer } from "./MarkdownRenderer";

/** Bodies longer than this (chars) or taller than COLLAPSE_LINES start collapsed. */
const COLLAPSE_CHARS = 600;
const COLLAPSE_LINES = 8;

/**
 * True for a message another agent wrote (spawn_send and the other mailbox
 * kinds). Such a message keeps role USER so the model sees a user turn, but it
 * is not the human's: it must not render as a user bubble, start a user turn
 * or become the pinned user header.
 */
export function isAgentMessage(message: Pick<Message, "sender">): boolean {
  return Boolean(message.sender?.threadId);
}

function textOf(message: Message): string {
  return (message.contentBlocks || [])
    .filter((block) => block.type === ContentBlockType.TEXT)
    .map((block) => block.content || "")
    .join("")
    .trim();
}

interface AgentMessageCardProps {
  message: Message;
  chatId?: string | null;
  onSelectThread?: (threadId: string | null) => void;
}

/** A message from another agent: compact, left-aligned and muted, never a user bubble. */
export const AgentMessageCard = memo(function AgentMessageCard({
  message,
  onSelectThread,
}: AgentMessageCardProps) {
  const sender = message.sender;
  const body = useMemo(() => textOf(message), [message]);
  const isLong = body.length > COLLAPSE_CHARS || body.split("\n").length > COLLAPSE_LINES;
  const [expanded, setExpanded] = useState(false);
  const collapsed = isLong && !expanded;
  const title = sender?.title || "another agent";

  return (
    <div
      data-testid="agent-message-card"
      className="message-content w-full px-2 mb-2 flex justify-start"
    >
      <div className="max-w-[85%] min-w-0 rounded-lg border border-border/60 bg-background px-3 py-2 text-muted-foreground">
        <div className="flex items-center gap-1.5 text-xs">
          <Bot className="h-3.5 w-3.5 shrink-0" aria-hidden />
          <span className="shrink-0">Message from</span>
          {sender?.threadId && onSelectThread ? (
            <button
              type="button"
              onClick={() => onSelectThread(sender.threadId)}
              className="min-w-0 truncate font-medium text-foreground hover:underline"
              title={`Open ${title}`}
            >
              {title}
            </button>
          ) : (
            <span className="min-w-0 truncate font-medium text-foreground">{title}</span>
          )}
        </div>
        {body && (
          <div
            className={cn(
              "mt-1.5 text-sm",
              collapsed && "max-h-40 overflow-hidden [mask-image:linear-gradient(to_bottom,black_70%,transparent)]",
            )}
          >
            <MarkdownRenderer content={body} isUser={false} className="text-sm w-full" />
          </div>
        )}
        {isLong && (
          <button
            type="button"
            onClick={() => setExpanded((value) => !value)}
            className="mt-1 flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
            aria-expanded={expanded}
          >
            {expanded ? <ChevronUp className="h-3 w-3" /> : <ChevronDown className="h-3 w-3" />}
            {expanded ? "Show less" : "Show more"}
          </button>
        )}
      </div>
    </div>
  );
});
