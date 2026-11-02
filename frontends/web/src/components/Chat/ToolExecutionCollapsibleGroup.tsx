/**
 * Collapsible group for read-only tools (view, grep, websearch, etc.)
 * Similar to Cursor's "Explored X files Y searches" grouping
 */

import { memo, useId } from 'react';
import { AlertCircle, ChevronDown, ChevronRight, Eye } from 'lucide-react';
import { cn } from '../../lib/utils';
import { ToolExecution, type ToolResultData } from './ToolExecution';
import type { Approval } from '../../gen/reliant/v1/approval_pb';
import { generateReadOnlyToolsSummary } from '../../lib/toolFormatters';
import { useSurface } from '../../lib/surfaceContext';
import { ApprovalStatus } from '../../gen/reliant/v1/approval_pb';
import { useToolGroupExpansion } from './useToolGroupExpansion';

// Mirrors the subset of lib/messageProcessor's ToolCallData this group reads.
// `input` is OPTIONAL there — undefined while the call is still streaming and
// its arguments have not arrived — and this local copy has to agree, or every
// caller passing a real ToolCallData fails to type-check.
type ToolCallData = {
  id: string;
  name: string;
  input?: Record<string, unknown> | string;
  finished?: boolean;
};

interface ToolExecutionCollapsibleGroupProps {
  executions: Array<{
    call: ToolCallData;
    result?: ToolResultData;
    approval?: Approval;
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
  const surface = useSurface();

  // Generate summary text like "Explored 2 files 1 search"
  const summaryText = generateReadOnlyToolsSummary(
    executions.map(({ call }) => ({
      name: call.name,
      // A still-streaming call has no input yet; the summary formatter takes
      // a required ToolInput, and an empty object reads as "no arguments"
      // rather than crashing the summary line.
      input: call.input ?? {},
    }))
  );

  const hasWarnings = executions.some(
    ({ status, result }) => status === 'failed' || result?.is_error
  );

  // Check if all tools are completed or running in background
  const allCompleted = executions.every(
    ({ status, result }) => status === 'completed' || status === 'backgrounded' || result !== undefined
  );

  // Open while a read is still running, closed once all of them are done.
  const { expanded: isExpanded, toggle } = useToolGroupExpansion({
    live: !allCompleted,
    failed: hasWarnings,
    pendingApproval: executions.some(({ approval }) => approval?.status === ApprovalStatus.PENDING),
    collapseAll: surface !== 'desktop',
  });
  const panelId = useId();

  return (
    <div
      className={cn(
        'border overflow-hidden',
        density === 'card' ? 'rounded-xl shadow-sm' : 'rounded-lg',
        density === 'minimal' && 'rounded-md shadow-none',
        hasWarnings
          ? 'border-destructive/40 bg-destructive/5'
          : allCompleted
          ? 'border-muted/30 bg-muted/5'
          : 'border-muted/50 bg-muted/10'
      )}
    >
      {/* Collapsible header */}
      <button
        type="button"
        className={cn(
          "flex w-full items-center justify-between text-left cursor-pointer hover:bg-muted/30 transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring touch:min-h-[44px]",
          density === "card" ? "px-3 py-2" : "px-1.5 py-1"
        )}
        onClick={toggle}
        aria-expanded={isExpanded}
        aria-controls={panelId}
      >
        <span className="flex min-w-0 items-center gap-1.5">
          {hasWarnings ? (
            <AlertCircle className="w-3.5 h-3.5 shrink-0 text-destructive" aria-hidden="true" />
          ) : (
            <Eye className="w-3.5 h-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
          )}
          <span className={cn("truncate text-xs font-mono", hasWarnings ? "text-destructive-ink" : "text-muted-foreground")}>
            {summaryText}
            {hasWarnings && <span className="sr-only"> (a call failed)</span>}
          </span>
        </span>

        <span className="flex shrink-0 items-center gap-1">
          {isExpanded ? (
            <ChevronDown className="w-3.5 h-3.5 text-muted-foreground" aria-hidden="true" />
          ) : (
            <ChevronRight className="w-3.5 h-3.5 text-muted-foreground" aria-hidden="true" />
          )}
        </span>
      </button>

      {/* Expanded content - show individual tool executions */}
      {isExpanded && (
        <div id={panelId} className={cn("border-t border-muted/30 p-1", density === "card" ? "space-y-2" : "space-y-1")}>
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