/**
 * Collapsible group for read-only tools (view, grep, websearch, etc.)
 * Similar to Cursor's "Explored X files Y searches" grouping
 */

import { useState, useEffect, memo } from 'react';
import { AlertCircle, ChevronDown, ChevronRight, Eye } from 'lucide-react';
import { cn } from '../../lib/utils';
import { ToolExecution, type ToolResultData } from './ToolExecution';
import type { ToolApprovalRequest } from '../../api/client';
import { generateReadOnlyToolsSummary } from '../../lib/toolFormatters';
import { shouldToolBeCollapsed, TOOL_COLLAPSE_SETTINGS_EVENT } from '../Settings/ToolCallSettings';
import { useSurface } from '../../lib/surfaceContext';

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
  density?: "compact" | "card" | "minimal";
}

function ToolExecutionCollapsibleGroupComponent({
  executions,
  messageId,
  chatId,
  showRichContent = false,
  onSelectThread,
  density = "compact",
}: ToolExecutionCollapsibleGroupProps) {
  // Determine initial expanded state based on first tool's settings
  // If any tool in the group should be expanded, expand the group
  const surface = useSurface();
  const [isExpanded, setIsExpanded] = useState(() => {
    // Check if any tool in the group should be expanded by default
    return executions.some(({ call }) => !shouldToolBeCollapsed(call.name, surface));
  });

  // Pick up preference changes made in Settings without a reload.
  useEffect(() => {
    const applyNewDefault = () => {
      setIsExpanded(executions.some(({ call }) => !shouldToolBeCollapsed(call.name, surface)));
    };
    window.addEventListener(TOOL_COLLAPSE_SETTINGS_EVENT, applyNewDefault);
    return () => window.removeEventListener(TOOL_COLLAPSE_SETTINGS_EVENT, applyNewDefault);
  }, [executions, surface]);

  // Generate summary text like "Explored 2 files 1 search"
  const summaryText = generateReadOnlyToolsSummary(
    executions.map(({ call }) => ({
      name: call.name,
      input: call.input,
    }))
  );

  const hasWarnings = executions.some(
    ({ status, result }) => status === 'failed' || result?.is_error
  );

  // Check if all tools are completed or running in background
  const allCompleted = executions.every(
    ({ status, result }) => status === 'completed' || status === 'backgrounded' || result !== undefined
  );

  return (
    <div
      className={cn(
        'border overflow-hidden',
        density === 'card' ? 'rounded-xl shadow-sm' : 'rounded-lg',
        density === 'minimal' && 'rounded-md shadow-none',
        hasWarnings
          ? 'border-warning/40 bg-warning/5'
          : allCompleted
          ? 'border-muted/30 bg-muted/5'
          : 'border-muted/50 bg-muted/10'
      )}
    >
      {/* Collapsible header */}
      <div
        className={cn(
          "flex items-center justify-between cursor-pointer hover:bg-muted/30 transition-colors",
          density === "card" ? "px-3 py-2" : "px-1.5 py-1"
        )}
        onClick={() => setIsExpanded(!isExpanded)}
      >
        <div className="flex items-center gap-1.5">
          {hasWarnings ? (
            <AlertCircle className="w-3.5 h-3.5 text-warning" />
          ) : (
            <Eye className="w-3.5 h-3.5 text-muted-foreground" />
          )}
          <span className={cn("text-xs font-mono", hasWarnings ? "text-warning" : "text-muted-foreground")}>
            {summaryText}
          </span>
        </div>

        <div className="flex items-center gap-1">
          {isExpanded ? (
            <ChevronDown className="w-3.5 h-3.5 text-muted-foreground" />
          ) : (
            <ChevronRight className="w-3.5 h-3.5 text-muted-foreground" />
          )}
        </div>
      </div>

      {/* Expanded content - show individual tool executions */}
      {isExpanded && (
        <div className={cn("border-t border-muted/30 p-1", density === "card" ? "space-y-2" : "space-y-1")}>
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
              density={density}
            />
          ))}
        </div>
      )}
    </div>
  );
}

export const ToolExecutionCollapsibleGroup = memo(ToolExecutionCollapsibleGroupComponent);