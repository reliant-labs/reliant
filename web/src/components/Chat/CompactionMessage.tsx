import { useState, useMemo, memo } from "react";
import { ContentBlockType, MessageRole } from "../../gen/reliant/v1/chat_pb";
import { ChevronDown, ChevronUp, History } from "lucide-react";
import { cn } from "../../lib/utils";
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
    <div className="mb-3 mx-2">
      <div
        className={cn(
          "border rounded-lg transition-colors duration-200",
          "bg-muted/30 border-border/50",
          "hover:border-border/70"
        )}
      >
        {/* Collapsed header - always visible */}
        <button
          onClick={() => setIsExpanded(!isExpanded)}
          className={cn(
            "w-full flex items-center gap-2 px-3 py-2 text-left",
            "text-sm text-muted-foreground hover:text-foreground",
            "transition-colors duration-200"
          )}
        >
          <History className="w-4 h-4 flex-shrink-0 text-blue-500" />
          <span className="flex-1 font-medium">
            Context continued from previous session
          </span>
          <span className="text-xs opacity-70">
            {isExpanded ? "Hide" : "Show"} summary
          </span>
          {isExpanded ? (
            <ChevronUp className="w-4 h-4 flex-shrink-0" />
          ) : (
            <ChevronDown className="w-4 h-4 flex-shrink-0" />
          )}
        </button>
        
        {/* Expanded content */}
        {isExpanded && (
          <div className="px-3 pb-3 border-t border-border/30">
            <div className="pt-3 text-sm">
              <MarkdownRenderer
                content={summary}
                isUser={false}
                isStreaming={false}
                worktreeId={worktreeId}
                className="text-sm"
              />
            </div>
          </div>
        )}
      </div>
    </div>
  );
});
