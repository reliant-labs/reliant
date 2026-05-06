import { useEffect } from "react";
import { createPortal } from "react-dom";
import { ChevronLeft } from "lucide-react";
import { useProjectStore } from "@/store/projectStore";
import { useAuthStore } from "@/store/authStore";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { cn } from "@/lib/utils";
import { useOnboardingFlowStore } from "./onboardingStore";
import { ProgressBar } from "./ProgressBar";

// Dev reset: URL param ?reset-onboarding clears state and forces onboarding to show
function handleDevReset(): boolean {
  if (typeof window === "undefined") return false;
  const params = new URLSearchParams(window.location.search);
  if (!params.has("reset-onboarding")) return false;

  const store = useOnboardingFlowStore.getState();
  store.reset();
  store.setDevForceShow(true);

  // Remove the query param so it doesn't persist on refresh
  const url = new URL(window.location.href);
  url.searchParams.delete("reset-onboarding");
  window.history.replaceState({}, "", url.toString());

  return true;
}

// Dev console helper
if (typeof window !== "undefined" && import.meta.env.DEV) {
  (window as any).__resetOnboarding = () => {
    const store = useOnboardingFlowStore.getState();
    store.reset();
    localStorage.removeItem("reliant-onboarding");
    console.log("Onboarding state reset. Refresh the page.");
  };
}

export function OnboardingOverlay() {
  const state = useOnboardingFlowStore((s) => s.state);
  const plan = useOnboardingFlowStore((s) => s.plan);
  const currentStepIndex = useOnboardingFlowStore((s) => s.currentStepIndex);
  const updatePlan = useOnboardingFlowStore((s) => s.updatePlan);
  const nextStep = useOnboardingFlowStore((s) => s.nextStep);
  const prevStep = useOnboardingFlowStore((s) => s.prevStep);
  const hydrating = useOnboardingFlowStore((s) => s.hydrating);
  const devForceShow = useOnboardingFlowStore((s) => s.devForceShow);
  const hydrateFromBackend = useOnboardingFlowStore((s) => s.hydrateFromBackend);
  const requireOnboarding = useOnboardingFlowStore((s) => s.requireOnboarding);
  const visibleSteps = useOnboardingFlowStore((s) => s.visibleSteps);
  const projects = useProjectStore((s) => s.projects);
  const currentProject = useProjectStore((s) => s.currentProject);
  const projectsLoading = useProjectStore((s) => s.isLoading);
  const authUser = useAuthStore((s) => s.user);
  const authSession = useAuthStore((s) => s.session);
  const authLoading = useAuthStore((s) => s.loading);
  const authInitialized = useAuthStore((s) => s.initialized);
  const { activeDaemon } = useDaemonStatus();
  const hasProjects = projects.length > 0 || Boolean(currentProject);
  const showSignIn = authInitialized && !authLoading && !authUser && !authSession;

  // Sync daemonPreConnected with the live daemon status. If a daemon is
  // already active, drop daemon-connect from the path and pre-pick the
  // local-daemon compute choice when the user hasn't picked yet.
  useEffect(() => {
    if (!activeDaemon) return;
    if (!plan.compute) {
      updatePlan({
        compute: "local_daemon",
        daemonLocation: "self_hosted",
        daemonProvisioning: false,
        daemonPreConnected: true,
      });
    } else if (plan.compute === "local_daemon" && !plan.daemonPreConnected) {
      updatePlan({ daemonPreConnected: true });
    }
  }, [activeDaemon, plan.compute, plan.daemonPreConnected, updatePlan]);

  // On mount: check for dev reset param, then hydrate from backend.
  useEffect(() => {
    const wasReset = handleDevReset();
    if (!wasReset && hasProjects) {
      hydrateFromBackend();
    }
  }, [hasProjects, hydrateFromBackend]);

  useEffect(() => {
    if (!projectsLoading && !hasProjects) {
      requireOnboarding();
    }
  }, [hasProjects, projectsLoading, requireOnboarding]);

  // Don't render on admin or auth pages
  if (typeof window !== "undefined") {
    const path = window.location.pathname;
    if (path.includes("/admin") || path.includes("/auth")) {
      return null;
    }
  }

  // Don't flash the overlay while hydrating from backend
  if (hydrating) {
    return null;
  }

  // Hide once onboarding completes (unless dev override is active).
  if (state === "completed" && !devForceShow) {
    return null;
  }

  // No-project users must complete required setup before entering the app.
  const shouldShow =
    !projectsLoading &&
    (!hasProjects || state === "not_started" || state === "in_progress" || devForceShow);
  if (!shouldShow) {
    return null;
  }

  // Need at least one registered step
  const steps = visibleSteps();
  if (steps.length === 0) {
    return null;
  }

  const safeStepIndex = Math.min(currentStepIndex, steps.length - 1);
  const currentStep = steps[safeStepIndex] ?? null;
  if (!currentStep) {
    return null;
  }

  const StepComponent = currentStep.component;
  const isFirst = safeStepIndex === 0;

  const overlay = (
    <div
      className="fixed inset-0 z-40 flex items-center justify-center overflow-hidden p-4"
      role="dialog"
      aria-modal="true"
      aria-label="Onboarding setup"
    >
      {/* Backdrop */}
      <div
        className="absolute inset-0 bg-[radial-gradient(circle_at_20%_20%,rgba(14,165,233,0.32),transparent_32%),radial-gradient(circle_at_78%_12%,rgba(168,85,247,0.28),transparent_30%),radial-gradient(circle_at_52%_88%,rgba(16,185,129,0.22),transparent_34%),linear-gradient(135deg,rgba(2,6,23,0.94),rgba(24,24,27,0.88)_45%,rgba(30,41,59,0.95))] backdrop-blur-xl"
        aria-hidden="true"
      />
      <div className="absolute -left-24 top-10 h-64 w-64 rounded-full bg-sky-400/20 blur-3xl" aria-hidden="true" />
      <div className="absolute -right-16 top-1/3 h-72 w-72 rounded-full bg-fuchsia-500/20 blur-3xl" aria-hidden="true" />
      <div className="absolute bottom-0 left-1/3 h-64 w-64 rounded-full bg-emerald-400/15 blur-3xl" aria-hidden="true" />

      {/* Card */}
      <div
        className={cn(
          "relative w-full max-w-[840px] rounded-[1.35rem] border border-white/15 bg-background/88 font-sans shadow-[0_28px_90px_rgba(2,6,23,0.55)] backdrop-blur-2xl flex flex-col overflow-hidden",
          "max-h-[calc(100vh-80px)]",
          "before:absolute before:inset-x-0 before:top-0 before:h-px before:bg-gradient-to-r before:from-transparent before:via-white/50 before:to-transparent",
          "animate-in fade-in-0 zoom-in-98 duration-300"
        )}
      >
        <div className="absolute inset-x-0 top-0 h-40 bg-[radial-gradient(circle_at_18%_0%,rgba(14,165,233,0.18),transparent_38%),radial-gradient(circle_at_82%_0%,rgba(168,85,247,0.16),transparent_34%)]" aria-hidden="true" />

        {/* Top bar: progress + sign in */}
        <div className="relative flex items-center justify-between border-b border-white/10 bg-white/[0.035] px-6 py-4">
          <div className="flex-1">
            <ProgressBar steps={steps} currentStepIndex={safeStepIndex} />
          </div>
          {showSignIn && (
            <a
              href="/auth"
              className="ml-4 rounded-full border border-white/10 bg-white/[0.04] px-3 py-1.5 text-xs text-muted-foreground transition-colors hover:border-sky-400/40 hover:text-foreground whitespace-nowrap"
            >
              Sign in
            </a>
          )}
        </div>

        {/* Step content */}
        <div className="relative flex-1 overflow-y-auto bg-[linear-gradient(180deg,rgba(255,255,255,0.035),rgba(255,255,255,0)_28%),hsl(var(--surface-modal))]">
          <div className="px-8 py-8">
            <StepComponent
              plan={plan}
              updatePlan={updatePlan}
              onNext={nextStep}
              onBack={prevStep}
            />
          </div>
        </div>

        {/* Footer navigation */}
        <div className="relative flex items-center justify-between border-t border-white/10 bg-background/80 px-6 py-4 backdrop-blur">
          <div>
            {!isFirst && (
              <button
                type="button"
                onClick={prevStep}
                className="flex items-center gap-1 rounded-lg border border-white/10 bg-white/[0.03] px-4 py-2 text-sm transition-colors hover:border-sky-400/40 hover:bg-sky-400/10"
              >
                <ChevronLeft className="w-4 h-4" />
                Back
              </button>
            )}
          </div>
          <div />
        </div>
      </div>
    </div>
  );

  return createPortal(overlay, document.body);
}