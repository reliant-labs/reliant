import { useState, useMemo, memo } from "react";
import { ContentBlockType, MessageRole } from "../../gen/reliant/v1/chat_pb";
import { History } from "lucide-react";
import { QuietRow } from "./QuietRow";
import { MarkdownRenderer } from "./MarkdownRenderer";
import { useChat } from "../../store/chatStoreHooks";
import type { Message } from "../../api/client";

interface CompactionMessageProps {
  message: Message;
  chatId?: string | null;
}

// Prefix that identifies compaction messages
const COMPACTION_PREFIX = "This session is being continued from a previous conversation";

/**
 * Extract text content from a proto Message's contentBlocks
 */
function getTextContent(message: Message): string {
  const blocks = message.contentBlocks || [];
  const textBlock = blocks.find((b) => b.type === ContentBlockType.TEXT);
  return textBlock?.content || "";
}

/**
 * Checks if a message is a compaction/continuation message
 */
export function isCompactionMessage(message: Message): boolean {
  if (message.role !== MessageRole.SYSTEM) return false;
  const textContent = getTextContent(message);
  return textContent.startsWith(COMPACTION_PREFIX);
}

/**
 * Extracts and processes the summary content from a compaction message.
 * - Strips <analysis> tags (LLM thinking, not useful for users)
 * - Extracts content from <summary> tags if present
 * - Returns cleaned content for display
 */
function processCompactionContent(textContent: string): {
  summary: string;
  hasAnalysis: boolean;
} {
  // Check if there's an analysis section
  const hasAnalysis = /<analysis>[\s\S]*?<\/analysis>/i.test(textContent);
  
  // Strip <analysis> tags and their content
  let processed = textContent.replace(/<analysis>[\s\S]*?<\/analysis>/gi, "");
  
  // Extract content from <summary> tags if present
  const summaryMatch = processed.match(/<summary>([\s\S]*?)<\/summary>/i);
  if (summaryMatch) {
    processed = summaryMatch[1].trim();
  }
  
  // Remove the prefix since we'll show it separately
  processed = processed.replace(COMPACTION_PREFIX, "").trim();
  
  // Clean up any leading "that ran out of context" or similar
  processed = processed.replace(/^that ran out of context\.?\s*The conversation is summarized below:\s*/i, "").trim();
  processed = processed.replace(/^\.?\s*The conversation is summarized below:\s*/i, "").trim();
  
  return {
    summary: processed,
    hasAnalysis,
  };
}

/**
 * CompactionMessage - Renders compaction/continuation messages in a collapsed format
 * 
 * Compaction messages are system messages created when context window is full.
 * They contain a detailed summary of the previous conversation.
 * 
 * This component shows them collapsed by default to reduce visual noise,
 * with an option to expand and see the full summary.
 */
export const CompactionMessage = memo(function CompactionMessage({
  message,
  chatId,
}: CompactionMessageProps) {
  const [isExpanded, setIsExpanded] = useState(false);
  
  // Get worktree_id from chat data for file link context
  const currentChat = useChat(chatId || "");
  const worktreeId = currentChat?.worktreeId;
  
  const textContent = getTextContent(message);
  const { summary } = useMemo(
    () => processCompactionContent(textContent),
    [textContent]
  );
  
  return (
    <QuietRow
      as="button"
      icon={<History className="size-4" />}
      expanded={isExpanded}
      onToggle={() => setIsExpanded(!isExpanded)}
      ariaLabel={`${isExpanded ? "Hide" : "Show"} summary`}
      detail={
        <MarkdownRenderer
          content={summary}
          isUser={false}
          isStreaming={false}
          worktreeId={worktreeId}
          className="text-ui"
        />
      }
    >
      <span className="truncate">Context continued from previous session</span>
    </QuietRow>
  );
});
