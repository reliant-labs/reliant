import { memo, useMemo, useState } from "react";
import { ChevronRight, Bot } from "lucide-react";
import { ContentBlockType } from "../../gen/reliant/v1/chat_pb";
import type { Message } from "../../api/client";
import { agentMessageSubject } from "../../lib/agentMessageSubject";
import { cn } from "../../lib/utils";
import { MarkdownRenderer } from "./MarkdownRenderer";

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

/**
 * A message from another agent: one compact line (its subject, and who sent
 * it), collapsed by default and expandable to the full body. Never a user
 * bubble. Agent-to-agent messages are routinely long reports, and showing
 * each in full buried the conversation the human is actually having.
 *
 * The line is the sender's `subject` (spawn_send requires one); a message
 * without one falls back to the first line of its body.
 */
export const AgentMessageCard = memo(function AgentMessageCard({
  message,
  onSelectThread,
}: AgentMessageCardProps) {
  const sender = message.sender;
  const body = useMemo(() => textOf(message), [message]);
  const subject = useMemo(() => agentMessageSubject(sender?.subject, body), [sender?.subject, body]);
  const [expanded, setExpanded] = useState(false);
  const title = sender?.title || "another agent";

  return (
    <div data-testid="agent-message-card" className="message-content w-full min-w-0 px-2 mb-2">
      <div className="min-w-0 rounded-md border border-border/60 bg-background text-muted-foreground">
        <div className="flex min-w-0 items-center gap-1.5 px-2 py-1 text-xs">
          <button
            type="button"
            onClick={() => setExpanded((value) => !value)}
            className="flex min-w-0 flex-1 items-center gap-1.5 text-left hover:text-foreground"
            aria-expanded={expanded}
            title={expanded ? "Collapse message" : "Expand message"}
          >
            <ChevronRight
              className={cn("h-3 w-3 shrink-0 transition-transform", expanded && "rotate-90")}
              aria-hidden
            />
            <Bot className="h-3.5 w-3.5 shrink-0" aria-hidden />
            <span data-testid="agent-message-subject" className="min-w-0 truncate text-foreground">
              {subject || "Message"}
            </span>
          </button>
          <span className="shrink-0">from</span>
          {sender?.threadId && onSelectThread ? (
            <button
              type="button"
              onClick={() => onSelectThread(sender.threadId)}
              className="min-w-0 max-w-[40%] truncate font-medium text-foreground hover:underline"
              title={`Open ${title}`}
            >
              {title}
            </button>
          ) : (
            <span className="min-w-0 max-w-[40%] truncate font-medium text-foreground">{title}</span>
          )}
        </div>
        {expanded && body && (
          <div data-testid="agent-message-body" className="min-w-0 border-t border-border/60 px-3 py-2 text-sm">
            <MarkdownRenderer content={body} isUser={false} className="text-sm w-full" />
          </div>
        )}
      </div>
    </div>
  );
});
