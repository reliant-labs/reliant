import { useCallback, useMemo } from 'react';
import { Shield } from 'lucide-react';
import { ApprovalActions } from './ApprovalActions';
import { ApprovalCardView } from './ApprovalCardView';
import { useActiveChatId } from '../../store/chatStoreHooks';
import {
  usePendingApprovals,
  useApproveToolRequest,
  useDenyToolRequest,
  useBatchApproveInChat,
  useBatchDenyInChat,
  type Approval,
} from '../../hooks/approval-queries';
import { parseApprovalCard } from '../../lib/approvalCard';
import { cn } from '../../lib/utils';
import { useSurface } from '../../lib/surfaceContext';

interface PermissionsPanelProps {
  chatId?: string; // Allow passing chatId for command center mode
}

/**
 * The open chat's pending approvals, above the composer. Every approval is
 * listed with its own Approve / Deny. An approval that grants an automation
 * standing power (an activation card) is decided on its own card, never in
 * "Approve all": "Approve all" and the ⌘↵ shortcut cover routine approvals
 * only, and appear only when there are two or more of them.
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
  const routine = useMemo(
    () => pendingApprovals.filter((a) => !parseApprovalCard(a.title).grantsPower),
    [pendingApprovals],
  );
  const approveOne = useApproveToolRequest(chatId ?? undefined);
  const denyOne = useDenyToolRequest(chatId ?? undefined);
  const batchApproveMutation = useBatchApproveInChat(chatId ?? undefined);
  const batchDenyMutation = useBatchDenyInChat(chatId ?? undefined);

  // Detect platform for keyboard shortcut hint
  const isMac = typeof window !== 'undefined' &&
    (window.navigator.platform.toUpperCase().includes('MAC') ||
     window.navigator.userAgent.toUpperCase().includes('MAC'));
  const shortcutKey = isMac ? '⌘' : 'Ctrl';

  const handleApproveRoutine = useCallback(() => {
    if (chatId && routine.length > 0) {
      batchApproveMutation.mutate({ requestIds: routine.map((a) => a.id) });
    }
  }, [chatId, routine, batchApproveMutation]);

  const handleDenyRoutine = useCallback(() => {
    if (chatId && routine.length > 0) {
      batchDenyMutation.mutate({ requestIds: routine.map((a) => a.id) });
    }
  }, [chatId, routine, batchDenyMutation]);

  // Only show permissions panel if there are pending approvals
  // The backend determines whether approvals are created based on workflow params
  if (!chatId || pendingApprovals.length === 0) {
    return null;
  }

  // The ⌘↵ shortcut approves the routine approvals: show it on the button
  // that does the same, the batch one when there is one, else the only one.
  const batch = routine.length > 1;

  return (
    <div className="flex w-full flex-col items-end" data-testid="permissions-panel">
      <ul
        className="mb-2 max-h-96 w-full divide-y divide-border overflow-auto rounded-lg border border-border bg-background"
        aria-label="Pending approvals"
      >
        {pendingApprovals.map((approval) => (
          <ApprovalItem
            key={approval.id}
            approval={approval}
            shortcutKey={!batch && routine[0]?.id === approval.id ? shortcutKey : undefined}
            onApprove={() => approveOne.mutate({ requestId: approval.id })}
            onDeny={() => denyOne.mutate({ requestId: approval.id })}
          />
        ))}
      </ul>
      {batch && (
        <div
          className={cn(
            "flex items-center gap-3 px-4 py-3 mb-3 bg-primary/5 dark:bg-primary/10 border border-primary/30 rounded-lg elevation-3",
            surface === "desktop" ? "inline-flex" : "w-full flex-wrap"
          )}
          data-testid="permissions-popup"
        >
          <div className="flex items-center gap-2">
            <Shield className="w-5 h-5 text-primary-ink" aria-hidden="true" />
            <span className="font-medium text-primary-ink">
              {routine.length} routine requests
            </span>
          </div>

          <ApprovalActions
            onApprove={handleApproveRoutine}
            onDeny={handleDenyRoutine}
            shortcutKey={shortcutKey}
            approveLabel={`Approve all ${routine.length}`}
            denyLabel={`Deny all ${routine.length}`}
          />
        </div>
      )}
    </div>
  );
}

function ApprovalItem({
  approval,
  shortcutKey,
  onApprove,
  onDeny,
}: {
  approval: Approval;
  shortcutKey?: string;
  onApprove: () => void;
  onDeny: () => void;
}) {
  const surface = useSurface();
  const card = parseApprovalCard(approval.title);
  const subject = card.lead || "this request";
  return (
    <li className="space-y-2.5 p-3" data-testid={`approval-${approval.id}`}>
      {card.grantsPower && (
        <p className="text-xs font-medium text-warning-ink">Grants an automation standing power: read it before you approve.</p>
      )}
      <ApprovalCardView title={approval.title} />
      <div className={cn("flex", surface === "desktop" ? "justify-end" : "w-full")}>
        <ApprovalActions
          compact={surface === "desktop"}
          onApprove={onApprove}
          onDeny={onDeny}
          shortcutKey={shortcutKey}
          approveName={`Approve: ${subject}`}
          denyName={`Deny: ${subject}`}
        />
      </div>
    </li>
  );
}
