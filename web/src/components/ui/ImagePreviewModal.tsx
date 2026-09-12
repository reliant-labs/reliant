import { X } from "lucide-react";
import { useEffect, useRef, useCallback } from "react";
import { createPortal } from "react-dom";
import { focusChatInput } from "../../hooks/useFocusManager";

interface ImagePreviewModalProps {
  isOpen: boolean;
  onClose: () => void;
  imageUrl: string;
  filename: string;
}

export function ImagePreviewModal({
  isOpen,
  onClose,
  imageUrl,
  filename,
}: ImagePreviewModalProps) {
  const wasOpenRef = useRef(false);
  const containerRef = useRef<HTMLDivElement>(null);

  // Stable close handler
  const handleClose = useCallback(() => {
    onClose();
  }, [onClose]);

  // Focus the container when the modal opens, so screen readers land inside the
  // dialog and the scroll container responds to arrow keys. Escape no longer
  // depends on this: see the listener below for why focus was never the issue.
  useEffect(() => {
    if (isOpen && containerRef.current) {
      containerRef.current.focus();
    }
  }, [isOpen]);

  useEffect(() => {
    if (!isOpen) return;

    const handleEscape = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.preventDefault();
      // stopImmediatePropagation, not just stopPropagation: the global shortcut
      // dispatcher's listener is on `document`, and stopPropagation alone would
      // still let it run.
      e.stopPropagation();
      e.stopImmediatePropagation();
      handleClose();
    };

    // `window`, capture phase — and the node matters more than the phase.
    //
    // This modal previously registered Escape at `document` in capture phase AND
    // as a React onKeyDown on the focused container, and NEITHER fired. The
    // global shortcut dispatcher (useAppKeyboardShortcuts -> lib/keyboard) owns
    // Escape from a single capture-phase listener on `document`, registered when
    // ModernApp mounts — long before any preview opens. Because this overlay
    // renders `data-modal-open="true"`, detectActiveContexts puts the dispatcher
    // in the `modal` context, where Escape resolves to the non-passthrough
    // `dismissModal` shortcut, so the dispatcher calls stopImmediatePropagation.
    // That killed the modal's own document-capture listener (same node,
    // registered later) and stopped the event before the bubble phase ever
    // reached React's onKeyDown. Both paths were dead by construction, and the
    // dispatcher's `onDismissModal` only closes the overlays whose open state
    // ModernApp itself holds — which does not include this one.
    //
    // Capture descends window -> document -> ... , so a window-capture listener
    // is ordered ahead of the dispatcher rather than racing it by mount order.
    // Claiming the event here also correctly suppresses the `stopStreaming`
    // binding, so Escape dismisses the top-most overlay instead of pausing the
    // chat behind it.
    window.addEventListener("keydown", handleEscape, true);
    document.body.style.overflow = "hidden";

    return () => {
      window.removeEventListener("keydown", handleEscape, true);
      document.body.style.overflow = "unset";
    };
  }, [isOpen, handleClose]);

  // Restore focus to chat input when modal closes
  useEffect(() => {
    if (isOpen) {
      wasOpenRef.current = true;
    } else if (wasOpenRef.current) {
      wasOpenRef.current = false;
      focusChatInput();
    }
  }, [isOpen]);

  if (!isOpen) return null;

  const modalContent = (
    <div
      ref={containerRef}
      tabIndex={-1}
      data-modal-open="true"
      role="dialog"
      aria-modal="true"
      aria-label={filename}
      className="fixed inset-0 z-[9999] flex flex-col bg-black outline-none"
    >
      {/* Top bar with close button */}
      <div className="flex-shrink-0 flex items-center px-4 py-3 bg-black border-b border-white/10 relative">
        {/* Spacer for centering */}
        <div className="w-8" />
        
        {/* Centered title */}
        <div className="flex-1 text-center">
          <span className="text-white text-sm font-medium truncate">
            {filename}
          </span>
        </div>
        
        {/* Close button */}
        <button
          onClick={onClose}
          className="p-1.5 hover:bg-white/10 rounded-md transition-colors flex-shrink-0"
          title="Close (ESC)"
        >
          <X className="w-4 h-4 text-white" />
        </button>
      </div>

      {/* Image area - click to close */}
      <div 
        className="flex-1 flex items-center justify-center overflow-auto p-8 cursor-pointer"
        onClick={onClose}
      >
        <img
          src={imageUrl}
          alt={filename}
          className="max-w-full max-h-full object-contain"
          onClick={(e) => e.stopPropagation()}
        />
      </div>
    </div>
  );

  return createPortal(modalContent, document.body);
}
