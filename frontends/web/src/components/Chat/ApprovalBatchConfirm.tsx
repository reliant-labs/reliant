// Copyright (c) 2025 Reliant Labs

/**
 * The confirmation an "Approve all" opens when the batch grants an automation
 * standing power: every activation card's grant summary (its lead sentence and
 * labelled rows, the plain-words account activation_card.go writes), then the
 * routine requests it also approves, then anything left out and why.
 *
 * Owner decision on reliant #1988 (2026-11-02): activation cards may be
 * approved together, but only after this shows what each one grants. Confirm
 * approves exactly the approvals shown here, never whatever is pending by the
 * time it is pressed. The chat's panel, the ⌘↵ shortcut and the Inbox share it.
 */

import Modal from "../forge-ui/modal";
import Button from "@/components/forge-ui/button";
import { ApprovalCardView } from "./ApprovalCardView";
import { parseApprovalCard, type ApprovalBatchPlan } from "@/lib/approvalCard";

export interface BatchApproval {
  id: string;
  title: string;
}

interface ApprovalBatchConfirmProps<T extends BatchApproval> {
  /** The batch to confirm; null when closed. */
  plan: ApprovalBatchPlan<T> | null;
  onConfirm: (plan: ApprovalBatchPlan<T>) => void;
  onCancel: () => void;
}

export function ApprovalBatchConfirm<T extends BatchApproval>({ plan, onConfirm, onCancel }: ApprovalBatchConfirmProps<T>) {
  if (!plan) return null;
  const count = plan.together.length;
  const grantIds = new Set(plan.grants.map((a) => a.id));
  const routine = plan.together.filter((a) => !grantIds.has(a.id));
  const grants = plan.grants.length;
  return (
    <Modal
      open
      onClose={onCancel}
      size="lg"
      title={`Approve all ${count}?`}
      description={`${grants === 1 ? "One of them turns on or changes an automation" : `${grants} of them turn on or change automations`}. Read what each grants: it takes effect as soon as you approve.`}
      footer={
        <>
          <Button variant="secondary" onClick={onCancel}>
            Cancel
          </Button>
          <Button onClick={() => onConfirm(plan)}>{`Approve ${count}`}</Button>
        </>
      }
    >
      <div className="space-y-4" data-testid="approval-batch-confirm">
        <section aria-label="What the automations will be able to do" className="space-y-2">
          <ul className="space-y-2">
            {plan.grants.map((approval) => (
              <li
                key={approval.id}
                className="rounded-lg border border-border p-3"
                data-testid={`batch-grant-${approval.id}`}
              >
                <ApprovalCardView title={approval.title} />
              </li>
            ))}
          </ul>
        </section>
        {routine.length > 0 && (
          <section aria-label="Routine requests" className="space-y-1.5">
            <h4 className="text-xs font-medium text-muted-foreground">
              {routine.length === 1 ? "And 1 routine request" : `And ${routine.length} routine requests`}
            </h4>
            <ul className="space-y-1 text-sm text-foreground">
              {routine.map((approval) => (
                <li key={approval.id} className="break-words" data-testid={`batch-routine-${approval.id}`}>
                  {parseApprovalCard(approval.title).lead || "A request with no description"}
                </li>
              ))}
            </ul>
          </section>
        )}
        {plan.apart.length > 0 && (
          <section aria-label="Not included" className="space-y-1.5">
            <h4 className="text-xs font-medium text-muted-foreground">Not included: decide these on their own</h4>
            <ul className="space-y-2 text-sm">
              {plan.apart.map(({ approval, reason }) => (
                <li key={approval.id} className="space-y-0.5" data-testid={`batch-apart-${approval.id}`}>
                  <p className="break-words text-foreground">{parseApprovalCard(approval.title).lead}</p>
                  <p className="text-xs text-warning-ink">{reason}</p>
                </li>
              ))}
            </ul>
          </section>
        )}
      </div>
    </Modal>
  );
}
