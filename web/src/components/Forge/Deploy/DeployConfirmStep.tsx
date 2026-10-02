// Copyright (c) 2025 Reliant Labs

/**
 * THE GUARDED CONFIRM for the only write on this surface that reaches live
 * infrastructure.
 *
 * THE GUARD IS STRUCTURAL, NOT A WARNING. This component cannot render without a
 * plan, and it cannot start a deploy without a confirmation token derived from
 * that plan — so "the user saw the target" and "the token names the target they
 * saw" are both properties of the type rather than of a code path someone
 * remembered to write. There is deliberately NO prop by which a caller could
 * pass an env, a cluster, a release or a token of its own choosing: the only
 * input is the document that was rendered.
 *
 * FROM HERE THE TWO DESTINATIONS DIVERGE, because the reader's relationship to
 * the target does.
 *
 * HOSTED: THE PLAN IS THE REVIEW, AND THE BUTTON IS THE APPROVAL. No typed
 * phrase, no "I have read the plan" checkbox. Both were removed because they
 * did not do what friction is supposed to do. The phrase was the control
 * plane's hostname — ours, not the reader's; they did not choose it, cannot
 * visit it, and every environment we host has the same one, so transcribing it
 * proved only that they could copy a string off the screen. Worse, it was the
 * most prominent instruction on the dialog, which taught them that the thing to
 * read was the box rather than the plan above it. The checkbox had the same
 * defect in a cheaper form: a claim nobody disbelieves, satisfiable by reflex.
 * What remains is a plan written in their own nouns and a button that names the
 * environment, which is a decision they can actually make.
 *
 * THE TOKEN STILL BINDS. Not typing it changed nothing about the safety
 * property: deployTokenFor derives it from the rendered plan, the request
 * carries it, and the server re-checks it — so a deploy whose declared target
 * or release moved between the preview and the click is still refused. The
 * transcription was never the mechanism; it was a demonstration of the
 * mechanism, aimed at the wrong audience.
 *
 * CLUSTER: UNCHANGED. Typing the context stays, every time. There the reader
 * owns the cluster, the name is theirs, it is genuinely ambiguous — a kubeconfig
 * holds many, and the ambient current-context is usually a DIFFERENT one than
 * the deploy targets — and the only way to produce the string is to read it off
 * the plan. That is friction that carries information, which is the test the
 * hosted phrase failed.
 *
 * DESTRUCTIVE FINDINGS BRING AN ACKNOWLEDGEMENT BACK, and they are the only
 * thing that does. A finding that says storage will be deleted or an address
 * reissued describes a deploy that SUCCEEDS and is still unrecoverable, which no
 * amount of re-deploying undoes — so it gets an explicit tick, shown only when
 * such a finding is present. Forge emits none today; the full
 * plan-before-promote treatment arrives with O-13, and this is the seam it
 * lands in rather than a checkbox waiting for a purpose. (A BLOCKING finding
 * needs no tick: it offers no confirm at all.)
 *
 * There is no field, toggle or "force" affordance for --skip-preflight or
 * --no-digest, and there must never be. They are deliberate overrides for a
 * human who has weighed the consequence; a button is a footgun.
 */

import { useId, useState } from "react";
import { AlertTriangle } from "lucide-react";

import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import {
  confirmPhrase,
  deployTokenFor,
  describeDeployToken,
  destructiveFindings,
  isHostedPlan,
  isMultiCluster,
  targetContexts,
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
  const [acknowledged, setAcknowledged] = useState(false);
  const [destructiveAcknowledged, setDestructiveAcknowledged] = useState(false);
  const [typedContext, setTypedContext] = useState("");
  const checkboxId = useId();
  const destructiveCheckboxId = useId();
  const typedContextId = useId();

  const env = plan.env ?? "";
  const environmentName = env || "this environment";
  const contexts = targetContexts(plan);
  const multi = isMultiCluster(plan);

  // The token comes from the plan. A plan that cannot produce one cannot
  // authorise a deploy — see deployTokenFor for why no default is safe.
  const token = deployTokenFor(plan);
  const isHostedPlanDoc = isHostedPlan(plan);

  const hosted = !!token?.hosted;
  const phrase = token ? confirmPhrase(token) : "";
  const contextMatches = !!token && typedContext.trim() === phrase;

  // Only an irreversible-loss finding is gated on; everything else advisory is
  // information, and a tick for information is the reflex this removed.
  const destructive = destructiveFindings(plan);
  const destructiveSatisfied = destructive.length === 0 || destructiveAcknowledged;

  const canStart =
    !!token &&
    destructiveSatisfied &&
    !isStarting &&
    // Hosted asks for nothing else: the plan was the review.
    (hosted || (acknowledged && contextMatches));

  return (
    <section className="space-y-3" data-testid="deploy-confirm">
      {token ? (
        <>
          {/* THE CLUSTER CEREMONY, for a target the reader owns and must name. */}
          {!hosted && (
            <>
              <label
                htmlFor={checkboxId}
                className="flex cursor-pointer items-start gap-2 rounded-lg border border-border px-3 py-2"
              >
                <input
                  id={checkboxId}
                  type="checkbox"
                  checked={acknowledged}
                  onChange={(event) => setAcknowledged(event.target.checked)}
                  className="mt-0.5 h-3.5 w-3.5 shrink-0 accent-primary"
                  data-testid="deploy-acknowledge"
                />
                {/* The user confirms a CLAIM, in words. That claim is what the
                    server re-checks, and a refusal is only intelligible to
                    someone who was shown the claim it refers to. */}
                <span className="text-xs text-foreground" data-testid="deploy-claim">
                  I have read the plan above, and {describeDeployToken(token)}.
                </span>
              </label>

              {multi && (
                // The whole blast radius, at the point of the click. The typed
                // name is the declared context — the one the token carries —
                // and the others are named so nobody discovers them afterwards.
                <p data-testid="deploy-confirm-all-contexts" className="text-2xs text-warning">
                  This writes to {contexts.length} clusters: {contexts.join(", ")}.
                </p>
              )}

              <div className="space-y-1.5 rounded-lg border border-solid border-destructive/50 bg-destructive/10 px-3 py-2">
                <p className="flex items-center gap-1.5 text-xs font-medium text-foreground">
                  {/* Hue on the icon and the panel; the sentence itself is
                      foreground — destructive text on its own tint measured
                      3.6:1 in dark, too low for the most important line here. */}
                  <AlertTriangle
                    className="h-3.5 w-3.5 shrink-0 text-destructive"
                    aria-hidden="true"
                  />
                  This applies manifests to a live cluster. It cannot be undone from git.
                </p>
                <label htmlFor={typedContextId} className="block text-2xs text-muted-foreground">
                  Type <span className="font-mono text-foreground">{phrase}</span> to confirm the
                  cluster.
                </label>
                <Input
                  id={typedContextId}
                  value={typedContext}
                  onChange={(event) => setTypedContext(event.target.value)}
                  placeholder={phrase}
                  autoComplete="off"
                  spellCheck={false}
                  className="font-mono text-xs"
                  data-testid="deploy-typed-context"
                />
              </div>
            </>
          )}

          {/* IRREVERSIBLE LOSS, on either destination. The deploy succeeds and
              the thing is still gone, so this is the one claim worth a tick. */}
          {destructive.length > 0 && (
            <div
              data-testid="deploy-destructive-findings"
              className="space-y-1.5 rounded-lg border border-solid border-destructive/50 bg-destructive/10 px-3 py-2"
            >
              <p className="flex items-center gap-1.5 text-xs font-medium text-foreground">
                <AlertTriangle
                  className="h-3.5 w-3.5 shrink-0 text-destructive"
                  aria-hidden="true"
                />
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
        </>
      ) : (
        // No token means the plan cannot support a claim: it is not a preview,
        // the guard refused, or no target is declared. Confirming would be
        // authorising a write against an unknown destination.
        <p
          data-testid="deploy-no-token"
          className="rounded-lg border border-dashed border-destructive/50 px-3 py-2 text-xs text-destructive"
        >
          {isHostedPlanDoc
            ? `${env || "This environment"} cannot be deployed from this plan. Plan it again to try.`
            : `This plan cannot authorise a deploy of ${env || "this environment"}: it does not name a declared cluster, or forge's own guard refused it. Re-plan to try again.`}
        </p>
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
          {isStarting
            ? "Starting deploy…"
            : !token
              ? `Deploy ${env}`
              : hosted
                ? // The button IS the approval, so it names the decision —
                  // the environment — and nothing about our infrastructure.
                  `Deploy to ${environmentName}`
                : `Deploy ${env} to ${phrase}`}
        </Button>
      </div>
    </section>
  );
}
