import { useCallback, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { Github, Loader2, Sparkles } from "lucide-react";
import { cn } from "@/lib/utils";
import { logger } from "@/lib/logger";
import { supabase } from "@/lib/supabase";
import { trackEvent } from "@/lib/analytics";
import { useCompleteOnboarding } from "@/hooks/useOnboardingQueries";
import { useGitHubCredential } from "@/hooks/useGitHubCredential";
import { gitService } from "@/services/controlPlane/git";
import { ensureProject, finalizeOnboardingSideEffects } from "../useOnboardingComplete";
import type { LaunchPlan, StepProps } from "../types";

export function ProjectChoiceStep({ plan, updatePlan }: StepProps) {
  const navigate = useNavigate();
  const completeOnboardingMutation = useCompleteOnboarding();
  const { hasToken: hasGithubCredential } = useGitHubCredential();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleStartNew = useCallback(async () => {
    if (!plan.compute) {
      setError("Missing compute selection. Go back and try again.");
      return;
    }
    setError(null);
    setBusy(true);
    try {
      const finalPlan: Partial<LaunchPlan> = { ...plan, intent: "build_app" };
      updatePlan({ intent: "build_app" });

      try {
        await ensureProject(finalPlan);
      } catch (projectErr) {
        logger.warn("[ProjectChoiceStep] ensureProject failed; aborting", projectErr);
        trackEvent("onboarding_ensure_project_failed", { error: String(projectErr) });
        setError(
          "Couldn't create your workspace. Please try again, or contact support if the problem persists.",
        );
        setBusy(false);
        return;
      }

      await completeOnboardingMutation.mutateAsync({
        compute: finalPlan.compute,
        modelProvider: finalPlan.modelProvider,
      });

      trackEvent("onboarding_completed", {
        provider: finalPlan.modelProvider ?? "unknown",
        compute: finalPlan.compute ?? "unknown",
        project_source: "new",
      });

      await finalizeOnboardingSideEffects(finalPlan.modelProvider);
      navigate({ to: "/", search: { step: undefined, plan: undefined } });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to finish onboarding");
      setBusy(false);
    }
  }, [completeOnboardingMutation, navigate, plan, updatePlan]);

  const handleConnectExisting = useCallback(async () => {
    setError(null);
    // Set intent first. If we already have a credential, derivation will
    // immediately route to the github-connect step (repo picker) on the
    // next render. If we don't, launch the custom OAuth flow with
    // returnTo back to /; the picker step naturally appears once intent
    // is set and the credential lands.
    await updatePlan({ intent: "existing_codebase" });
    if (hasGithubCredential) return;
    setBusy(true);
    try {
      const oauthURL = gitService.getOAuthURL();
      if (!oauthURL) throw new Error("Control plane URL not configured");
      const { data: { session } } = await supabase.auth.getSession();
      if (!session?.access_token) throw new Error("Not signed in");
      const returnTo = `${window.location.pathname}${window.location.search}`;
      const params = new URLSearchParams({ token: session.access_token, returnTo });
      window.location.href = `${oauthURL}?${params.toString()}`;
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to start GitHub OAuth");
      setBusy(false);
    }
  }, [hasGithubCredential, updatePlan]);

  return (
    <div className="space-y-6">
      <div className="space-y-2 text-center">
        <h2 className="text-2xl font-semibold tracking-tight text-foreground">
          Pick a starting point
        </h2>
        <p className="text-sm text-muted-foreground">
          You can change this later — both paths land you in the same workspace.
        </p>
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <button
          type="button"
          onClick={handleStartNew}
          disabled={busy}
          className={cn(
            "group relative flex min-h-[180px] min-w-0 flex-col items-start gap-3 overflow-hidden rounded-xl border-2 border-primary/40 p-5 text-left transition-all",
            "bg-[linear-gradient(135deg,rgba(56,189,248,0.18),rgba(168,85,247,0.18))]",
            !busy && "hover:border-primary hover:shadow-[0_18px_40px_-20px_rgba(56,189,248,0.55)]",
            busy && "cursor-not-allowed opacity-80",
          )}
        >
          <div className="flex h-10 w-10 flex-shrink-0 items-center justify-center rounded-lg bg-primary/20 text-primary ring-1 ring-primary/30">
            {busy ? <Loader2 className="h-5 w-5 animate-spin" /> : <Sparkles className="h-5 w-5" />}
          </div>
          <div className="min-w-0 space-y-1">
            <div className="flex items-center gap-2">
              <span className="text-base font-semibold text-foreground">Start something new</span>
              <span className="rounded bg-primary/20 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wider text-primary">
                Fastest
              </span>
            </div>
            <p className="text-xs leading-relaxed text-muted-foreground">
              Spin up a fresh workspace. Reliant scaffolds the project and gets the agent ready to build.
            </p>
          </div>
        </button>

        <button
          type="button"
          onClick={handleConnectExisting}
          disabled={busy}
          className={cn(
            "group relative flex min-h-[180px] min-w-0 flex-col items-start gap-3 overflow-hidden rounded-xl border-2 border-border/50 bg-background p-5 text-left transition-all",
            !busy && "hover:border-primary/50 hover:bg-muted/40",
            busy && "cursor-not-allowed opacity-60",
          )}
        >
          <div className="flex h-10 w-10 flex-shrink-0 items-center justify-center rounded-lg bg-muted text-foreground">
            <Github className="h-5 w-5" />
          </div>
          <div className="min-w-0 space-y-1">
            <span className="block text-base font-semibold text-foreground">
              Connect GitHub
            </span>
            <p className="text-xs leading-relaxed text-muted-foreground">
              Connect GitHub to work on an existing project.
            </p>
          </div>
        </button>
      </div>

      {error && <p className="text-center text-xs text-destructive">{error}</p>}
    </div>
  );
}
