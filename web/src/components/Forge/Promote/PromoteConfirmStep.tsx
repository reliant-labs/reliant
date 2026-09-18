// Copyright (c) 2025 Reliant Labs

/**
 * The guarded confirm.
 *
 * THE GUARD IS STRUCTURAL, NOT A WARNING. This component cannot render without a
 * plan, and it cannot enable its button without a confirmation token derived from
 * that plan — so "the user saw the diff" and "the token describes what they saw"
 * are both properties of the type, not of a code path someone remembered to
 * write. There is deliberately no prop by which a caller could pass an env, a
 * release or a token of its own choosing.
 *
 * EVERY ENVIRONMENT IS CONFIRMED. There is no "dev is safe" exemption, and the
 * reference project is why: all three of its bound environments are cloud, and
 * prod is live GKE. An exemption keyed on a name would be a guess about which
 * clusters matter, made by the one layer with no information about it.
 *
 * The user confirms a CLAIM, shown in words: "I have read this and <env> is
 * currently bound to <release>". That phrasing is not decoration — it is the
 * assertion the server will check, and a refusal is only intelligible to someone
 * who was shown the claim it refers to.
 *
 * A rollback additionally requires typing the environment name. The extra
 * friction is scoped to the one case where the default reading of the screen is
 * wrong: a forward promote does roughly what a reviewer skimming it expects,
 * while a rollback moves the environment backwards. Gating every promote behind
 * typing would train the reflex out and make the rollback case indistinguishable.
 */

import { useId, useState } from "react";
import { AlertTriangle } from "lucide-react";

import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import {
  confirmationTokenFor,
  describeToken,
  promoteDirectionOf,
  type ForgePromotePlan,
} from "@/services/forge/promote";

import { isDestructiveDirection } from "./promoteVocabulary";

export interface PromoteConfirmStepProps {
  /** The plan that was RENDERED. The token is derived from this and nothing else. */
  plan: ForgePromotePlan;
  onConfirm: () => void;
  onCancel: () => void;
  isApplying?: boolean;
}

export function PromoteConfirmStep({
  plan,
  onConfirm,
  onCancel,
  isApplying,
}: PromoteConfirmStepProps) {
  const [acknowledged, setAcknowledged] = useState(false);
  const [typedEnv, setTypedEnv] = useState("");
  const checkboxId = useId();
  const typedEnvId = useId();

  const direction = promoteDirectionOf(plan.direction);
  const rollback = isDestructiveDirection(direction);
  const env = plan.env ?? "";
  const release = plan.target?.release ?? plan.release ?? "";

  // The token comes from the plan. A plan that cannot produce one cannot
  // authorise a write — see confirmationTokenFor for why no default is safe.
  const token = confirmationTokenFor(plan);

  const envMatches = !rollback || typedEnv.trim() === env;
  const canApply = !!token && acknowledged && envMatches && !isApplying;

  return (
    <section className="space-y-3" data-testid="promote-confirm">
      {token ? (
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
            data-testid="promote-acknowledge"
          />
          <span className="text-xs text-foreground">
            I have read the diff above, and {describeToken(token)}.
          </span>
        </label>
      ) : (
        // No token means forge did not say what the env is bound to. Confirming
        // would be authorising a write against an unknown current state.
        <p
          data-testid="promote-no-token"
          className="rounded-lg border border-dashed border-destructive/50 px-3 py-2 text-xs text-destructive"
        >
          This plan does not say what {env || "the environment"} is currently bound to, so a promote
          cannot be authorised from it. Re-plan to try again.
        </p>
      )}

      {rollback && token && (
        <div className="space-y-1.5 rounded-lg border border-solid border-destructive/50 bg-destructive/10 px-3 py-2">
          <p className="flex items-center gap-1.5 text-xs font-medium text-destructive">
            <AlertTriangle className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
            This is a rollback. It moves {env} backwards.
          </p>
          <label htmlFor={typedEnvId} className="block text-2xs text-muted-foreground">
            Type <span className="font-mono text-foreground">{env}</span> to confirm.
          </label>
          <Input
            id={typedEnvId}
            value={typedEnv}
            onChange={(event) => setTypedEnv(event.target.value)}
            placeholder={env}
            autoComplete="off"
            spellCheck={false}
            className="font-mono text-xs"
            data-testid="promote-typed-env"
          />
        </div>
      )}

      {/* The irreversibility, stated where the button is. A forge EnvBinding
          keeps no history: the previous release is overwritten in place. */}
      <p className="text-2xs text-muted-foreground">
        Promoting overwrites {env || "this environment"}&apos;s binding in place. Forge keeps no
        history of the release it replaces.
      </p>

      <div className="flex items-center justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onCancel} disabled={isApplying}>
          Cancel
        </Button>
        <Button
          variant={rollback ? "destructive" : "primary"}
          size="sm"
          onClick={onConfirm}
          disabled={!canApply}
          loading={isApplying}
          data-testid="promote-apply"
          className={cn(!canApply && "opacity-60")}
        >
          {isApplying
            ? "Writing binding…"
            : rollback
              ? `Roll ${env} back to ${release}`
              : `Promote ${env} to ${release}`}
        </Button>
      </div>
    </section>
  );
}
