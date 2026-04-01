/**
 * Onboarding Navigation Bar
 *
 * A floating navigation bar that stays fixed at the bottom of the screen
 * during spotlight steps. Provides consistent navigation controls without
 * requiring users to track moving tooltips.
 */

import { createPortal } from "react-dom";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { cn } from "../../lib/utils";

interface OnboardingNavBarProps {
  stepNumber: number;
  totalSteps: number;
  onNext: () => void;
  onBack?: () => void;
  onSkipAll: () => void;
  nextLabel?: string;
  showSkipAll?: boolean;
  isVisible?: boolean;
  stepTitle?: string;
}

export function OnboardingNavBar({
  stepNumber,
  totalSteps,
  onNext,
  onBack,
  onSkipAll,
  nextLabel = "Next",
  showSkipAll = true,
  isVisible = true,
  stepTitle,
}: OnboardingNavBarProps) {
  const progress = (stepNumber / totalSteps) * 100;

  const navContent = (
    <div
      className={cn(
        "fixed bottom-24 left-1/2 -translate-x-1/2 z-[101]",
        "flex items-center gap-4 px-6 py-3.5",
        "bg-zinc-900 dark:bg-zinc-800 text-white rounded-xl",
        "shadow-[0_8px_32px_rgba(0,0,0,0.4)]",
        "transition-all duration-300",
        isVisible ? "opacity-100 translate-y-0" : "opacity-0 translate-y-4 pointer-events-none"
      )}
    >
      {/* Progress indicator */}
      <div className="flex items-center gap-3 pr-4 border-r border-zinc-700">
        <div className="w-28 h-1.5 bg-zinc-700 rounded-full overflow-hidden">
          <div
            className="h-full bg-white transition-all duration-300 ease-out rounded-full"
            style={{ width: `${progress}%` }}
          />
        </div>
        <span className="text-xs text-zinc-400 whitespace-nowrap font-medium">
          {stepNumber} / {totalSteps}
        </span>
      </div>

      {/* Step title */}
      {stepTitle && (
        <span className="text-sm text-zinc-300 font-medium px-2 max-w-48 truncate">
          {stepTitle}
        </span>
      )}

      {/* Navigation buttons */}
      <div className="flex items-center gap-2">
        {showSkipAll && (
          <button
            onClick={onSkipAll}
            className="px-3 py-1.5 text-sm text-zinc-400 hover:text-white transition-colors"
          >
            Skip Tutorial
          </button>
        )}

        {onBack && (
          <button
            onClick={onBack}
            className="flex items-center gap-1 px-3 py-1.5 text-sm text-zinc-300 border border-zinc-600 rounded-lg hover:bg-zinc-700 transition-colors"
          >
            <ChevronLeft className="w-4 h-4" />
            Back
          </button>
        )}

        <button
          onClick={onNext}
          className="flex items-center gap-1 px-4 py-1.5 text-sm bg-white text-zinc-900 font-medium rounded-lg hover:bg-zinc-200 transition-colors"
        >
          {nextLabel}
          <ChevronRight className="w-4 h-4" />
        </button>
      </div>
    </div>
  );

  return createPortal(navContent, document.body);
}
