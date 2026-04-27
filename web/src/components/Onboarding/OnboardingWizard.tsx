/**
 * Onboarding Wizard
 *
 * Main orchestrator component that manages the onboarding flow.
 * Renders the appropriate step component based on current state.
 *
 * 3-phase flow:
 * 1. Tour not completed + wizard active: Show guided spotlight tour steps (9 steps)
 * 2. Tour completed + checklist not dismissed: Show floating OnboardingChecklist
 * 3. Tour completed + checklist dismissed: Render nothing
 *
 * 9-step tour:
 * 1. Welcome (modal) - value pitch
 * 2. Chat & Sidebars (multi-spotlight) - development environment overview
 * 3. Workspaces (spotlight) - isolated branches
 * 4. Workflow Intro (spotlight) - what workflows are
 * 5. Workflow Hub (spotlight) - browse templates
 * 6. Workflow Builder (spotlight) - visual builder canvas
 * 7. Workflow Builder Chat (spotlight) - builder AI assistant
 * 8. Presets & Params (spotlight) - customization
 * 9. Completion (modal) - quick-start actions
 */

import { useEffect } from "react";
import { Workflow, Sparkles, Settings2 } from "lucide-react";
import { useOnboardingChecklistStore } from "../../store/onboardingChecklistStore";
import { useViewerStore } from "../../store/viewerStore";
import { useWorkspaceStateStore } from "../../store/workspaceStateStore";
import { useProjectStore } from "../../store/projectStore";
import { useOnboardingFlowStore } from "../OnboardingFlow/onboardingStore";
import {
  ONBOARDING_STEPS,
  getStepById,
  getNextStepId,
  stepRequiresChatMode,
  stepRequiresSettingsMode,
  stepRequiresWorkflowMode,
} from "./constants";
import { OnboardingSpotlight } from "./OnboardingSpotlight";
import { OnboardingMultiSpotlight } from "./OnboardingMultiSpotlight";
import { OnboardingModal } from "./OnboardingModal";
import { OnboardingNavBar } from "./OnboardingNavBar";
import { OnboardingChecklist } from "./OnboardingChecklist";
import type { OnboardingStepId, StepProps } from "./types";
import { WelcomeStep, CompletionStep } from "./steps";

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

  // Navigate to workflow mode when this step mounts
  useEffect(() => {
    useViewerStore.getState().setWorkflowMode(true);
  }, []);

  // When going back from this step, exit workflow mode
  const handleBack = props.onBack
    ? () => {
        useViewerStore.getState().setWorkflowMode(false);
        props.onBack!();
      }
    : undefined;

  return (
    <OnboardingSpotlight
      targetSelector={step.targetSelector!}
      title={step.title}
      description={
        <div className="space-y-2">
          <p>{step.description}</p>
          <div className="text-xs text-muted-foreground/80 space-y-1">
            <p className="flex items-center gap-1.5">
              <span className="w-1.5 h-1.5 rounded-full bg-blue-400 flex-shrink-0" />
              <strong>Agent</strong> — General-purpose coding assistant
            </p>
            <p className="flex items-center gap-1.5">
              <span className="w-1.5 h-1.5 rounded-full bg-blue-400 flex-shrink-0" />
              <strong>Checklist</strong> — Full dev pipeline with planning, TDD, review
            </p>
            <p className="flex items-center gap-1.5">
              <span className="w-1.5 h-1.5 rounded-full bg-blue-400 flex-shrink-0" />
              <strong>Auditing Agent</strong> — Agent with per-turn audit oversight
            </p>
          </div>
        </div>
      }
      stepNumber={props.stepNumber}
      totalSteps={props.totalSteps}
      onNext={props.onComplete}
      onBack={handleBack}
      onSkipAll={() => {
        useViewerStore.getState().setWorkflowMode(false);
        props.onSkipAll();
      }}
      tooltipPosition="top"
      tooltipPadding={80}
      spotlightConfig={step.spotlightConfig}
    />
  );
}

function WorkflowBuilderStep(props: StepProps) {
  const step = getStepById("workflow-builder")!;

  useEffect(() => {
    useViewerStore.getState().setWorkflowMode(true, "__new__");
  }, []);

  const handleBack = props.onBack
    ? () => {
        useViewerStore.getState().setWorkflowMode(true, "__hub__");
        props.onBack!();
      }
    : undefined;

  return (
    <OnboardingSpotlight
      targetSelector={step.targetSelector!}
      title={step.title}
      description={step.description}
      stepNumber={props.stepNumber}
      totalSteps={props.totalSteps}
      onNext={props.onComplete}
      onBack={handleBack}
      onSkipAll={props.onSkipAll}
      tooltipPosition="top"
      tooltipPadding={80}
      spotlightConfig={step.spotlightConfig}
    />
  );
}

function WorkflowBuilderChatStep(props: StepProps) {
  const step = getStepById("workflow-builder-chat")!;

  useEffect(() => {
    const vs = useViewerStore.getState();
    if (!vs.isWorkflowMode) {
      vs.setWorkflowMode(true, "__new__");
    }
  }, []);

  // When completing, exit workflow mode
  const handleComplete = () => {
    useViewerStore.getState().setWorkflowMode(false);
    props.onComplete();
  };

  const handleSkipAll = () => {
    useViewerStore.getState().setWorkflowMode(false);
    props.onSkipAll();
  };

  return (
    <OnboardingSpotlight
      targetSelector={step.targetSelector!}
      title={step.title}
      description={step.description}
      stepNumber={props.stepNumber}
      totalSteps={props.totalSteps}
      onNext={handleComplete}
      onBack={props.onBack}
      onSkipAll={handleSkipAll}
      tooltipPosition="left"
      spotlightConfig={step.spotlightConfig}
    />
  );
}

function PresetsAndParamsStep({ onComplete: _onComplete, stepNumber, totalSteps }: StepProps) {
  return (
    <OnboardingModal
      isOpen={true}
      title="Presets & Parameters"
      stepNumber={stepNumber}
      totalSteps={totalSteps}
      hideNavigation
      hideProgressBar
    >
      <div className="space-y-4">
        <p className="text-[15px] leading-relaxed text-muted-foreground">
          Customize how your AI agents work with presets and parameters,
          found at the bottom of the chat input.
        </p>

        <div className="grid gap-2">
          <div className="flex items-start gap-3 p-3 rounded-lg bg-muted/50 border-l-2 border-primary/30">
            <div className="flex-shrink-0 p-1.5 rounded-lg bg-primary/15 text-primary">
              <Sparkles className="w-4 h-4" />
            </div>
            <div className="min-w-0">
              <h4 className="text-sm font-medium text-foreground">Presets</h4>
              <p className="text-xs text-muted-foreground">
                Choose agent roles — researcher, debugger, planner, and more
              </p>
            </div>
          </div>
          <div className="flex items-start gap-3 p-3 rounded-lg bg-muted/50 border-l-2 border-primary/30">
            <div className="flex-shrink-0 p-1.5 rounded-lg bg-primary/15 text-primary">
              <Settings2 className="w-4 h-4" />
            </div>
            <div className="min-w-0">
              <h4 className="text-sm font-medium text-foreground">Parameters</h4>
              <p className="text-xs text-muted-foreground">
                Workflow settings defined in YAML — model, tools, thinking level, and more
              </p>
            </div>
          </div>
        </div>

        <div className="flex items-center gap-2 p-2 rounded-lg bg-primary/10 border border-primary/20">
          <Settings2 className="w-4 h-4 text-primary flex-shrink-0" />
          <p className="text-xs text-foreground">
            All parameters come from the workflow's YAML — authors control what users can configure.
          </p>
        </div>
      </div>
    </OnboardingModal>
  );
}

// ─── Step Component Map ──────────────────────────────────────────────────────

const STEP_COMPONENTS: Record<OnboardingStepId, React.ComponentType<StepProps>> = {
  "welcome": WelcomeStep,
  "chat-and-sidebars": ChatAndSidebarsStep,
  "workspaces": WorkspacesStep,
  "workflow-intro": WorkflowIntroStep,
  "workflow-hub": WorkflowHubStep,
  "workflow-builder": WorkflowBuilderStep,
  "workflow-builder-chat": WorkflowBuilderChatStep,
  "presets-and-params": PresetsAndParamsStep,
  "completion": CompletionStep,
};

// ─── Main Wizard Component ───────────────────────────────────────────────────

export function OnboardingWizard() {
  // Get everything from our unified store
  const {
    isWizardActive,
    currentStepId,
    isInitialized,
    loadState,
    completeStep,
    skipAll,
    nextStep,
    previousStep,
    hasCompletedOnboarding,
    startWizard,
    panelState,
    detectCompletedItems,
    subscribeToStoreChanges,
  } = useOnboardingChecklistStore();

  const currentProject = useProjectStore((s) => s.currentProject);
  const isWorkflowMode = useViewerStore((s) => s.isWorkflowMode);
  const isSettingsMode = useViewerStore((s) => s.isSettingsMode);
  const onboardingFlowState = useOnboardingFlowStore((s) => s.state);

  // Load state on mount
  useEffect(() => {
    if (!isInitialized) {
      loadState();
    }
  }, [isInitialized, loadState]);

  // Auto-start wizard for first-time users after project loads.
  // Defer if the new onboarding flow is still active (not_started or in_progress).
  useEffect(() => {
    if (isInitialized && !hasCompletedOnboarding && !isWizardActive && currentProject) {
      if (onboardingFlowState === "not_started" || onboardingFlowState === "in_progress") {
        return; // New onboarding overlay is active — defer spotlight tour
      }
      startWizard();
    }
  }, [isInitialized, hasCompletedOnboarding, isWizardActive, startWizard, currentProject, onboardingFlowState]);

  // Handle view context based on current step
  useEffect(() => {
    if (!currentStepId || !isWizardActive) return;

    const viewerStore = useViewerStore.getState();

    // If we're on a chat step, ensure we're NOT in workflow/settings mode
    if (stepRequiresChatMode(currentStepId)) {
      if (viewerStore.isWorkflowMode) {
        viewerStore.setWorkflowMode(false);
      }
      if (viewerStore.isSettingsMode) {
        viewerStore.setSettingsMode(false);
      }
    }

    // If we're on a settings step, ensure we're NOT in workflow mode
    if (stepRequiresSettingsMode(currentStepId)) {
      if (viewerStore.isWorkflowMode) {
        viewerStore.setWorkflowMode(false);
      }
    }
  }, [currentStepId, isWizardActive]);

  // Ensure workflow builder steps have the correct workflow mode
  useEffect(() => {
    if (!currentStepId) return;
    if (currentStepId === "workflow-builder" || currentStepId === "workflow-builder-chat") {
      useViewerStore.getState().setWorkflowMode(true, "__new__");
    }
  }, [currentStepId]);

  // Set up checklist detection after tour completes
  useEffect(() => {
    if (!isInitialized || !hasCompletedOnboarding) return;
    detectCompletedItems();
    const unsub = subscribeToStoreChanges();
    return unsub;
  }, [isInitialized, hasCompletedOnboarding, detectCompletedItems, subscribeToStoreChanges]);

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
      // Clean up view mode before transitioning — the NavBar calls this
      // directly (bypassing step-specific wrappers), so we must handle
      // mode exits here at the wizard level.
      const nextId = getNextStepId(currentStepId);
      if (nextId && !stepRequiresWorkflowMode(nextId)) {
        const vs = useViewerStore.getState();
        if (vs.isWorkflowMode) vs.setWorkflowMode(false);
      }
      if (nextId && !stepRequiresSettingsMode(nextId)) {
        const vs = useViewerStore.getState();
        if (vs.isSettingsMode) vs.setSettingsMode(false);
      }
      await completeStep(currentStepId);
      nextStep();
    };

    const handleSkipAll = async () => {
      // Always clean up view modes when skipping
      const vs = useViewerStore.getState();
      if (vs.isWorkflowMode) vs.setWorkflowMode(false);
      if (vs.isSettingsMode) vs.setSettingsMode(false);
      await skipAll();
    };

    const handleBack = stepIndex > 0 ? previousStep : undefined;

    const isFirstStep = stepIndex === 0;
    const isLastStep = stepIndex === ONBOARDING_STEPS.length - 1;
    const nextLabel = isFirstStep ? "Get Started" : isLastStep ? "Finish" : "Next";

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

  // Phase 2: Tour done, show checklist if not dismissed (only on main chat page)
  if (hasCompletedOnboarding && panelState !== "dismissed" && !isWorkflowMode && !isSettingsMode) {
    return <OnboardingChecklist />;
  }

  // Phase 3: Everything done
  return null;
}