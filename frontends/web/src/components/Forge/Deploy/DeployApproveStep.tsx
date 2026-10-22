// Copyright (c) 2025 Reliant Labs

/**
 * THE APPROVAL. The one control on this surface that causes a deploy.
 *
 * WHAT CHANGED, AND WHY IT IS THE WHOLE POINT. This step used to confirm
 * against the instant preview, whose token named the target cluster and the
 * bound release. That approved WHERE bytes land and said nothing about WHAT
 * ships — and a deploy builds from the chosen branch and cuts a new release, so
 * "what ships" did not exist yet when the operator clicked. The click approved
 * whatever got computed next.
 *
 * Now the approval names the PLAN: its digest travels with the deploy, and
 * forge recomputes the plan and refuses if it is not that one. So the thing
 * approved and the thing deployed are the same thing, checked by something
 * other than hope.
 *
 * THE GUARD IS STRUCTURAL. This component cannot render without a plan
 * document, and cannot produce a deploy without a digest and a release derived
 * FROM that document. There is no prop by which a caller could supply an env, a
 * release, a digest or a token of its own choosing — the only input is what was
 * rendered. That is what makes "the operator saw what they approved" a property
 * of the type rather than of a code path someone remembered to write.
 *
 * ABSENT, NOT DISABLED. Until every irreversible change is accepted, there is
 * no button. A disabled button invites hunting for the way to enable it; an
 * absent one makes the acknowledgement the thing to do.
 *
 * There is no field, toggle or "force" affordance for skipping forge's checks,
 * and there must never be.
 */

import { Button } from "@/components/ui/Button";
import {
  approvableDigest,
  approvableRelease,
  approvalFor,
  planOf,
  requiredAcknowledgements,
  unacknowledgeableStopFindings,
  type DeployPlanReport,
  type PlanApproval,
} from "@/services/forge/deployPlan";

export interface DeployApproveStepProps {
  /** The plan-only document. The sole source of the approval. */
  report: DeployPlanReport;
  /** The stop-class codes accepted so far. */
  acknowledged: ReadonlySet<string>;
  /** Called with the approval derived from the rendered plan. */
  onApprove: (approval: PlanApproval) => void;
  onCancel: () => void;
  isDeploying?: boolean;
  /** The environment's name, for the button. */
  env?: string;
}

export function DeployApproveStep({
  report,
  acknowledged,
  onApprove,
  onCancel,
  isDeploying,
  env,
}: DeployApproveStepProps) {
  const plan = planOf(report);
  const digest = approvableDigest(report);
  const release = approvableRelease(report);
  const environmentName = (env ?? report.env ?? "").trim() || "this environment";

  // NO PLAN, NO DIGEST, OR NO RELEASE: there is nothing to bind an approval to.
  // Each of these means a deploy from here would approve something other than
  // what was read, so none of them offers a button.
  if (!plan || !digest || !release) {
    return (
      <p
        data-testid="deploy-not-approvable"
        className="rounded-lg border border-dashed border-destructive/50 px-3 py-2 text-xs text-destructive-ink"
      >
        This cannot be deployed from what is on screen. Work out the changes again, and if
        that keeps happening the environment may never have been built.
      </p>
    );
  }

  // A nameless irreversible change cannot be accepted, so the plan cannot be
  // approved at all. The plan view explains it; this just withholds the button.
  if (unacknowledgeableStopFindings(plan).length > 0) {
    return (
      <div className="flex items-center justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onCancel} disabled={isDeploying}>
          Cancel
        </Button>
      </div>
    );
  }

  const outstanding = requiredAcknowledgements(plan).filter((code) => !acknowledged.has(code));
  // The approval is DERIVED FROM THE RENDERED DOCUMENT by its single producer.
  // Null means not approvable, and null makes the button absent.
  const approval = approvalFor(report, acknowledged);

  return (
    <section className="space-y-3" data-testid="deploy-approve">
      {/* Why there is no button yet, in the reader's terms — not a count of
          unticked checkboxes, which they can already see. */}
      {outstanding.length > 0 && (
        <p
          data-testid="deploy-approve-outstanding"
          className="rounded-lg border border-dashed border-border px-3 py-2 text-xs text-muted-foreground"
        >
          Accept the irreversible changes above to continue.
        </p>
      )}

      <div className="flex items-center justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onCancel} disabled={isDeploying}>
          Cancel
        </Button>

        {/* ABSENT until approvable. */}
        {approval && (
          <Button
            variant="destructive"
            size="sm"
            onClick={() => onApprove(approval)}
            loading={isDeploying}
            disabled={isDeploying}
            data-testid="deploy-approve-start"
          >
            {/* The button names the DECISION — deploy these changes to this
                environment — and not the cluster, which the operator did not
                choose and cannot change from here. */}
            {isDeploying ? "Starting deploy…" : `Deploy this plan to ${environmentName}`}
          </Button>
        )}
      </div>
    </section>
  );
}
