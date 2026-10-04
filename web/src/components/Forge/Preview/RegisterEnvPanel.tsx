// Copyright (c) 2025 Reliant Labs

/**
 * "WOULD BE CREATED" — and the button that creates it.
 *
 * ── THE CHICKEN-AND-EGG ─────────────────────────────────────────────────────
 *
 * An environment declared in the KCL and never built has no control-plane row.
 * So Live cannot show it, and the managed store has no environment to hold a
 * value against — which is the trap: you cannot set a secret before the first
 * deploy, and you cannot deploy without the secret.
 *
 * Register breaks it in one click (briefing §6, design §10 state 2):
 *
 *   1. Preview reads forge's own projection of the env's render on the daemon
 *      (`forge env shape <env> --json`) — the only daemon call on this path,
 *      and it is here because only the daemon can read the user's files.
 *   2. The BROWSER calls control-plane EnsureEnvironment with the kind and
 *      shape from that render, using the USER'S SESSION. Not the daemon's
 *      token — that indirection is what produced the owner's 403.
 *   3. Live then shows "Declared, not built yet", and secrets can be set from
 *      Live with the daemon offline, for good.
 *
 * ── THE USER IS NEVER ASKED ─────────────────────────────────────────────────
 *
 * Not for the kind, not for anything. An environment's kind is IMMUTABLE once
 * the row exists, so a wrong answer produces an environment that can only be
 * abandoned — and a human answering a form is exactly the guess #353 had to
 * guard against afterwards with a FailedPrecondition. The kind is forge's
 * answer, recorded at Preview time, which is the owner's direction. When forge
 * could not state it, this panel REFUSES and says why rather than offering a
 * button that would write a guess.
 */

import { Plus } from "lucide-react";

import Badge from "@/components/forge-ui/badge";
import { Button } from "@/components/ui/Button";
import { useForgeEnvShape, useRegisterEnvironment } from "@/hooks/forge-queries";
import { liveKindLabel } from "@/services/forge/live";
import { registerCandidate } from "@/services/forge/register";

export function RegisterEnvPanel({
  projectId,
  envName,
  forgeProject,
  /** Whether forge's topology declares this env in the current checkout. */
  declaredHere,
  /**
   * Called once the add succeeds, so the parent can keep this panel mounted
   * to show the confirmation — the add is what makes Live know the
   * environment, which would otherwise unmount the panel mid-confirmation.
   */
  onRegistered,
}: {
  projectId: string | null;
  envName: string;
  forgeProject: string | null;
  declaredHere: boolean;
  onRegistered?: () => void;
}) {
  const shape = useForgeEnvShape(projectId, envName);
  const register = useRegisterEnvironment();

  const report = shape.data?.kind === "report" ? shape.data.report : null;
  const candidate = registerCandidate(report, { project: forgeProject, env: envName });

  return (
    <section
      className="space-y-3"
      data-testid="register-env-panel"
      aria-labelledby="register-env-heading"
    >
      <div className="flex flex-wrap items-center gap-2">
        <h3 id="register-env-heading" className="text-sm font-semibold text-foreground">
          <span className="font-mono">{envName}</span>
        </h3>
        <Badge label="would be created" variant="neutral" size="sm" />
      </div>

      {/* The customer's nouns (#366): what they get, not where we file it.
          "Reliant has no record of it" is a fact about their project;
          "the control plane has no row" is a fact about our database. */}
      <p className="text-xs text-muted-foreground">
        {declaredHere
          ? "Your code declares this environment. Registering records what forge renders for it, so its releases and secrets are tracked here — even with your daemon offline."
          : "Reliant hasn't seen this environment yet. Adding it records what forge renders for it."}
      </p>

      {shape.isLoading && (
        <p data-testid="register-shape-loading" className="text-xs text-muted-foreground">
          Rendering <span className="font-mono">{envName}</span>…
        </p>
      )}

      {candidate.ok ? (
        <>
          <dl className="flex flex-wrap gap-x-6 gap-y-1 text-xs text-muted-foreground">
            <Fact label="Runs as">
              <span className="text-foreground">{liveKindLabel(candidate.candidate.kind)}</span>
            </Fact>
            <Fact label="Project">
              <span className="font-mono text-foreground">{candidate.candidate.project}</span>
            </Fact>
          </dl>
          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() =>
                register.mutate(candidate.candidate, { onSuccess: () => onRegistered?.() })
              }
              loading={register.isPending}
              disabled={register.isPending}
              leftIcon={<Plus className="h-3 w-3" />}
              data-testid={`register-${envName}`}
              aria-label={`Add ${envName} to this project`}
            >
              {register.isPending ? "Registering…" : "Register"}
            </Button>
          </div>
          {register.error && (
            // The server's own words, as `detail`. EnsureEnvironment refuses —
            // rather than rewriting — a declaration whose immutable fields
            // disagree with what is already recorded, and that refusal is the
            // one message worth passing through verbatim.
            <p data-testid="register-error" className="text-xs text-muted-foreground">
              Couldn&apos;t add <span className="font-mono">{envName}</span>.{" "}
              <span className="font-mono text-2xs">{register.error.message}</span>
            </p>
          )}
        </>
      ) : (
        !shape.isLoading && (
          <p data-testid="register-unavailable" className="text-xs text-muted-foreground">
            {candidate.reason}
          </p>
        )
      )}
    </section>
  );
}

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-baseline gap-1.5">
      <dt className="text-2xs font-medium uppercase tracking-wide">{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}
