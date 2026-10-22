import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { Loader2 } from "lucide-react";
import { logger } from "@/lib/logger";
import { useCompleteOnboarding } from "@/hooks/useOnboardingQueries";
import { Button } from "@/components/ui/Button";
import { ensureProject, finalizeOnboardingSideEffects } from "../useOnboardingComplete";
import { leaveOnboarding } from "../leaveOnboarding";
import { markOnboardingFinalized } from "../analytics";
import { ProvisioningGate } from "../ProvisioningGate";
import { useCommitLaunchPlan } from "../useCommitLaunchPlan";
import type { StepProps } from "../types";

/**
 * Terminal step for local compute. There is nothing to ask: the user's own
 * machine is the workspace, so it lands on their existing project or a default
 * one (see `ensureProject`), then completes, commits and shows the
 * provisioning gate — the same sequence the cloud terminal steps run.
 *
 * Runs once on mount. Failures stop and show Retry; billable work is never
 * retried automatically.
 */
export function FinishStep({ plan, updatePlan, onBack }: StepProps) {
  const navigate = useNavigate();
  const completeOnboardingMutation = useCompleteOnboarding();
  const { commit, runCommit, retry } = useCommitLaunchPlan(updatePlan);
  const [error, setError] = useState<string | null>(null);
  // A ref, not state: StrictMode re-runs the effect before a state update
  // would be visible, and this sequence must not start twice.
  const startedRef = useRef(false);

  const finalize = useCallback(async () => {
    if (!plan.compute) {
      setError("Missing compute selection. Go back and try again.");
      return;
    }
    setError(null);
    try {
      await ensureProject(plan);
      await completeOnboardingMutation.mutateAsync({
        compute: plan.compute,
        modelProvider: plan.modelProvider,
      });
      markOnboardingFinalized(plan, "new");
      await finalizeOnboardingSideEffects();
      await runCommit(plan);
    } catch (err) {
      logger.warn("[FinishStep] finalize failed", err);
      setError(err instanceof Error ? err.message : "Failed to finish onboarding");
    }
  }, [completeOnboardingMutation, plan, runCommit]);

  useEffect(() => {
    if (startedRef.current) return;
    startedRef.current = true;
    void finalize();
    // Mount-only by design; `finalize` closes over the plan at that moment.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const goToChat = useCallback(() => {
    void leaveOnboarding("completed_cloud_gate_continue", navigate);
  }, [navigate]);

  if (commit) {
    return (
      <div className="space-y-6">
        <ProvisioningGate
          commit={commit}
          onContinue={goToChat}
          onRetry={() => void retry(plan)}
        />
      </div>
    );
  }

  if (error) {
    return (
      <div className="space-y-6">
        <div className="space-y-2 text-center">
          <h2 className="text-xl font-semibold tracking-tight text-foreground text-balance">
            We couldn't finish setting up
          </h2>
          <p role="alert" className="text-sm text-destructive text-pretty">
            {error}
          </p>
        </div>
        <div className="flex justify-center gap-2">
          <Button variant="outline" onClick={onBack}>
            Back
          </Button>
          <Button
            onClick={() => {
              void finalize();
            }}
          >
            Retry
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className="flex flex-col items-center gap-3 py-8 text-center">
      <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" aria-hidden />
      <h2 className="text-xl font-semibold tracking-tight text-foreground">
        Setting up your project…
      </h2>
      <p className="text-sm text-muted-foreground text-pretty">
        Reliant is preparing a folder to work in. You can add more projects later.
      </p>
    </div>
  );
}
