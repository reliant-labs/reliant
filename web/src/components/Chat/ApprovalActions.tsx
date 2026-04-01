/**
 * ApprovalActions - Renders custom action buttons for workflow approvals
 * 
 * When a workflow step has custom actions configured (e.g., "Deploy Now", "Cancel", "Request Changes"),
 * this component renders those custom buttons instead of the default Approve/Deny buttons.
 * 
 * If no custom actions are configured, it falls back to default Approve All / Deny All buttons.
 */

import { useMemo } from 'react';
import type { ToolApprovalRequest, ApprovalActionConfig } from '../../api/client';

interface ApprovalActionsProps {
  pendingApprovals: ToolApprovalRequest[];
  onApprove: (actionTaken?: string) => void;
  onDeny: (actionTaken?: string) => void;
  shortcutKey: string; // "⌘" on Mac, "Ctrl" on Windows
}

export function ApprovalActions({
  pendingApprovals,
  onApprove,
  onDeny,
  shortcutKey,
}: ApprovalActionsProps) {
  // Aggregate unique actions from all pending approvals
  // For workflow_step approvals with custom actions, use those
  // For tool approvals or approvals without custom actions, use defaults
  const actions = useMemo(() => {
    const customActions: ApprovalActionConfig[] = [];
    let hasWorkflowStepWithActions = false;

    for (const approval of pendingApprovals) {
      if (approval.actions && approval.actions.length > 0) {
        hasWorkflowStepWithActions = true;
        // Add unique actions (by label)
        for (const action of approval.actions) {
          if (!customActions.some(a => a.label === action.label && a.type === action.type)) {
            customActions.push(action);
          }
        }
      }
    }

    // If any approval has custom actions, use those
    if (hasWorkflowStepWithActions && customActions.length > 0) {
      return customActions;
    }

    // Otherwise, return default actions
    return [
      { type: 'approve', label: 'Approve All' },
      { type: 'deny', label: 'Deny All' },
    ] as ApprovalActionConfig[];
  }, [pendingApprovals]);

  // Separate approve-type and deny-type actions
  const approveActions = actions.filter(a => a.type === 'approve' || a.type === 'modify');
  const denyActions = actions.filter(a => a.type === 'deny');

  // If we only have the defaults, show simple layout
  const isDefault = actions.length === 2 && 
    actions[0].label === 'Approve All' && 
    actions[1].label === 'Deny All';

  return (
    <div className="flex items-center gap-2">
      {/* Approve-type buttons */}
      {approveActions.map((action, index) => (
        <button
          key={`approve-${index}`}
          onClick={() => onApprove(action.label)}
          className="flex items-center gap-2 px-3 py-1.5 text-sm bg-success hover:bg-success/90 text-success-foreground rounded transition-colors"
          title={action.label}
        >
          {action.label}
          {/* Show keyboard shortcut only for the first/primary approve action */}
          {index === 0 && isDefault && (
            <span className="px-1.5 py-0.5 rounded text-xs font-mono" style={{
              backgroundColor: 'hsl(var(--success-foreground) / 0.2)',
              color: 'hsl(var(--success-foreground))'
            }}>
              {shortcutKey}+↵
            </span>
          )}
        </button>
      ))}
      
      {/* Deny-type buttons */}
      {denyActions.map((action, index) => (
        <button
          key={`deny-${index}`}
          onClick={() => onDeny(action.label)}
          className="px-3 py-1.5 text-sm bg-destructive hover:bg-destructive/90 text-destructive-foreground rounded transition-colors"
          title={action.label}
        >
          {action.label}
        </button>
      ))}
    </div>
  );
}
