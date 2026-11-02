import { useCallback, useMemo, useState } from 'react';
import { Shield } from 'lucide-react';
import { ApprovalActions } from './ApprovalActions';
import { ApprovalBatchConfirm } from './ApprovalBatchConfirm';
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
import { parseApprovalCard, planApprovalBatch, type ApprovalBatchPlan } from '../../lib/approvalCard';
import { cn } from '../../lib/utils';
import { useSurface } from '../../lib/surfaceContext';

interface PermissionsPanelProps {
  chatId?: string; // Allow passing chatId for command center mode
}

/**
 * The open chat's pending approvals, above the composer. Every approval is
 * listed with its own Approve / Deny. A run that asks for several at once
 * (one turn's activation changes, say) can be decided with "Approve all N":
 * when the batch includes an activation card, which grants an automation
 * standing power, Approve all first opens a confirmation that shows every
 * card's grant summary (ApprovalBatchConfirm). An activation approval in the
 * old plain-text form has no such summary, so it stays out of the batch and
 * says why (planApprovalBatch).
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
  const plan = useMemo(() => planApprovalBatch(pendingApprovals), [pendingApprovals]);
  const apartReason = useMemo(
    () => new Map(plan.apart.map(({ approval, reason }) => [approval.id, reason])),
    [plan],
  );
  const [confirming, setConfirming] = useState<ApprovalBatchPlan<Approval> | null>(null);
  const approveOne = useApproveToolRequest(chatId ?? undefined);
  const denyOne = useDenyToolRequest(chatId ?? undefined);
  const batchApproveMutation = useBatchApproveInChat(chatId ?? undefined);
  const batchDenyMutation = useBatchDenyInChat(chatId ?? undefined);

  // Detect platform for keyboard shortcut hint
  const isMac = typeof window !== 'undefined' &&
    (window.navigator.platform.toUpperCase().includes('MAC') ||
     window.navigator.userAgent.toUpperCase().includes('MAC'));
  const shortcutKey = isMac ? '⌘' : 'Ctrl';

  const approveTogether = useCallback((shown: ApprovalBatchPlan<Approval>) => {
    if (chatId && shown.together.length > 0) {
      batchApproveMutation.mutate({ requestIds: shown.together.map((a) => a.id) });
    }
  }, [chatId, batchApproveMutation]);

  // A batch that grants standing power is approved from its confirmation,
  // which shows each card's grant summary; a routine one at once.
  const handleApproveAll = useCallback(() => {
    if (plan.grants.length > 0) setConfirming(plan);
    else approveTogether(plan);
  }, [plan, approveTogether]);

  const handleDenyAll = useCallback(() => {
    if (chatId && plan.together.length > 0) {
      batchDenyMutation.mutate({ requestIds: plan.together.map((a) => a.id) });
    }
  }, [chatId, plan, batchDenyMutation]);

  // Only show permissions panel if there are pending approvals
  // The backend determines whether approvals are created based on workflow params
  if (!chatId || pendingApprovals.length === 0) {
    return null;
  }

  // The ⌘↵ shortcut does what Approve all does (through the same
  // confirmation): show it on the batch button when there is one, else on
  // the only approval it would approve.
  const batch = plan.together.length > 1;
  const count = plan.together.length;

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
            apartReason={apartReason.get(approval.id)}
            shortcutKey={!batch && plan.together[0]?.id === approval.id ? shortcutKey : undefined}
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
              {count} requests
              {plan.apart.length > 0 && (
                <span className="font-normal text-muted-foreground">
                  {` · ${plan.apart.length} decided on ${plan.apart.length === 1 ? "its" : "their"} own`}
                </span>
              )}
            </span>
          </div>

          <ApprovalActions
            onApprove={handleApproveAll}
            onDeny={handleDenyAll}
            shortcutKey={shortcutKey}
            approveLabel={`Approve all ${count}`}
            denyLabel={`Deny all ${count}`}
          />
        </div>
      )}
      <ApprovalBatchConfirm
        plan={confirming}
        onCancel={() => setConfirming(null)}
        onConfirm={(shown) => {
          setConfirming(null);
          approveTogether(shown);
        }}
      />
    </div>
  );
}

function ApprovalItem({
  approval,
  apartReason,
  shortcutKey,
  onApprove,
  onDeny,
}: {
  approval: Approval;
  /** Why this approval is not in Approve all, when it is not. */
  apartReason?: string;
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
      {apartReason && <p className="text-xs text-warning-ink">{apartReason}</p>}
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
