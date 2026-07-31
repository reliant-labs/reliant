import { useCallback } from 'react';
import { Shield } from 'lucide-react';
import { ApprovalActions } from './ApprovalActions';
import { useActiveChatId } from '../../store/chatStoreHooks';
import { usePendingApprovals, useBatchApprove, useBatchDeny } from '../../hooks/approval-queries';

interface PermissionsPanelProps {
  chatId?: string; // Allow passing chatId for command center mode
}

export function PermissionsPanel({ chatId: propsChatId }: PermissionsPanelProps = {}) {
  const activeChatId = useActiveChatId();
  const chatId = propsChatId || activeChatId;

  const pendingApprovalsQuery = usePendingApprovals(chatId ?? undefined);
  const pendingApprovals = pendingApprovalsQuery.data ?? [];

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

  // Always show when there are pending approvals - now inline above chat input
  return (
    <div className="inline-flex items-center gap-3 px-4 py-3 mb-3 bg-primary/5 dark:bg-primary/10 border border-primary/30 rounded-lg elevation-3" data-testid="permissions-popup">
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