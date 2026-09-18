// Copyright (c) 2025 Reliant Labs

/**
 * The promote flow: plan → confirm → applied, or refused.
 *
 * THE ORDER IS THE GUARD. Confirm is rendered only inside the branch where a
 * plan document exists, so there is no route to the write that skips the diff —
 * not a disabled button that could be re-enabled, but an absent one. Every other
 * outcome of the plan call (not a forge project, forge too old, unreachable,
 * malformed) renders the shared state components and offers no confirm at all.
 *
 * PURE PROPS, like TopologyView and PromotePlanView: the hooks live in the page
 * component next door, so this whole flow — including the refusal path, which is
 * the hardest state to reach against a live daemon — can be driven from fixtures.
 *
 * After a successful apply the flow switches to an APPLIED panel rather than
 * closing. Closing on success would be the one moment the user is most likely to
 * assume something shipped, and this is where they are told, in forge's own
 * words, that nothing did and what to run next.
 */

import { CheckCircle2 } from "lucide-react";

import { Button } from "@/components/ui/Button";
import type { ApplyPromoteResult } from "@/api/forge-grpc";
import type { ForgePromotePlan } from "@/services/forge/promote";
import type { ForgeOutcome } from "@/services/forge/topology";

import {
  ForgeMalformed,
  ForgeUnreachable,
  ForgeUnsupported,
  NotForgeProject,
} from "../ForgeStates";
import { PromoteConfirmStep } from "./PromoteConfirmStep";
import { PromotePlanView, ShipsNothingNotice } from "./PromotePlanView";
import { PromoteRefusalNotice } from "./PromoteRefusalNotice";

export interface PromoteFlowProps {
  /** The plan outcome. Confirm is reachable only from the `report` branch. */
  planOutcome: ForgeOutcome<ForgePromotePlan> | undefined;
  isPlanning: boolean;
  /** A transport/daemon failure — genuinely an error, unlike the outcomes above. */
  planError?: Error | null;
  /** The write's result, once attempted. */
  applyResult?: ApplyPromoteResult | null;
  /** A thrown failure from the write. Distinct from a refusal. */
  applyError?: Error | null;
  isApplying: boolean;
  onConfirm: (plan: ForgePromotePlan) => void;
  onReplan: () => void;
  onClose: () => void;
  projectName?: string;
}

export function PromoteFlow({
  planOutcome,
  isPlanning,
  planError,
  applyResult,
  applyError,
  isApplying,
  onConfirm,
  onReplan,
  onClose,
  projectName,
}: PromoteFlowProps) {
  // APPLIED. Terminal, and it must not imply a deploy happened.
  if (applyResult?.kind === "applied") {
    return <AppliedPanel outcome={applyResult.outcome} onClose={onClose} />;
  }

  if (isPlanning && !planOutcome) {
    return (
      <p
        data-testid="promote-planning"
        className="rounded-lg border border-dashed border-border px-6 py-10 text-center text-sm text-muted-foreground"
      >
        Computing the promote plan…
      </p>
    );
  }

  if (planError && !planOutcome) {
    return (
      <div
        data-testid="promote-plan-error"
        className="rounded-lg border border-destructive/40 bg-destructive/10 px-6 py-8 text-center text-sm text-destructive"
      >
        Could not reach your daemon to plan this promote: {planError.message}
      </div>
    );
  }

  if (!planOutcome) return null;

  // Each is a successful RPC with a different meaning, and none of them can
  // support a write — so no confirm step is rendered in any of these branches.
  if (planOutcome.kind === "not-forge-project") return <NotForgeProject projectName={projectName} />;
  if (planOutcome.kind === "unsupported") return <ForgeUnsupported meta={planOutcome.meta} />;
  if (planOutcome.kind === "unreachable") return <ForgeUnreachable meta={planOutcome.meta} />;
  if (planOutcome.kind === "malformed") return <ForgeMalformed meta={planOutcome.meta} />;

  const plan = planOutcome.report;
  const refused = applyResult?.kind === "refused" ? applyResult.refusal : null;

  return (
    <div className="space-y-4">
      {/* The diff, always above the confirm. */}
      <PromotePlanView plan={plan} />

      {refused ? (
        // Refused: nothing was written, and the plan above is the one that was
        // already rejected as stale. No confirm until a fresh plan replaces it.
        <PromoteRefusalNotice refusal={refused} onReplan={onReplan} isReplanning={isPlanning} />
      ) : (
        <>
          {applyError && (
            // A thrown failure, not a refusal. The outcome is genuinely unknown
            // here, so it does not claim either way — the offered move is to
            // re-plan and look.
            <div
              data-testid="promote-apply-error"
              className="space-y-2 rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive"
            >
              <p>The promote could not be completed: {applyError.message}</p>
              <p className="text-muted-foreground">
                Whether the binding was written is not known from this failure. Re-plan to see the
                environment&apos;s current state.
              </p>
              <Button variant="secondary" size="xs" onClick={onReplan} disabled={isPlanning}>
                Re-plan
              </Button>
            </div>
          )}
          <PromoteConfirmStep
            plan={plan}
            onConfirm={() => onConfirm(plan)}
            onCancel={onClose}
            isApplying={isApplying}
          />
        </>
      )}
    </div>
  );
}

/**
 * The binding was written. THE WORD "DEPLOYED" APPEARS NOWHERE.
 *
 * The heading says the binding was updated, and the ships-nothing notice —
 * rendered from the APPLIED document, which forge populates with
 * `ships_nothing` and `next_step` exactly as it does a preview — states that
 * nothing reached a cluster and names the command that would. A panel that said
 * "promoted successfully" and stopped would leave the user believing they had
 * shipped, which is worse than showing them nothing at all.
 */
function AppliedPanel({
  outcome,
  onClose,
}: {
  outcome: ForgeOutcome<ForgePromotePlan>;
  onClose: () => void;
}) {
  // A write that succeeded but whose report could not be read: the binding IS
  // written, so this must not read as a failure, but nothing can be shown about
  // what it contains.
  if (outcome.kind !== "report") {
    return (
      <div className="space-y-3" data-testid="promote-applied">
        <p className="text-sm text-foreground">
          The binding was written. Forge&apos;s report of it could not be read, so the details
          cannot be shown here.
        </p>
        <p className="text-xs text-muted-foreground">
          Promoting moves a pointer — nothing has been deployed.
        </p>
        <div className="flex justify-end">
          <Button variant="secondary" size="sm" onClick={onClose}>
            Done
          </Button>
        </div>
      </div>
    );
  }

  const plan = outcome.report;

  return (
    <div className="space-y-4" data-testid="promote-applied">
      <div className="flex items-center gap-2 text-success">
        <CheckCircle2 className="h-4 w-4 shrink-0" aria-hidden="true" />
        <h3 className="text-sm font-medium" data-testid="promote-applied-heading">
          Binding updated — {plan.env} now points at {plan.target?.release ?? plan.release}
        </h3>
      </div>

      {/* The same notice as the preview, from the applied document, hoisted
          above the diff because it is the most important element on this panel.
          The plan view is told not to repeat it. */}
      <ShipsNothingNotice plan={plan} />

      <PromotePlanView plan={plan} showShipsNothing={false} />

      <div className="flex justify-end">
        <Button variant="secondary" size="sm" onClick={onClose}>
          Done
        </Button>
      </div>
    </div>
  );
}
