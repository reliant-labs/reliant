/**
 * Onboarding Modal
 *
 * A specialized modal component for onboarding wizard steps.
 * Includes step indicator, navigation buttons, and skip options.
 */

import { useEffect, useRef } from "react";
import { createPortal } from "react-dom";
import { X, ChevronLeft, ChevronRight } from "lucide-react";
import { cn } from "../../lib/utils";

interface OnboardingModalProps {
  isOpen: boolean;
  title: string;
  description?: string;
  stepNumber: number;
  totalSteps: number;
  children: React.ReactNode;
  onNext?: () => void;
  onBack?: () => void;
  onSkipAll?: () => void;
  onClose?: () => void;
  nextLabel?: string;
  backLabel?: string;
  showSkipAll?: boolean;
  hideNavigation?: boolean;
  className?: string;
  /** Optional icon/logo to display above the title */
  icon?: React.ReactNode;
  /** Hide the progress bar at the top */
  hideProgressBar?: boolean;
}

export function OnboardingModal({
  isOpen,
  title,
  description,
  stepNumber,
  totalSteps,
  children,
  onNext,
  onBack,
  onSkipAll,
  onClose,
  nextLabel = "Next",
  backLabel = "Back",
  showSkipAll = true,
  hideNavigation = false,
  className,
  icon,
  hideProgressBar = false,
}: OnboardingModalProps) {
  const modalRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (isOpen) {
      document.body.style.overflow = "hidden";

      const handleKeyDown = (e: KeyboardEvent) => {
        if (e.key === "Escape" && onClose) {
          onClose();
        }
      };

      document.addEventListener("keydown", handleKeyDown);

      // Focus the modal
      if (modalRef.current) {
        const focusableElements = modalRef.current.querySelectorAll(
          'button, input, select, textarea, [tabindex]:not([tabindex="-1"])'
        );
        if (focusableElements.length > 0) {
          (focusableElements[0] as HTMLElement).focus();
        }
      }

      return () => {
        document.removeEventListener("keydown", handleKeyDown);
        document.body.style.overflow = "unset";
      };
    } else {
      document.body.style.overflow = "unset";
    }
  }, [isOpen, onClose]);

  if (!isOpen) {
    return null;
  }

  const progress = (stepNumber / totalSteps) * 100;

  const modalContent = (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center p-4 sm:p-6"
      role="dialog"
      aria-modal="true"
      aria-labelledby="onboarding-title"
    >
      {/* Backdrop */}
      <div
        className="absolute inset-0 bg-black/80 backdrop-blur-xl transition-opacity"
        onClick={onClose}
        aria-hidden="true"
      />

      {/* Modal */}
      <div
        ref={modalRef}
        className={cn(
          "relative border border-border/50 rounded-xl font-sans",
          "before:absolute before:inset-0 before:rounded-xl before:bg-gradient-to-b before:from-primary/5 before:to-transparent before:pointer-events-none",
          "w-full max-w-xl max-h-[calc(100vh-100px)] overflow-hidden flex flex-col",
          "elevation-5",
          "animate-in fade-in-0 zoom-in-98 duration-300",
          className
        )}
      >
        {/* Progress bar */}
        {!hideProgressBar && (
          <div className="h-1 bg-muted">
            <div
              className="h-full bg-primary transition-all duration-300 ease-out"
              style={{ width: `${progress}%` }}
            />
          </div>
        )}

        {/* Header */}
        <div className="flex items-center justify-between px-8 py-6 border-b border-border/60 elevation-1">
          <div className="flex-1">
            {icon && (
              <div className="flex justify-center mb-3">
                {icon}
              </div>
            )}

            <h2
              id="onboarding-title"
              className={icon ? "text-2xl font-semibold text-foreground tracking-tight text-center" : "text-2xl font-semibold text-foreground tracking-tight"}
            >
              {title}
            </h2>
            {description && (
              <p className={icon ? "text-sm text-muted-foreground mt-1 text-center" : "text-sm text-muted-foreground mt-1"}>{description}</p>
            )}
          </div>
          {onClose && (
            <button
              onClick={onClose}
              className="p-2 -mr-2 hover:bg-muted/80 rounded-lg transition-colors focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 focus:ring-offset-background"
              aria-label="Close wizard"
            >
              <X className="w-5 h-5 text-muted-foreground" />
            </button>
          )}
        </div>

        {/* Content */}
        <div className="flex-1 overflow-y-auto bg-[hsl(var(--surface-modal))]">
          <div className="px-8 py-8">{children}</div>
        </div>

        {/* Footer with navigation */}
        {!hideNavigation && (
          <div className="flex items-center justify-between px-8 py-5 border-t border-border/60 bg-[hsl(var(--surface-modal))]">
            <div className="flex items-center gap-2">
              {showSkipAll && onSkipAll && (
                <button
                  onClick={onSkipAll}
                  className="px-3 py-1.5 text-sm text-muted-foreground hover:text-foreground transition-colors"
                >
                  Skip Tutorial
                </button>
              )}
            </div>

            <div className="flex items-center gap-2">
              {onBack && (
                <button
                  onClick={onBack}
                  className="flex items-center gap-1 px-4 py-2 text-sm border border-border rounded-lg hover:bg-muted transition-colors"
                >
                  <ChevronLeft className="w-4 h-4" />
                  {backLabel}
                </button>
              )}
              {onNext && (
                <button
                  onClick={onNext}
                  className="flex items-center gap-1 px-4 py-2 text-sm bg-primary text-primary-foreground rounded-lg hover:bg-primary/90 transition-colors"
                >
                  {nextLabel}
                  <ChevronRight className="w-4 h-4" />
                </button>
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  );

  return createPortal(modalContent, document.body);
}
