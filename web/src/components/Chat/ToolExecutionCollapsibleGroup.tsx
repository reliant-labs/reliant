/**
 * Collapsible group for read-only tools (view, grep, websearch, etc.)
 * Similar to Cursor's "Explored X files Y searches" grouping
 */

import { useState, memo } from 'react';
import { ChevronDown, ChevronRight, Eye } from 'lucide-react';
import { cn } from '../../lib/utils';
import { ToolExecution, type ToolResultData } from './ToolExecution';
import type { ToolApprovalRequest } from '../../api/client';
import { generateReadOnlyToolsSummary } from '../../lib/toolFormatters';
import { shouldToolBeCollapsed } from '../Settings/ToolCallSettings';

type ToolCallData = {
  id: string;
  name: string;
  input: Record<string, unknown> | string;
  finished?: boolean;
};

interface ToolExecutionCollapsibleGroupProps {
  executions: Array<{
    call: ToolCallData;
    result?: ToolResultData;
    approval?: ToolApprovalRequest;
    status?: 'pending' | 'preparing' | 'requested' | 'writing_input' | 'executing' | 'cancelling' | 'cancelled' | 'completed' | 'backgrounded' | 'denied' | 'failed';
    onCancel?: (id: string) => void;
    onConvertToBackground?: (id: string) => void;
  }>;
  messageId?: string;
  chatId?: string;
  showRichContent?: boolean;
  onSelectThread?: (threadId: string | null) => void;
}

function ToolExecutionCollapsibleGroupComponent({
  executions,
  messageId,
  chatId,
  showRichContent = false,
  onSelectThread,
}: ToolExecutionCollapsibleGroupProps) {
  // Determine initial expanded state based on first tool's settings
  // If any tool in the group should be expanded, expand the group
  const [isExpanded, setIsExpanded] = useState(() => {
    // Check if any tool in the group should be expanded by default
    return executions.some(({ call }) => !shouldToolBeCollapsed(call.name));
  });

  // Generate summary text like "Explored 2 files 1 search"
  const summaryText = generateReadOnlyToolsSummary(
    executions.map(({ call }) => ({
      name: call.name,
      input: call.input,
    }))
  );

  // Check if all tools are completed or running in background
  const allCompleted = executions.every(
    ({ status, result }) => status === 'completed' || status === 'backgrounded' || result !== undefined
  );

  return (
    <div
      className={cn(
        'border rounded-lg overflow-hidden',
        allCompleted
          ? 'border-muted/30 bg-muted/5'
          : 'border-muted/50 bg-muted/10'
      )}
    >
      {/* Collapsible header */}
      <div
        className="flex items-center justify-between px-1.5 py-1 cursor-pointer hover:bg-muted/30 transition-colors"
        onClick={() => setIsExpanded(!isExpanded)}
      >
        <div className="flex items-center gap-1.5">
          <Eye className="w-3 h-3 text-muted-foreground" />
          <span className="text-[11px] font-mono text-muted-foreground">
            {summaryText}
          </span>
        </div>

        <div className="flex items-center gap-1">
          {isExpanded ? (
            <ChevronDown className="w-3 h-3 text-muted-foreground" />
          ) : (
            <ChevronRight className="w-3 h-3 text-muted-foreground" />
          )}
        </div>
      </div>

      {/* Expanded content - show individual tool executions */}
      {isExpanded && (
        <div className="border-t border-muted/30 p-1 space-y-1">
          {executions.map((execution, index) => (
            <ToolExecution
              key={`${messageId || 'msg'}-readonly-${index}-${execution.call.id}`}
              toolCall={execution.call}
              toolResult={execution.result}
              status={execution.status}
              onCancel={execution.onCancel}
              onConvertToBackground={execution.onConvertToBackground}
              approval={execution.approval}
              chatId={chatId}
              showRichContent={showRichContent}
              onSelectThread={onSelectThread}
            />
          ))}
        </div>
      )}
    </div>
  );
}

export const ToolExecutionCollapsibleGroup = memo(ToolExecutionCollapsibleGroupComponent);