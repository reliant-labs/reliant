// Copyright (c) 2025 Reliant Labs

/**
 * THE GUARDED CONFIRM for the only write on this surface that reaches a live
 * cluster.
 *
 * THE GUARD IS STRUCTURAL, NOT A WARNING. This component cannot render without a
 * plan, and it cannot enable its button without a confirmation token derived from
 * that plan — so "the user saw the cluster" and "the token names the cluster they
 * saw" are both properties of the type rather than of a code path someone
 * remembered to write. There is deliberately NO prop by which a caller could pass
 * an env, a cluster, a release or a token of its own choosing: the only input is
 * the document that was rendered.
 *
 * TYPING THE CLUSTER NAME IS REQUIRED, EVERY TIME. Promote reserves that friction
 * for a rollback, because a forward promote does roughly what a reviewer skimming
 * it expects. No deploy is in that category. It applies manifests to a cluster
 * and for a cloud environment nothing can undo it, so the cluster the operator is
 * about to write to is typed out in full — which is also the one piece of
 * friction that cannot be satisfied by a reflex, because it requires reading the
 * name off the plan above.
 *
 * MULTI-CLUSTER TYPES THE DECLARED CONTEXT, the one the token carries and the one
 * the server re-checks. The full set is displayed here again so the blast radius
 * is on screen at the moment of the click, not only further up the page.
 *
 * There is no field, toggle or "force" affordance for --skip-preflight or
 * --no-digest, and there must never be. They are deliberate overrides for a human
 * who has weighed the consequence; a button is a footgun.
 */

import { useId, useState } from "react";
import { AlertTriangle } from "lucide-react";

import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import {
  deployTokenFor,
  describeDeployToken,
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
  const [typedContext, setTypedContext] = useState("");
  const checkboxId = useId();
  const typedContextId = useId();

  const env = plan.env ?? "";
  const contexts = targetContexts(plan);
  const multi = isMultiCluster(plan);

  // The token comes from the plan. A plan that cannot produce one cannot
  // authorise a deploy — see deployTokenFor for why no default is safe, and note
  // that the cluster half has no "unset" spelling at all.
  const token = deployTokenFor(plan);

  const contextMatches = !!token && typedContext.trim() === token.expectedDeclaredContext;
  const canStart = !!token && acknowledged && contextMatches && !isStarting;

  return (
    <section className="space-y-3" data-testid="deploy-confirm">
      {token ? (
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
            {/* The user confirms a CLAIM, in words. That claim is what the server
                re-checks, and a refusal is only intelligible to someone who was
                shown the claim it refers to. */}
            <span className="text-xs text-foreground" data-testid="deploy-claim">
              I have read the plan above, and {describeDeployToken(token)}.
            </span>
          </label>

          {multi && (
            // The whole blast radius, at the point of the click. The typed name is
            // the declared context — the one the token carries — and the others
            // are named so nobody discovers them afterwards.
            <p data-testid="deploy-confirm-all-contexts" className="text-2xs text-warning">
              This writes to {contexts.length} clusters: {contexts.join(", ")}.
            </p>
          )}

          <div className="space-y-1.5 rounded-lg border border-solid border-destructive/50 bg-destructive/10 px-3 py-2">
            <p className="flex items-center gap-1.5 text-xs font-medium text-destructive">
              <AlertTriangle className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
              This applies manifests to a live cluster. It cannot be undone from git.
            </p>
            <label htmlFor={typedContextId} className="block text-2xs text-muted-foreground">
              Type{" "}
              <span className="font-mono text-foreground">{token.expectedDeclaredContext}</span> to
              confirm the cluster.
            </label>
            <Input
              id={typedContextId}
              value={typedContext}
              onChange={(event) => setTypedContext(event.target.value)}
              placeholder={token.expectedDeclaredContext}
              autoComplete="off"
              spellCheck={false}
              className="font-mono text-xs"
              data-testid="deploy-typed-context"
            />
          </div>
        </>
      ) : (
        // No token means the plan cannot support a claim: it is not a preview,
        // forge's guard refused, or no cluster is declared. Confirming would be
        // authorising a write against an unknown target.
        <p
          data-testid="deploy-no-token"
          className="rounded-lg border border-dashed border-destructive/50 px-3 py-2 text-xs text-destructive"
        >
          This plan cannot authorise a deploy of {env || "this environment"}: it does not name a
          declared cluster, or forge&apos;s own guard refused it. Re-plan to try again.
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
            : token
              ? `Deploy ${env} to ${token.expectedDeclaredContext}`
              : `Deploy ${env}`}
        </Button>
      </div>
    </section>
  );
}
