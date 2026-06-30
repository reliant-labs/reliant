import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { KeyRound, Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";
import { getIsDev } from "@/lib/constants";
import { useCloudEligibility } from "@/hooks/useOnboardingQueries";
import { trackEvent } from "@/lib/analytics";
import type { ModelProvider, StepProps } from "../types";

function getForcedEligibility(): "eligible" | "ineligible" | null {
  if (typeof window === "undefined") return null;
  const value = new URLSearchParams(window.location.search).get(
    "onboarding-credits",
  );
  if (value === "eligible") return "eligible";
  if (value === "ineligible") return "ineligible";
  return null;
}

export function ModelStep({ plan, updatePlan, onNext }: StepProps) {
  const navigate = useNavigate();
  const cloudEligibility = useCloudEligibility();

  const forcedEligibility = getForcedEligibility();
  const isEligible =
    forcedEligibility === "eligible" ||
    (forcedEligibility == null && (getIsDev() || cloudEligibility.eligible));
  const eligibilityLoading =
    forcedEligibility == null && !getIsDev() && cloudEligibility.isLoading;

  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const finishOnboarding = useCallback(
    async (modelProvider: ModelProvider) => {
      if (!plan.compute) {
        setError("Choose where Reliant should run before finishing setup.");
        return;
      }

      setError(null);
      trackEvent("onboarding_model_selected", { provider: modelProvider });
      await updatePlan({ modelProvider });
      onNext();
    },
    [onNext, plan, updatePlan],
  );

  // ── Auto-default to Reliant credits (skip this step) ─────────────────────
  //
  // The model step used to be a 6-provider BYO-key fork shown before the user
  // saw any value. For users eligible for Reliant's free credits there is an
  // unambiguous best default — Reliant routing, no key needed — so we pick it
  // for them and skip the step entirely. Mirrors ComputeStep's auto-skip:
  // we wait for eligibility to settle (`!eligibilityLoading`) so the form
  // doesn't flash before the decision, and a ref guards against re-entry.
  //
  // Reversible by construction: the skip is just `updatePlan({ modelProvider })`,
  // and deriveStep moves past `model` only because modelProvider is now set.
  // Ineligible users (no credits available) still render the step below, where
  // Reliant is the prominent default and BYO is a demoted link to Settings.
  const hasAutoSkipped = useRef(false);
  useEffect(() => {
    if (hasAutoSkipped.current) return;
    if (plan.modelProvider) return;
    if (!plan.compute) return;
    if (eligibilityLoading) return;
    if (!isEligible) return;
    hasAutoSkipped.current = true;
    trackEvent("onboarding_model_autoskipped", { provider: "reliant_credits" });
    void finishOnboarding("reliant_credits");
    // finishOnboarding closes over plan/updatePlan/onNext, but the ref guards
    // re-entry, so we narrow deps to the trigger conditions.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isEligible, eligibilityLoading, plan.modelProvider, plan.compute]);

  const handleStartReliant = useCallback(async () => {
    setBusy(true);
    try {
      await finishOnboarding("reliant_credits");
    } finally {
      setBusy(false);
    }
  }, [finishOnboarding]);

  // "Use my own API key instead" — start onboarding on Reliant routing (so the
  // flow completes and the workspace is usable immediately) and hand the user
  // off to Settings → General, where CombinedGeneralSettings owns provider-key
  // entry. This deliberately avoids re-implementing the BYO-key fork inline.
  const handleUseOwnKey = useCallback(async () => {
    setBusy(true);
    try {
      trackEvent("onboarding_model_byo_deferred_to_settings");
      await finishOnboarding("reliant_credits");
      navigate({ to: "/settings/$section", params: { section: "general" } });
    } finally {
      setBusy(false);
    }
  }, [finishOnboarding, navigate]);

  const creditsAvailable = isEligible;

  // Eligible users are auto-skipped by the effect above; render a neutral
  // placeholder for the frame between mount and the skip firing so the BYO
  // fallback UI never flashes for them.
  if (isEligible && !eligibilityLoading && !plan.modelProvider) {
    return (
      <div
        className="space-y-5 py-6 text-center"
        role="status"
        aria-live="polite"
      >
        <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-primary/15 text-primary">
          <Loader2 className="h-7 w-7 animate-spin" />
        </div>
        <p className="text-xs text-muted-foreground">Setting up your models…</p>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div className="space-y-2 text-center">
        <h2 className="text-2xl font-semibold tracking-tight text-foreground">
          Start building with Reliant
        </h2>
        <p className="text-sm text-muted-foreground">
          No API key needed — Reliant routes to the best available model. You
          can connect your own provider any time in Settings.
        </p>
      </div>

      {/* Primary path: Reliant model routing. Eligible users never see this
          step (the auto-skip effect picks reliant_credits and advances); this
          card is what ineligible users land on, with BYO demoted to the link
          below that defers to Settings → General. */}
      <div className="space-y-3 rounded-xl border-2 border-primary/40 bg-[linear-gradient(135deg,rgba(56,189,248,0.12),rgba(168,85,247,0.10))] p-5">
        <div className="flex items-start gap-3">
          <KeyRound className="mt-0.5 h-4 w-4 text-primary" />
          <div>
            <h3 className="text-sm font-medium text-foreground">
              Use Reliant&apos;s model routing
            </h3>
            {eligibilityLoading ? (
              <div className="mt-0.5 flex items-center gap-2 text-xs text-muted-foreground">
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
                Checking credit availability...
              </div>
            ) : creditsAvailable ? (
              <p className="mt-0.5 text-xs leading-relaxed text-muted-foreground">
                $20 free credit included &mdash; no API key needed.
              </p>
            ) : (
              <p className="mt-0.5 text-xs leading-relaxed text-muted-foreground">
                No API key needed. Reliant routes to the best available model
                automatically.
              </p>
            )}
          </div>
        </div>
        <button
          type="button"
          onClick={handleStartReliant}
          disabled={busy}
          className={cn(
            "inline-flex w-full items-center justify-center gap-2 rounded-lg py-2.5 text-sm font-semibold transition-colors",
            !busy
              ? "bg-primary text-primary-foreground hover:bg-primary/90"
              : "cursor-not-allowed bg-muted text-muted-foreground",
          )}
        >
          {busy && <Loader2 className="h-4 w-4 animate-spin" />}
          Start with Reliant
        </button>
      </div>

      {/* Demoted BYO affordance. Defers key entry to Settings → General
          (CombinedGeneralSettings) rather than re-implementing the
          provider/key/OAuth fork inline here. */}
      <div className="text-center">
        <button
          type="button"
          onClick={handleUseOwnKey}
          disabled={busy}
          className="text-xs font-medium text-muted-foreground underline-offset-4 transition-colors hover:text-foreground hover:underline disabled:cursor-not-allowed disabled:opacity-60"
        >
          Use my own API key instead &rarr;
        </button>
      </div>

      {error && <p className="text-center text-xs text-destructive">{error}</p>}
    </div>
  );
}
