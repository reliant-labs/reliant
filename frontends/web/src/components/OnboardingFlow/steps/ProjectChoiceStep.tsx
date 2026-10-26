import { useCallback, useState, type ReactNode } from "react";
import { useNavigate } from "@tanstack/react-router";
import { Github, Loader2, MessageSquare, Rocket } from "lucide-react";
import { cn } from "@/lib/utils";
import { logger } from "@/lib/logger";
import { connectGitHub } from "@/lib/github-connect";
import { gitService } from "@/services/controlPlane/git";
import { trackEvent } from "@/lib/analytics";
import { useCompleteOnboarding } from "@/hooks/useOnboardingQueries";
import { useGitHubCredential } from "@/hooks/useGitHubCredential";
import { ensureProject, finalizeOnboardingSideEffects } from "../useOnboardingComplete";
import { leaveOnboarding } from "../leaveOnboarding";
import { markOnboardingFinalized } from "../analytics";
import { ProvisioningGate } from "../ProvisioningGate";
import { useCommitLaunchPlan } from "../useCommitLaunchPlan";
import type { LaunchPlan, OnboardingIntent, StepProps } from "../types";

export function ProjectChoiceStep({ plan, updatePlan }: StepProps) {
  const navigate = useNavigate();
  const completeOnboardingMutation = useCompleteOnboarding();
  const { hasToken: hasGithubCredential, isLoading: credentialLoading } = useGitHubCredential();
  // Which choice is in flight, so its card shows the spinner; false when idle.
  const [busy, setBusy] = useState<false | OnboardingIntent>(false);
  const [error, setError] = useState<string | null>(null);
  // The commit point. Onboarding creates nothing until this runs, and it runs
  // once, here, after the server has confirmed onboarding — not from an effect
  // and not from a step observing a state change.
  const { commit, runCommit, retry } = useCommitLaunchPlan(updatePlan);

  // The exit: the gate's Continue is the ONLY way out of the terminal step,
  // which is what makes it impossible to leave while the machine is still
  // provisioning — or to leave without seeing that provisioning failed.
  //
  // Where it lands follows the choice: "Build an app" opens the app-building
  // composer, "Just chat" the plain new chat (leaveOnboarding).
  const leave = useCallback(() => {
    void leaveOnboarding("completed_cloud_gate_continue", navigate, plan.intent);
  }, [navigate, plan.intent]);

  // Both start in the default project (ensureProject); they differ only in where the user lands.
  const handleStartNew = useCallback(async (intent: "build_app" | "chat") => {
    if (!plan.compute) {
      setError("Missing compute selection. Go back and try again.");
      return;
    }
    setError(null);
    setBusy(intent);
    trackEvent("onboarding_intent_selected", { intent });
    try {
      const finalPlan: Partial<LaunchPlan> = { ...plan, intent };
      updatePlan({ intent });

      try {
        await ensureProject(finalPlan);
      } catch (projectErr) {
        logger.warn("[ProjectChoiceStep] ensureProject failed; aborting", projectErr);
        trackEvent("onboarding_ensure_project_failed", { error: String(projectErr) });
        setError(
          "Couldn't set up your project. Please try again, or contact support if the problem persists.",
        );
        setBusy(false);
        return;
      }

      await completeOnboardingMutation.mutateAsync({
        compute: finalPlan.compute,
        modelProvider: finalPlan.modelProvider,
      });

      markOnboardingFinalized(finalPlan, "new");

      await finalizeOnboardingSideEffects();

      // Commit, then let the gate narrate it. A plan that asked for nothing
      // (local compute, own key) commits instantly with no tasks and the gate
      // falls through to Continue, so there is no wait to sit through.
      await runCommit(finalPlan);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to finish onboarding");
      setBusy(false);
    }
  }, [completeOnboardingMutation, plan, runCommit, updatePlan]);

  const handleConnectExisting = useCallback(async () => {
    setError(null);
    trackEvent("onboarding_intent_selected", { intent: "existing_codebase" });
    // Set intent first. With a credential already stored, derivation routes
    // straight to the github-connect step (the repo picker) and there is no
    // OAuth hop. Without one, connect GitHub; the picker step appears once
    // intent is set and the credential lands (a web return reloads onto it,
    // the desktop app finishes in place).
    await updatePlan({ intent: "existing_codebase" });
    // While the credential query is still loading `hasToken` reads false for a
    // user who has one; ask the server rather than send them through consent
    // they have already given.
    let hasCredential = hasGithubCredential;
    if (!hasCredential && credentialLoading) {
      hasCredential = await gitService
        .getCredential("github")
        .then((status) => status.hasToken)
        .catch(() => false);
    }
    if (hasCredential) return;
    setBusy("existing_codebase");
    const outcome = await connectGitHub();
    if (outcome.kind === "redirecting") return;
    if (outcome.kind === "error") setError(outcome.message);
    setBusy(false);
  }, [hasGithubCredential, credentialLoading, updatePlan]);

  if (commit) {
    return (
      <div className="space-y-6">
        <ProvisioningGate
          commit={commit}
          onContinue={leave}
          onRetry={() => void retry(plan)}
        />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div className="space-y-2 text-center">
        <h2 className="text-2xl font-semibold tracking-tight text-foreground">
          What do you want to do first?
        </h2>
        <p className="text-sm text-muted-foreground">
          You can do all of these later — this is just where you start.
        </p>
      </div>

      <div className="grid gap-3 sm:grid-cols-3">
        <ChoiceCard
          primary
          icon={<Rocket className="h-5 w-5" />}
          title="Build an app"
          tag="Fastest"
          description="Describe it. Reliant builds it, hosts it, and gives you a live URL."
          onClick={() => void handleStartNew("build_app")}
          busy={busy === "build_app"}
          disabled={busy !== false}
          testId="onboarding-choice-build-app"
        />
        <ChoiceCard
          icon={<Github className="h-5 w-5" />}
          title="Connect GitHub"
          description="Pick a repository and Reliant will start working in it."
          onClick={() => void handleConnectExisting()}
          busy={busy === "existing_codebase"}
          disabled={busy !== false}
          testId="onboarding-choice-github"
        />
        <ChoiceCard
          icon={<MessageSquare className="h-5 w-5" />}
          title="Just chat"
          description="Ask questions, research and write. A project is there when you need one."
          onClick={() => void handleStartNew("chat")}
          busy={busy === "chat"}
          disabled={busy !== false}
          testId="onboarding-choice-chat"
        />
      </div>

      {error && <p className="text-center text-xs text-destructive-ink">{error}</p>}
    </div>
  );
}

function ChoiceCard({
  primary = false,
  icon,
  title,
  tag,
  description,
  onClick,
  busy,
  disabled,
  testId,
}: {
  primary?: boolean;
  icon: ReactNode;
  title: string;
  tag?: string;
  description: string;
  onClick: () => void;
  busy: boolean;
  disabled: boolean;
  testId: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      aria-busy={busy || undefined}
      data-testid={testId}
      className={cn(
        "group relative flex min-h-[180px] min-w-0 flex-col items-start gap-3 overflow-hidden rounded-xl border-2 p-5 text-left transition-all",
        primary
          ? "border-primary/40 bg-[linear-gradient(135deg,rgba(56,189,248,0.18),rgba(168,85,247,0.18))]"
          : "border-border/50 bg-background",
        !disabled && (primary
          ? "hover:border-primary hover:shadow-[0_18px_40px_-20px_rgba(56,189,248,0.55)]"
          : "hover:border-primary/50 hover:bg-muted/40"),
        disabled && (busy ? "cursor-wait" : "cursor-not-allowed opacity-60"),
      )}
    >
      <div
        className={cn(
          "flex h-10 w-10 flex-shrink-0 items-center justify-center rounded-lg",
          primary ? "bg-primary/20 text-primary ring-1 ring-primary/30" : "bg-muted text-foreground",
        )}
      >
        {busy ? <Loader2 className="h-5 w-5 animate-spin" /> : icon}
      </div>
      <div className="min-w-0 space-y-1">
        <div className="flex items-center gap-2">
          <span className="text-base font-semibold text-foreground">{title}</span>
          {tag && (
            <span className="rounded bg-primary/20 px-1.5 py-0.5 text-2xs font-medium uppercase tracking-wider text-primary">
              {tag}
            </span>
          )}
        </div>
        <p className="text-xs leading-relaxed text-muted-foreground">{description}</p>
      </div>
    </button>
  );
}
