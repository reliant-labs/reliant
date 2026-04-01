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

  // Focus the container when modal opens so it can receive keyboard events
  useEffect(() => {
    if (isOpen && containerRef.current) {
      containerRef.current.focus();
    }
  }, [isOpen]);

  useEffect(() => {
    if (!isOpen) return;

    const handleEscape = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        handleClose();
      }
    };

    // Use capture phase to ensure we get the event first
    document.addEventListener("keydown", handleEscape, true);
    document.body.style.overflow = "hidden";

    return () => {
      document.removeEventListener("keydown", handleEscape, true);
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
      className="fixed inset-0 z-[9999] flex flex-col bg-black outline-none"
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.preventDefault();
          e.stopPropagation();
          handleClose();
        }
      }}
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
