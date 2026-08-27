/**
 * Onboarding Wizard
 *
 * Main orchestrator component that manages the onboarding flow.
 * Renders the appropriate step component based on current state.
 *
 * The wizard is fully URL-decoupled: the active step lives in the URL as
 * `?tour=<step-id>`. The wizard derives `currentStepId` and `isWizardActive`
 * via `useTourNavigation()`. Steps that need to spotlight an element on a
 * specific page check `pathname` and either render the spotlight (right page)
 * or a small "Open <page> to continue" modal (wrong page). The user owns the
 * URL; the wizard reacts. There are no useEffects that push routes.
 *
 * 3-phase flow:
 * 1. Tour active (`?tour=<step>` present): Show guided spotlight tour steps (8 steps)
 * 2. Tour completed + checklist not dismissed: Show floating OnboardingChecklist
 * 3. Tour completed + checklist dismissed: Render nothing
 */

import { useEffect, useRef } from "react";
import { useLocation } from "@tanstack/react-router";
import { Workflow } from "lucide-react";
import { useAuthStore } from "../../store/authStore";
import { useOnboardingChecklistStore } from "../../store/onboardingChecklistStore";
import { useTourStore } from "../../store/tourStore";
import { useWorkspaceStateStore } from "../../store/workspaceStateStore";
import { trackEvent } from "../../lib/analytics";
import { useSurface } from "../../lib/surfaceContext";

import {
  ONBOARDING_STEPS,
  getStepById,
} from "./constants";
import { OnboardingSpotlight } from "./OnboardingSpotlight";
import { OnboardingMultiSpotlight } from "./OnboardingMultiSpotlight";
import { OnboardingModal } from "./OnboardingModal";
import { OnboardingNavBar } from "./OnboardingNavBar";
import { OnboardingChecklist } from "./OnboardingChecklist";
import type { OnboardingStepId, StepProps } from "./types";
import { CompletionStep } from "./steps";
import { useTourNavigation, STEP_EXPECTED_PATH } from "./useTourNavigation";

// ─── Page-redirect modal ─────────────────────────────────────────────────────
// Rendered when a spotlight step's expected page is not the current page.
// Keeps the user in control of navigation — single button takes them to the
// right place, Skip/Back still available via the OnboardingNavBar.

interface OpenPageModalProps extends StepProps {
  title: string;
  message: string;
  actionLabel: string;
  onOpen: () => void;
}

function OpenPageModal({
  title,
  message,
  actionLabel,
  onOpen,
  stepNumber,
  totalSteps,
}: OpenPageModalProps) {
  return (
    <OnboardingModal
      isOpen={true}
      title={title}
      stepNumber={stepNumber}
      totalSteps={totalSteps}
      hideNavigation
      hideProgressBar
    >
      <div className="space-y-5">
        <p className="text-sm text-muted-foreground text-center">{message}</p>
        <div className="flex justify-center">
          <button
            type="button"
            onClick={onOpen}
            className="inline-flex items-center gap-2 px-5 py-2.5 text-sm font-medium bg-primary text-primary-foreground rounded-lg hover:bg-primary/90 transition-colors"
          >
            {actionLabel}
          </button>
        </div>
      </div>
    </OnboardingModal>
  );
}

// ─── Spotlight Step Wrappers ─────────────────────────────────────────────────

function ChatAndSidebarsStep(props: StepProps) {
  const step = getStepById("chat-and-sidebars")!;

  // Expand left sidebar so user can see it
  useEffect(() => {
    useWorkspaceStateStore.getState().setLeftSidebarExpandedGlobal(true);
  }, []);

  return (
    <OnboardingMultiSpotlight
      targets={step.targets || []}
      stepNumber={props.stepNumber}
      totalSteps={props.totalSteps}
      onNext={props.onComplete}
      onBack={props.onBack}
      onSkipAll={props.onSkipAll}
    />
  );
}

function WorkspacesStep(props: StepProps) {
  const step = getStepById("workspaces")!;
  return (
    <OnboardingSpotlight
      targetSelector={step.targetSelector!}
      title={step.title}
      description={
        <div className="space-y-2">
          <p>{step.description}</p>
          <p className="text-xs text-muted-foreground/80">
            Use the dropdown to switch workspaces, or create a new one to start an isolated feature branch.
          </p>
        </div>
      }
      stepNumber={props.stepNumber}
      totalSteps={props.totalSteps}
      onNext={props.onComplete}
      onBack={props.onBack}
      onSkipAll={props.onSkipAll}
      spotlightConfig={step.spotlightConfig}
    />
  );
}

function WorkflowIntroStep(props: StepProps) {
  const step = getStepById("workflow-intro")!;

  useEffect(() => {
    useWorkspaceStateStore.getState().setLeftSidebarExpandedGlobal(true);
  }, []);

  return (
    <OnboardingSpotlight
      targetSelector={step.targetSelector!}
      title={step.title}
      description={
        <div className="space-y-2">
          <p>{step.description}</p>
          <div className="flex items-start gap-2 p-2 rounded bg-primary/5 border border-primary/10">
            <Workflow className="w-4 h-4 text-primary flex-shrink-0 mt-0.5" />
            <p className="text-xs text-muted-foreground">
              Workflows are defined in YAML and run multi-agent pipelines with loops, branches, parallelization, and human-in-the-loop approvals.
            </p>
          </div>
        </div>
      }
      stepNumber={props.stepNumber}
      totalSteps={props.totalSteps}
      onNext={props.onComplete}
      onBack={props.onBack}
      onSkipAll={props.onSkipAll}
      spotlightConfig={step.spotlightConfig}
    />
  );
}

function WorkflowHubStep(props: StepProps) {
  const step = getStepById("workflow-hub")!;
  const { pathname } = useLocation();
  const { goToStep } = useTourNavigation();
  const expected = STEP_EXPECTED_PATH("workflow-hub")!;

  // Wrong page → render a modal asking the user to open Workflows. We never
  // push routes from an effect — the user clicks the button.
  if (!pathname.startsWith(expected)) {
    return (
      <OpenPageModal
        {...props}
        title={step.title}
        message="Open Workflows to continue this step."
        actionLabel="Open Workflows"
        onOpen={() => goToStep("workflow-hub")}
      />
    );
  }

  return (
    <OnboardingSpotlight
      targetSelector={step.targetSelector!}
      title={step.title}
      description={
        <div className="space-y-2">
          <p>{step.description}</p>
          <div className="text-xs text-muted-foreground/80 space-y-1">
            <p className="flex items-center gap-1.5">
              <span className="w-1.5 h-1.5 rounded-full bg-primary flex-shrink-0" />
              <strong>Agent</strong> — General-purpose coding assistant
            </p>
            <p className="flex items-center gap-1.5">
              <span className="w-1.5 h-1.5 rounded-full bg-primary flex-shrink-0" />
              <strong>Checklist</strong> — Full dev pipeline with planning, TDD, review
            </p>
            <p className="flex items-center gap-1.5">
              <span className="w-1.5 h-1.5 rounded-full bg-primary flex-shrink-0" />
              <strong>Auditing Agent</strong> — Agent with per-turn audit oversight
            </p>
          </div>
        </div>
      }
      stepNumber={props.stepNumber}
      totalSteps={props.totalSteps}
      onNext={props.onComplete}
      onBack={props.onBack}
      onSkipAll={props.onSkipAll}
      tooltipPosition="top"
      tooltipPadding={80}
      spotlightConfig={step.spotlightConfig}
    />
  );
}

function WorkflowBuilderStep(props: StepProps) {
  const step = getStepById("workflow-builder")!;
  const { pathname } = useLocation();
  const { goToStep } = useTourNavigation();
  const expected = STEP_EXPECTED_PATH("workflow-builder")!;

  // The builder needs `/workflow/<name>`; the hub alone doesn't satisfy this.
  if (!pathname.startsWith(expected)) {
    return (
      <OpenPageModal
        {...props}
        title={step.title}
        message="Open the workflow builder to continue this step."
        actionLabel="Open Workflow Builder"
        onOpen={() => goToStep("workflow-builder")}
      />
    );
  }

  return (
    <OnboardingSpotlight
      targetSelector={step.targetSelector!}
      title={step.title}
      description={step.description}
      stepNumber={props.stepNumber}
      totalSteps={props.totalSteps}
      onNext={props.onComplete}
      onBack={props.onBack}
      onSkipAll={props.onSkipAll}
      tooltipPosition="top"
      tooltipPadding={80}
      spotlightConfig={step.spotlightConfig}
    />
  );
}

function WorkflowBuilderChatStep(props: StepProps) {
  const step = getStepById("workflow-builder-chat")!;
  const { pathname } = useLocation();
  const { goToStep } = useTourNavigation();
  const expected = STEP_EXPECTED_PATH("workflow-builder-chat")!;

  if (!pathname.startsWith(expected)) {
    return (
      <OpenPageModal
        {...props}
        title={step.title}
        message="Open the workflow builder to continue this step."
        actionLabel="Open Workflow Builder"
        onOpen={() => goToStep("workflow-builder-chat")}
      />
    );
  }

  return (
    <OnboardingSpotlight
      targetSelector={step.targetSelector!}
      title={step.title}
      description={step.description}
      stepNumber={props.stepNumber}
      totalSteps={props.totalSteps}
      onNext={props.onComplete}
      onBack={props.onBack}
      onSkipAll={props.onSkipAll}
      tooltipPosition="left"
      spotlightConfig={step.spotlightConfig}
    />
  );
}

// ─── Step Component Map ──────────────────────────────────────────────────────

const STEP_COMPONENTS: Record<OnboardingStepId, React.ComponentType<StepProps>> = {
  "chat-and-sidebars": ChatAndSidebarsStep,
  "workspaces": WorkspacesStep,
  "workflow-intro": WorkflowIntroStep,
  "workflow-hub": WorkflowHubStep,
  "workflow-builder": WorkflowBuilderStep,
  "workflow-builder-chat": WorkflowBuilderChatStep,
  "completion": CompletionStep,
};

// ─── Main Wizard Component ───────────────────────────────────────────────────

export function OnboardingWizard() {
  const surface = useSurface();
  const {
    currentStepId,
    isWizardActive,
    completeAndAdvance,
    goBack,
    skipAll,
  } = useTourNavigation();

  const tourInitialized = useTourStore((state) => state.isInitialized);
  const loadTourState = useTourStore((state) => state.loadState);

  const {
    isInitialized: checklistInitialized,
    loadState: loadChecklistState,
    panelState,
    allRequiredComplete,
    detectCompletedItems,
    subscribeToStoreChanges,
  } = useOnboardingChecklistStore();

  const isInitialized = tourInitialized && checklistInitialized;

  const location = useLocation();
  const isSettingsMode = location.pathname.startsWith("/settings");
  const isWorkflowMode = location.pathname.startsWith("/workflow");
  // The dedicated /onboarding route IS the onboarding experience — showing
  // the post-onboarding "Setup Guide" floating panel on top of it is a
  // bad-UX dupe of the same information. Phase 2 (the checklist) is for
  // AFTER /onboarding exits to the main chat; while the user is on
  // /onboarding itself we suppress the floater entirely.
  const isOnboardingRoute = location.pathname.startsWith("/onboarding");

  // Onboarding is per-USER state, so none of it may render to a signed-out
  // visitor. Route alone is not that gate: the sign-in screen is reachable
  // at more than one path, and a forced sign-out (see the 401 handler in
  // api/transport.ts) drops the user there without unmounting this wizard —
  // which is how the Setup Guide ended up painted over the login screen.
  const user = useAuthStore((state) => state.user);

  // NOTE: the wizard used to defer itself while NewChatView's blocking
  // "What are you building?" dialog could be on screen, which meant waiting
  // on the chat-list query before the tour could start — a visible lag for
  // exactly the brand-new user the tour is for. That dialog is gone (the
  // starter cards render inline now and never paint over a spotlight), so
  // there is nothing left to defer for.

  // Load state on mount. Pure data fetch — no navigation.
  useEffect(() => {
    if (!tourInitialized) {
      void loadTourState();
    }
    if (!checklistInitialized) {
      void loadChecklistState();
    }
  }, [tourInitialized, checklistInitialized, loadTourState, loadChecklistState]);

  // tour_started fires once when the wizard becomes active. tourStartRef holds
  // the wall-clock start so tour_completed / tour_step_skipped can report a
  // full-tour duration. stepStartRef tracks the current step's view time so
  // tour_step_completed / tour_step_skipped can report per-step duration.
  const tourStartRef = useRef<number | null>(null);
  const stepStartRef = useRef<number | null>(null);
  useEffect(() => {
    if (isWizardActive && currentStepId) {
      tourStartRef.current = Date.now();
      stepStartRef.current = Date.now();
      trackEvent("tour_started", { totalSteps: ONBOARDING_STEPS.length });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isWizardActive]);

  // tour_step_viewed fires on every step transition (including the first).
  // stepStartRef resets so subsequent step_completed / step_skipped events get
  // a fresh per-step duration.
  useEffect(() => {
    if (!isWizardActive || !currentStepId) return;
    stepStartRef.current = Date.now();
    trackEvent("tour_step_viewed", { stepId: currentStepId });
  }, [isWizardActive, currentStepId]);

  // Set up checklist detection after checklist state is ready
  useEffect(() => {
    if (!checklistInitialized) return;
    void detectCompletedItems();
    const unsub = subscribeToStoreChanges();
    return unsub;
  }, [checklistInitialized, detectCompletedItems, subscribeToStoreChanges]);

  // The guided tour spotlights desktop chrome by DOM selector
  // (`[data-onboarding='left-sidebar']`, `'right-sidebar'`,
  // `'workflow-canvas'`). None of that exists on the mobile surface, so every
  // step would fall back to its "Open <page> to continue" prompt pointing at
  // pages mobile does not have. The tour stays a desktop feature; mobile users
  // still get the *setup* flow at /onboarding, which is a different system.
  if (surface !== "desktop") return null;

  // Signed out — neither the tour nor the checklist belongs on screen.
  if (!user) return null;

  // Not ready yet
  if (!isInitialized) return null;

  // Phase 1: Guided tour is active
  if (isWizardActive && currentStepId) {
    const currentStep = getStepById(currentStepId);
    if (!currentStep) return null;

    const stepIndex = ONBOARDING_STEPS.findIndex((s) => s.id === currentStepId);
    const StepComponent = STEP_COMPONENTS[currentStepId];
    if (!StepComponent) return null;

    const handleComplete = async () => {
      const stepDuration = stepStartRef.current
        ? Date.now() - stepStartRef.current
        : 0;
      trackEvent("tour_step_completed", {
        stepId: currentStepId,
        stepName: currentStep.title,
        stepsCompleted: stepIndex + 1,
        totalSteps: ONBOARDING_STEPS.length,
        duration_ms: stepDuration,
      });
      if (stepIndex === ONBOARDING_STEPS.length - 1 && tourStartRef.current) {
        trackEvent("tour_completed", {
          totalSteps: ONBOARDING_STEPS.length,
          duration_ms: Date.now() - tourStartRef.current,
        });
        tourStartRef.current = null;
      }
      await completeAndAdvance();
    };

    const handleSkipAll = async () => {
      const stepDuration = stepStartRef.current
        ? Date.now() - stepStartRef.current
        : 0;
      const tourDuration = tourStartRef.current
        ? Date.now() - tourStartRef.current
        : 0;
      trackEvent("tour_step_skipped", {
        stepId: currentStepId,
        stepName: currentStep.title,
        stepsCompleted: stepIndex,
        totalSteps: ONBOARDING_STEPS.length,
        duration_ms: stepDuration,
        tour_duration_ms: tourDuration,
      });
      tourStartRef.current = null;
      await skipAll();
    };

    const handleBack = stepIndex > 0 ? goBack : undefined;

    const isLastStep = stepIndex === ONBOARDING_STEPS.length - 1;
    const nextLabel = isLastStep ? "Finish" : "Next";

    return (
      <>
        <StepComponent
          onComplete={handleComplete}
          onSkipAll={handleSkipAll}
          onBack={handleBack}
          stepNumber={stepIndex + 1}
          totalSteps={ONBOARDING_STEPS.length}
        />
        <OnboardingNavBar
          stepNumber={stepIndex + 1}
          totalSteps={ONBOARDING_STEPS.length}
          stepTitle={currentStep.title}
          onNext={handleComplete}
          onBack={handleBack}
          onSkipAll={handleSkipAll}
          nextLabel={nextLabel}
          showSkipAll={!isLastStep}
        />
      </>
    );
  }

  // Phase 2: Show checklist after setup if not dismissed (only on main chat page).
  // isOnboardingRoute gate: the /onboarding route is the onboarding UX itself —
  // surfacing the post-onboarding floater on top of it is a misleading dupe.
  // allRequiredComplete gate: a guide with nothing left to do is clutter, so it
  // retires itself rather than sitting at full progress forever.
  if (
    panelState !== "dismissed" &&
    !allRequiredComplete() &&
    !isWorkflowMode &&
    !isSettingsMode &&
    !isOnboardingRoute
  ) {
    return <OnboardingChecklist />;
  }

  // Phase 3: Everything done
  return null;
}