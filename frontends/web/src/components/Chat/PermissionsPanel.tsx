import { useCallback, useMemo } from 'react';
import { Shield } from 'lucide-react';
import { ApprovalActions } from './ApprovalActions';
import { useActiveChatId } from '../../store/chatStoreHooks';
import {
  usePendingApprovals,
  useBatchApproveInChat,
  useBatchDenyInChat,
  useApproveToolRequest,
  useDenyToolRequest,
} from '../../hooks/approval-queries';
import type { Approval } from '../../gen/reliant/v1/approval_pb';
import { cn } from '../../lib/utils';
import { useSurface } from '../../lib/surfaceContext';

interface PermissionsPanelProps {
  chatId?: string; // Allow passing chatId for command center mode
}

/**
 * The chat's approval card: every approval the open chat is waiting on, above
 * the composer. A run that asks for several at once (one turn's activation
 * changes, say) raises them together, so the bar decides all of them in one
 * click; each one also keeps its own Approve/Deny, for reviewing them one by
 * one.
 */
export function PermissionsPanel({ chatId: propsChatId }: PermissionsPanelProps = {}) {
  const activeChatId = useActiveChatId();
  const chatId = propsChatId || activeChatId;
  const surface = useSurface();

  const pendingApprovalsQuery = usePendingApprovals(chatId ?? undefined);
  // The `?? []` fallback would otherwise mint a new array on every render while
  // the query is still loading, re-creating both approve/deny callbacks.
  const pendingApprovals = useMemo(
    () => pendingApprovalsQuery.data?.approvals ?? [],
    [pendingApprovalsQuery.data],
  );
  const batchApproveMutation = useBatchApproveInChat(chatId ?? undefined);
  const batchDenyMutation = useBatchDenyInChat(chatId ?? undefined);
  const approveOne = useApproveToolRequest(chatId ?? undefined);
  const denyOne = useDenyToolRequest(chatId ?? undefined);
  
  // Detect platform for keyboard shortcut hint
  const isMac = typeof window !== 'undefined' && 
    (window.navigator.platform.toUpperCase().includes('MAC') || 
     window.navigator.userAgent.toUpperCase().includes('MAC'));
  const shortcutKey = isMac ? '⌘' : 'Ctrl';

  const handleApprove = useCallback(() => {
    if (chatId && pendingApprovals.length > 0) {
      batchApproveMutation.mutate({
        requestIds: pendingApprovals.map((a) => a.id),
      });
    }
  }, [chatId, pendingApprovals, batchApproveMutation]);

  const handleDeny = useCallback(() => {
    if (chatId && pendingApprovals.length > 0) {
      batchDenyMutation.mutate({
        requestIds: pendingApprovals.map((a) => a.id),
      });
    }
  }, [chatId, pendingApprovals, batchDenyMutation]);

  // Only show permissions panel if there are pending approvals
  // The backend determines whether approvals are created based on workflow params
  if (!chatId || pendingApprovals.length === 0) {
    return null;
  }

  const several = pendingApprovals.length > 1;

  // Always show when there are pending approvals - now inline above chat input.
  // `inline-flex` sizes to content on desktop; on mobile it forces the "N
  // Pending" label and both buttons onto one row that doesn't fit a 390px
  // viewport, so narrow surfaces get a full-width, wrapping layout instead.
  return (
    <div className="flex w-full flex-col items-end">
      <ul
        className="mb-2 max-h-80 w-full divide-y divide-border/60 overflow-auto rounded-lg border border-border bg-background px-3"
        aria-label="Pending approval details"
      >
        {pendingApprovals.map((approval) => (
          <PendingApprovalRow
            key={approval.id}
            approval={approval}
            // One approval is decided by the bar below; a row of its own
            // would only repeat it.
            onApprove={several ? () => approveOne.mutate({ requestId: approval.id }) : undefined}
            onDeny={several ? () => denyOne.mutate({ requestId: approval.id }) : undefined}
          />
        ))}
      </ul>
      <div
        className={cn(
          "flex items-center gap-3 px-4 py-3 mb-3 bg-primary/5 dark:bg-primary/10 border border-primary/30 rounded-lg elevation-3",
          surface === "desktop" ? "inline-flex" : "w-full flex-wrap"
        )}
        data-testid="permissions-popup"
      >
        <div className="flex items-center gap-2">
          <Shield className="w-5 h-5 text-primary" />
          <span className="font-medium text-primary">
            {pendingApprovals.length} Pending
          </span>
        </div>

        <ApprovalActions
          onApprove={handleApprove}
          onDeny={handleDeny}
          shortcutKey={shortcutKey}
          approveLabel={several ? `Approve all ${pendingApprovals.length}` : "Approve"}
          denyLabel={several ? "Deny all" : "Deny"}
        />
      </div>
    </div>
  );
}

/** One pending approval's title, with its own decision when there are several. */
function PendingApprovalRow({
  approval,
  onApprove,
  onDeny,
}: {
  approval: Approval;
  onApprove?: () => void;
  onDeny?: () => void;
}) {
  return (
    <li className="flex items-start gap-3 py-2" data-testid={`pending-approval-${approval.id}`}>
      <pre className="min-w-0 flex-1 whitespace-pre-wrap break-words text-xs text-foreground">{approval.title}</pre>
      {onApprove && onDeny && (
        <div className="shrink-0">
          <ApprovalActions compact approveLabel="Approve" denyLabel="Deny" onApprove={onApprove} onDeny={onDeny} />
        </div>
      )}
    </li>
  );
}
