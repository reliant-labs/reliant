/**
 * One quiet row for a run's read-only calls (view, grep, glob, websearch, …):
 * "Explored 5 files 3 searches", opening onto each call's own row. Like
 * Cursor's "Explored X files Y searches" grouping.
 */

import { memo, useEffect, useMemo } from 'react';
import { Eye, Loader2, XCircle } from 'lucide-react';
import { ToolExecution, type ToolResultData } from './ToolExecution';
import type { Approval } from '../../gen/reliant/v1/approval_pb';
import { generateReadOnlyToolsSummary } from '../../lib/toolFormatters';
import { useSurface } from '../../lib/surfaceContext';
import { ApprovalStatus } from '../../gen/reliant/v1/approval_pb';
import { useToolGroupExpansion } from './useToolGroupExpansion';
import { QuietRow } from './QuietRow';
import { elidedBlockIdsOf, prefetchContentBlockBodies } from '../../hooks/content-block-bodies';

// Mirrors the subset of lib/messageProcessor's ToolCallData this group reads.
// `input` is OPTIONAL there — undefined while the call is still streaming and
// its arguments have not arrived — and this local copy has to agree, or every
// caller passing a real ToolCallData fails to type-check.
type ToolCallData = {
  id: string;
  name: string;
  input?: Record<string, unknown> | string;
  finished?: boolean;
  content_block_id?: string;
  /** `input` is a skeleton of an elided input (see messageProcessor's ToolCallData). */
  inputElided?: boolean;
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
}

function ToolExecutionCollapsibleGroupComponent({
  executions,
  messageId,
  chatId,
  showRichContent = false,
  onSelectThread,
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

  const failed = executions.filter(
    ({ status, result }) => status === 'failed' || result?.is_error
  ).length;

  // Check if all tools are completed or running in background
  const allCompleted = executions.every(
    ({ status, result }) => status === 'completed' || status === 'backgrounded' || result !== undefined
  );

  // Open while a read is still running, closed once all of them are done.
  const { expanded, toggle } = useToolGroupExpansion({
    live: !allCompleted,
    failed: failed > 0,
    pendingApproval: executions.some(({ approval }) => approval?.status === ApprovalStatus.PENDING),
    collapseAll: surface !== 'desktop',
  });

  // An open group reads the bodies its rows were sent without in one batch
  // (see TurnWorkFold).
  const elidedKey = useMemo(() => elidedBlockIdsOf(executions).join(','), [executions]);
  useEffect(() => {
    if (!expanded || !chatId || elidedKey === '') return;
    prefetchContentBlockBodies(chatId, elidedKey.split(','));
  }, [expanded, chatId, elidedKey]);

  return (
    <QuietRow
      as="button"
      data-testid="read-only-tool-group"
      icon={
        !allCompleted ? (
          <Loader2 className="size-3.5 animate-spin text-primary-ink" />
        ) : failed > 0 ? (
          <XCircle className="size-4 text-destructive-ink" />
        ) : (
          <Eye className="size-4" />
        )
      }
      expanded={expanded}
      onToggle={toggle}
      ariaLabel={failed > 0 ? `${summaryText}, ${failed} failed` : summaryText}
      trailing={
        failed > 0 ? <span className="text-meta tabular-nums text-destructive-ink">{failed} failed</span> : undefined
      }
      detail={
        <div className="flex flex-col gap-0.5">
          {executions.map((execution) => (
            <ToolExecution
              key={`${messageId || 'msg'}-readonly-${execution.call.id}`}
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
      }
    >
      <span className="truncate">{summaryText}</span>
    </QuietRow>
  );
}

export const ToolExecutionCollapsibleGroup = memo(ToolExecutionCollapsibleGroupComponent);
