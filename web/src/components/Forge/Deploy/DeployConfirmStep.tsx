// Copyright (c) 2025 Reliant Labs

/**
 * THE GUARDED CONFIRM for the only write on this surface that reaches live
 * infrastructure.
 *
 * ONE RULE, BOTH DESTINATIONS: the KCL declares the target, the plan is the
 * review, and the button naming the environment is the approval.
 *
 * THE GUARD IS STRUCTURAL, NOT A WARNING. This component cannot render without
 * a plan, and it cannot start a deploy without a confirmation token derived
 * from that plan — so "the user saw the plan" and "the token describes what
 * they saw" are both properties of the type rather than of a code path someone
 * remembered to write. There is deliberately NO prop by which a caller could
 * pass an env, a cluster, a release or a token of its own choosing: the only
 * input is the document that was rendered.
 *
 * WHY THERE IS NOTHING TO TYPE ANY MORE. This step used to require
 * transcribing the kube context, on the reasoning that a deploy to the wrong
 * cluster is unrecoverable. The premise was the mistake: you cannot deploy to
 * the wrong cluster. The target is DECLARED in the environment's KCL —
 * `forge.K8sCluster.cluster` IS the kubectl context — and forge deploys to that
 * declaration and never to the ambient `current-context`. There is no point in
 * the flow at which a user chooses a cluster, so there is no wrong choice for
 * friction to catch. What the typing actually did was put an instruction to
 * copy a string in the most prominent position on the dialog, which taught the
 * reader that the box rather than the plan was the thing to attend to. The
 * hosted half had the identical defect for a different reason (the string was
 * our hostname), and both are now the same single rule — which also means a
 * user cannot be surprised by which ceremony a given environment demands.
 *
 * THE TOKEN STILL BINDS, AND THAT WAS NEVER THE TYPING'S JOB. deployTokenFor
 * derives it from the rendered plan, the request carries it, and the server
 * re-checks both halves — so a deploy whose KCL now declares a different
 * cluster, or whose bound release moved, is still REFUSED after the click. The
 * transcription was a demonstration of that mechanism aimed at the wrong
 * audience; removing it changes nothing a server-side check depends on. The
 * refusal notice is where a stale target surfaces, and it names both contexts.
 *
 * THE ONE REMAINING CHECKBOX IS IRREVERSIBLE LOSS. A finding that says storage
 * will be deleted or an address reissued describes a deploy that SUCCEEDS and
 * is still unrecoverable, which no re-deploy undoes — so it gets an explicit
 * tick, shown only when such a finding is present. That is a claim about a
 * consequence the plan cannot otherwise force a reader to confront, which is
 * exactly what the removed checkbox was not. Forge emits none today; the full
 * treatment arrives with the plan-before-promote work (O-13). (A BLOCKING
 * finding needs no tick: it offers no confirm at all.)
 *
 * There is no field, toggle or "force" affordance for --skip-preflight or
 * --no-digest, and there must never be. They are deliberate overrides for a
 * human who has weighed the consequence; a button is a footgun.
 */

import { useId, useState } from "react";
import { AlertTriangle } from "lucide-react";

import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/Button";
import {
  deployTokenFor,
  destructiveFindings,
  isHostedPlan,
  type ForgeDeployReport,
} from "@/services/forge/deploy";

export interface DeployConfirmStepProps {
  /** The plan that was RENDERED. The token is derived from this and nothing else. */
  plan: ForgeDeployReport;
  onConfirm: () => void;
  onCancel: () => void;
  isStarting?: boolean;
}

export function DeployConfirmStep({
  plan,
  onConfirm,
  onCancel,
  isStarting,
}: DeployConfirmStepProps) {
  const [destructiveAcknowledged, setDestructiveAcknowledged] = useState(false);
  const destructiveCheckboxId = useId();

  const env = plan.env ?? "";
  const environmentName = env || "this environment";

  // The token comes from the plan. A plan that cannot produce one cannot
  // authorise a deploy — see deployTokenFor for why no default is safe.
  const token = deployTokenFor(plan);
  const hostedPlanDoc = isHostedPlan(plan);

  // Only an irreversible-loss finding is gated on; everything else advisory is
  // information, and a tick for information is the reflex this surface removed.
  const destructive = destructiveFindings(plan);
  const destructiveSatisfied = destructive.length === 0 || destructiveAcknowledged;

  const canStart = !!token && destructiveSatisfied && !isStarting;

  return (
    <section className="space-y-3" data-testid="deploy-confirm">
      {!token && (
        // No token means the plan cannot support a claim: it is not a preview,
        // the guard refused, or no target is declared. Confirming would be
        // authorising a write against an unknown destination.
        <p
          data-testid="deploy-no-token"
          className="rounded-lg border border-dashed border-destructive/50 px-3 py-2 text-xs text-destructive"
        >
          {hostedPlanDoc
            ? `${env || "This environment"} cannot be deployed from this plan. Plan it again to try.`
            : `This plan cannot authorise a deploy of ${env || "this environment"}: its environment declares no cluster, or forge's own guard refused it. Re-plan to try again.`}
        </p>
      )}

      {/* IRREVERSIBLE LOSS, on either destination. The deploy succeeds and the
          thing is still gone, so this is the one claim worth a tick. */}
      {token && destructive.length > 0 && (
        <div
          data-testid="deploy-destructive-findings"
          className="space-y-1.5 rounded-lg border border-solid border-destructive/50 bg-destructive/10 px-3 py-2"
        >
          <p className="flex items-center gap-1.5 text-xs font-medium text-foreground">
            <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-destructive" aria-hidden="true" />
            This deploy destroys something that can&apos;t be brought back.
          </p>
          <ul className="space-y-0.5">
            {destructive.map((finding, index) => (
              <li
                key={`${finding.check ?? "check"}:${finding.subject ?? ""}:${index}`}
                className="text-2xs text-muted-foreground"
              >
                <span className="font-mono text-foreground">{finding.subject}</span>
                {finding.detail && <span> — {finding.detail}</span>}
              </li>
            ))}
          </ul>
          <label
            htmlFor={destructiveCheckboxId}
            className="flex cursor-pointer items-start gap-2 pt-0.5"
          >
            <input
              id={destructiveCheckboxId}
              type="checkbox"
              checked={destructiveAcknowledged}
              onChange={(event) => setDestructiveAcknowledged(event.target.checked)}
              className="mt-0.5 h-3.5 w-3.5 shrink-0 accent-primary"
              data-testid="deploy-acknowledge-destructive"
            />
            <span className="text-2xs text-foreground">
              I understand this can&apos;t be undone.
            </span>
          </label>
        </div>
      )}

      <div className="flex items-center justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onCancel} disabled={isStarting}>
          Cancel
        </Button>
        <Button
          variant="destructive"
          size="sm"
          onClick={onConfirm}
          disabled={!canStart}
          loading={isStarting}
          data-testid="deploy-start"
          className={cn(!canStart && "opacity-60")}
        >
          {/* The button IS the approval, so it names the decision — which
              environment — and never the declared target, which the user did
              not choose and cannot change from here. */}
          {isStarting ? "Starting deploy…" : `Deploy to ${environmentName}`}
        </Button>
      </div>
    </section>
  );
}
