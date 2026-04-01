import { useChatStore } from '../../store/chatStore';
import { useCallback } from 'react';
import { Shield } from 'lucide-react';
import { ApprovalActions } from './ApprovalActions';
import type { ToolApprovalRequest } from '../../api/client';

// Stable empty array to prevent re-renders
const EMPTY_ARRAY: ToolApprovalRequest[] = [];

interface PermissionsPanelProps {
  chatId?: string; // Allow passing chatId for command center mode
}

export function PermissionsPanel({ chatId: propsChatId }: PermissionsPanelProps = {}) {
  const activeChatId = useChatStore((state) => state.activeChatId);
  const approveAllPending = useChatStore((state) => state.approveAllPending);
  const denyAllPending = useChatStore((state) => state.denyAllPending);

  // Use props chatId if provided (command center mode), otherwise use activeChatId
  const chatId = propsChatId || activeChatId;
  
  // Detect platform for keyboard shortcut hint
  const isMac = typeof window !== 'undefined' && 
    (window.navigator.platform.toUpperCase().includes('MAC') || 
     window.navigator.userAgent.toUpperCase().includes('MAC'));
  const shortcutKey = isMac ? '⌘' : 'Ctrl';

  // Use memoized selector to ensure Zustand properly tracks state changes
  const pendingApprovalsSelector = useCallback(
    (state: ReturnType<typeof useChatStore.getState>) =>
      (chatId ? state.pendingApprovals[chatId] : undefined) || EMPTY_ARRAY,
    [chatId]
  );
  const pendingApprovals = useChatStore(pendingApprovalsSelector);

  const handleApprove = useCallback((actionTaken?: string) => {
    if (chatId) {
      approveAllPending(chatId, actionTaken);
    }
  }, [chatId, approveAllPending]);

  const handleDeny = useCallback((actionTaken?: string) => {
    if (chatId) {
      // No reason required - backend will use default message
      denyAllPending(chatId, undefined, actionTaken);
    }
  }, [chatId, denyAllPending]);

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
        pendingApprovals={pendingApprovals}
        onApprove={handleApprove}
        onDeny={handleDeny}
        shortcutKey={shortcutKey}
      />
    </div>
  );
}