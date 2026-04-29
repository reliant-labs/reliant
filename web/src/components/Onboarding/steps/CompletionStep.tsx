/**
 * Completion Step
 *
 * Final step — the critical handoff moment.
 * Shows one clear CTA, secondary links, and a subtle tip.
 */

import { Search, Code2 } from "lucide-react";
import { OnboardingModal } from "../OnboardingModal";
import { useOnboardingChecklistStore } from "../../../store/onboardingChecklistStore";
import { useViewerStore } from "../../../store/viewerStore";
import { useChatStore } from "../../../store/chatStore";
import { useWorktreeStore } from "../../../store/worktreeStore";
import { useChatParamsStore } from "../../../store/chatParamsStore";
import { useAttachmentStore } from "../../../store/attachmentStore";
import type { StepProps } from "../types";

export function CompletionStep({
  onComplete,
  stepNumber,
  totalSteps,
}: StepProps) {
  const projectHasCode = useOnboardingChecklistStore((s) => s.projectHasCode);

  const handleQuickStart = async (prompt: string) => {
    // Complete onboarding first
    onComplete();
    // Small delay to let onboarding close
    setTimeout(async () => {
      try {
        const worktreeId = useWorktreeStore.getState().currentWorktree?.id;
        if (!worktreeId) return;
        const chat = await useChatStore.getState().createChat(worktreeId, prompt);
        useChatParamsStore.getState().transferTempToChat(chat.id);
        useChatStore.getState().selectChat(chat);
        useAttachmentStore.getState().clearAttachments("temp");
      } catch (error) {
        console.error("Failed to create quick-start chat:", error);
      }
    }, 300);
  };

  return (
    <OnboardingModal
      isOpen={true}
      title="Ready to go"
      stepNumber={stepNumber}
      totalSteps={totalSteps}
      hideNavigation
      hideProgressBar
    >
      <div className="space-y-5">
        {/* Primary CTA */}
        <div className="text-center">
          <p className="text-sm text-muted-foreground mb-4">
            {projectHasCode !== false
              ? "We detected code in your project. Let's dive in."
              : "Start building something new."}
          </p>
          <button
            type="button"
            onClick={() =>
              handleQuickStart(
                projectHasCode !== false
                  ? "Search for refactoring opportunities in this codebase"
                  : "Write me a Python hello world HTTP server"
              )
            }
            className="inline-flex items-center gap-2 px-6 py-3 text-sm font-medium bg-primary text-primary-foreground rounded-lg hover:bg-primary/90 transition-colors"
          >
            {projectHasCode !== false ? (
              <>
                <Search className="w-4 h-4" />
                Explore your codebase
              </>
            ) : (
              <>
                <Code2 className="w-4 h-4" />
                Create something new
              </>
            )}
          </button>
        </div>

        {/* Divider */}
        <div className="border-t border-border/40" />

        {/* Secondary actions as text links */}
        <div className="flex flex-col items-center gap-2 text-sm">
          <button
            type="button"
            onClick={() => {
              onComplete();
              setTimeout(() => {
                useViewerStore.getState().setWorkflowMode(true);
              }, 300);
            }}
            className="text-muted-foreground hover:text-primary transition-colors"
          >
            Browse workflow templates
          </button>
          <a
            href="https://docs.reliantlabs.io/"
            target="_blank"
            rel="noopener noreferrer"
            className="text-muted-foreground hover:text-primary transition-colors"
          >
            Read the docs
          </a>
          <a
            href="https://cal.com/team/reliant/onboarding"
            target="_blank"
            rel="noopener noreferrer"
            className="text-muted-foreground hover:text-primary transition-colors"
          >
            Book a walkthrough
          </a>
        </div>

        {/* Preset tip */}
        <p className="text-xs text-muted-foreground/60 text-center">
          Tip: Use presets to choose agent roles like researcher, debugger, or
          planner
        </p>

        {/* Restart hint */}
        <p className="text-xs text-muted-foreground/60 text-center">
          Restart this tour anytime from Settings → About
        </p>
      </div>
    </OnboardingModal>
  );
}