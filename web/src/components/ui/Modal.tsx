import { X } from "lucide-react";
import { cn } from "../../lib/utils";
import { useCallback, useEffect, useRef } from "react";
import { createPortal } from "react-dom";
import { focusChatInput } from "../../hooks/useFocusManager";

interface ModalProps {
  isOpen: boolean;
  onClose: () => void;
  title?: string;
  titlePrefix?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
  size?: "sm" | "md" | "lg" | "xl" | "full";
  hideCloseButton?: boolean;
  headerActions?: React.ReactNode;
}

const sizeClasses = {
  sm: "max-w-md",
  md: "max-w-lg",
  lg: "max-w-2xl",
  xl: "max-w-4xl",
  full: "max-w-6xl",
};

export function Modal({
  isOpen,
  onClose,
  title,
  titlePrefix,
  children,
  className,
  size = "xl",
  hideCloseButton = false,
  headerActions,
}: ModalProps) {
  const modalRef = useRef<HTMLDivElement>(null);
  const hasOpenedRef = useRef(false);
  // Track the element that was focused before modal opened
  const previouslyFocusedRef = useRef<HTMLElement | null>(null);
  // Stable ref for onClose so the effect only depends on isOpen.
  // Without this, every parent re-render creates a new onClose identity,
  // causing the effect to cleanup (overflow: unset) and re-run (overflow: hidden)
  // on every frame — producing a visible layout thrash / flash.
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  const stableOnClose = useCallback(() => onCloseRef.current(), []);
  
  useEffect(() => {
    if (isOpen) {
      // Save the currently focused element before opening modal
      if (!hasOpenedRef.current) {
        previouslyFocusedRef.current = document.activeElement as HTMLElement | null;
      }
      
      document.body.style.overflow = "hidden";
      
      // Dispatch event to close all tooltips (only on first open)
      if (!hasOpenedRef.current) {
        window.dispatchEvent(new CustomEvent('modalOpened'));
      }
      
      const handleKeyDown = (e: KeyboardEvent) => {
        if (e.key === "Escape") {
          onCloseRef.current();
        }
      };
      
      document.addEventListener("keydown", handleKeyDown);
      
      // Only auto-focus on initial open, not on re-renders
      if (modalRef.current && !hasOpenedRef.current) {
        hasOpenedRef.current = true;
        const focusableElements = modalRef.current.querySelectorAll(
          'input, select, textarea, [tabindex]:not([tabindex="-1"]), button:not([aria-label="Close modal"]), [href]'
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
      
      // Restore focus when modal closes
      if (hasOpenedRef.current) {
        hasOpenedRef.current = false;
        
        // Try to restore focus to the previously focused element
        requestAnimationFrame(() => {
          const previousElement = previouslyFocusedRef.current;
          if (previousElement && document.body.contains(previousElement)) {
            try {
              previousElement.focus();
              previouslyFocusedRef.current = null;
              return;
            } catch {
              // Element may no longer be focusable
            }
          }
          
          // Fall back to focusing chat input
          previouslyFocusedRef.current = null;
          focusChatInput();
        });
      }
    }
  }, [isOpen]);

  if (!isOpen) {
    return null;
  }

  const modalContent = (
    <div 
      className="fixed inset-0 z-50 isolate flex items-center justify-center p-4 sm:p-6"
      role="dialog"
      aria-modal="true"
      aria-labelledby={title ? "modal-title" : undefined}
      data-modal-open="true"
    >
      <div
        className="absolute inset-0 bg-black/80"
        onClick={stableOnClose}
        aria-hidden="true"
      />

      <div
        ref={modalRef}
        className={cn(
          "relative border border-border/50 rounded-xl",
          "w-full max-h-[90vh] overflow-hidden flex flex-col",
          "elevation-5",
          "animate-in fade-in-0 zoom-in-95 duration-200",
          sizeClasses[size],
          className
        )}
      >
        {title && (
          <div className="flex items-center justify-between gap-4 px-6 py-5 border-b border-border/60 elevation-1">
            <div className="flex items-center gap-3 min-w-0">
              {titlePrefix}
              <h2 
                id="modal-title"
                className="text-xl font-semibold text-foreground tracking-tight truncate"
              >
                {title}
              </h2>
            </div>
            <div className="flex items-center gap-2">
              {headerActions}
              {!hideCloseButton && (
                <button
                  onClick={stableOnClose}
                  className="p-2 -mr-2 hover:bg-muted/80 rounded-lg transition-colors focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 focus:ring-offset-background"
                  aria-label="Close modal"
                >
                  <X className="w-5 h-5 text-muted-foreground" />
                </button>
              )}
            </div>
          </div>
        )}

        <div className="flex-1 overflow-y-auto bg-[hsl(var(--surface-modal))]">
          <div className="px-6 py-6">{children}</div>
        </div>
      </div>
    </div>
  );

  return createPortal(modalContent, document.body);
}
