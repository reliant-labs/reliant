import { useCallback, useMemo } from 'react';
import { Shield } from 'lucide-react';
import { ApprovalActions } from './ApprovalActions';
import { useActiveChatId } from '../../store/chatStoreHooks';
import { usePendingApprovals, useBatchApprove, useBatchDeny } from '../../hooks/approval-queries';
import { cn } from '../../lib/utils';
import { useSurface } from '../../lib/surfaceContext';

interface PermissionsPanelProps {
  chatId?: string; // Allow passing chatId for command center mode
}

export function PermissionsPanel({ chatId: propsChatId }: PermissionsPanelProps = {}) {
  const activeChatId = useActiveChatId();
  const chatId = propsChatId || activeChatId;
  const surface = useSurface();

  const pendingApprovalsQuery = usePendingApprovals(chatId ?? undefined);
  // The `?? []` fallback would otherwise mint a new array on every render while
  // the query is still loading, re-creating both approve/deny callbacks.
  const pendingApprovals = useMemo(
    () => pendingApprovalsQuery.data ?? [],
    [pendingApprovalsQuery.data],
  );

  const batchApproveMutation = useBatchApprove();
  const batchDenyMutation = useBatchDeny();
  
  // Detect platform for keyboard shortcut hint
  const isMac = typeof window !== 'undefined' && 
    (window.navigator.platform.toUpperCase().includes('MAC') || 
     window.navigator.userAgent.toUpperCase().includes('MAC'));
  const shortcutKey = isMac ? '⌘' : 'Ctrl';

  const handleApprove = useCallback(() => {
    if (chatId && pendingApprovals.length > 0) {
      batchApproveMutation.mutate({
        chatId,
        requestIds: pendingApprovals.map((a) => a.id),
      });
    }
  }, [chatId, pendingApprovals, batchApproveMutation]);

  const handleDeny = useCallback(() => {
    if (chatId && pendingApprovals.length > 0) {
      batchDenyMutation.mutate({
        chatId,
        requestIds: pendingApprovals.map((a) => a.id),
      });
    }
  }, [chatId, pendingApprovals, batchDenyMutation]);

  // Only show permissions panel if there are pending approvals
  // The backend determines whether approvals are created based on workflow params
  if (pendingApprovals.length === 0) {
    return null;
  }

  // Always show when there are pending approvals - now inline above chat input.
  // `inline-flex` sizes to content on desktop; on mobile it forces the "N
  // Pending" label and both buttons onto one row that doesn't fit a 390px
  // viewport, so narrow surfaces get a full-width, wrapping layout instead.
  return (
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
      />
    </div>
  );
}