import { useState, useRef, useEffect, useCallback, useLayoutEffect, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { ChevronDown } from "lucide-react";
import { cn } from "../../lib/utils";
import { Tooltip } from "./Tooltip";

interface DropdownProps {
  // Trigger
  trigger?: ReactNode;
  triggerClassName?: string;
  
  // Content
  children: ReactNode;
  
  // Behavior
  isOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
  disabled?: boolean;
  
  // Styling
  align?: "left" | "right";
  direction?: "up" | "down";
  className?: string;
  contentClassName?: string;
  
  // Tooltip
  tooltip?: string;
  
  // Variants
  variant?: "chat" | "form";
  compact?: boolean;
}

const VIEWPORT_PADDING = 8;
const DROPDOWN_GAP = 4;

interface DropdownPosition {
  top: number;
  left: number;
}

/**
 * Reusable Dropdown component with consistent behavior and styling.
 * Uses mousedown listener pattern for proper click-outside handling.
 */
export function Dropdown({
  trigger,
  triggerClassName,
  children,
  isOpen: controlledIsOpen,
  onOpenChange,
  disabled = false,
  align = "left",
  direction = "down",
  className,
  contentClassName,
  tooltip,
  variant = "chat",
  compact = false,
}: DropdownProps) {
  const [uncontrolledIsOpen, setUncontrolledIsOpen] = useState(false);
  const [position, setPosition] = useState<DropdownPosition | null>(null);
  const dropdownRef = useRef<HTMLDivElement>(null);
  const contentRef = useRef<HTMLDivElement>(null);
  
  // Use controlled state if provided, otherwise use internal state
  const isOpen = controlledIsOpen ?? uncontrolledIsOpen;
  const setIsOpen = useCallback((open: boolean) => {
    if (onOpenChange) {
      onOpenChange(open);
    } else {
      setUncontrolledIsOpen(open);
    }
  }, [onOpenChange]);

  const updatePosition = useCallback(() => {
    const triggerElement = dropdownRef.current;
    const contentElement = contentRef.current;

    if (!triggerElement || !contentElement) {
      return;
    }

    const triggerRect = triggerElement.getBoundingClientRect();
    const contentRect = contentElement.getBoundingClientRect();
    const contentWidth = contentRect.width;
    const contentHeight = contentRect.height;
    const viewportWidth = window.innerWidth;
    const viewportHeight = window.innerHeight;
    const topWhenDown = triggerRect.bottom + DROPDOWN_GAP;
    const topWhenUp = triggerRect.top - contentHeight - DROPDOWN_GAP;

    let top = direction === "up" ? topWhenUp : topWhenDown;
    let left = align === "right" ? triggerRect.right - contentWidth : triggerRect.left;

    if (
      direction === "down" &&
      top + contentHeight > viewportHeight - VIEWPORT_PADDING &&
      topWhenUp >= VIEWPORT_PADDING
    ) {
      top = topWhenUp;
    } else if (
      direction === "up" &&
      top < VIEWPORT_PADDING &&
      topWhenDown + contentHeight <= viewportHeight - VIEWPORT_PADDING
    ) {
      top = topWhenDown;
    }

    const maxLeft = Math.max(VIEWPORT_PADDING, viewportWidth - contentWidth - VIEWPORT_PADDING);
    const maxTop = Math.max(VIEWPORT_PADDING, viewportHeight - contentHeight - VIEWPORT_PADDING);

    left = Math.min(Math.max(left, VIEWPORT_PADDING), maxLeft);
    top = Math.min(Math.max(top, VIEWPORT_PADDING), maxTop);

    setPosition((currentPosition) => {
      if (currentPosition?.top === top && currentPosition.left === left) {
        return currentPosition;
      }

      return { top, left };
    });
  }, [align, direction]);

  // Close dropdown when clicking outside - STANDARD PATTERN
  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      const target = event.target as Node;

      if (
        dropdownRef.current &&
        !dropdownRef.current.contains(target) &&
        contentRef.current &&
        !contentRef.current.contains(target)
      ) {
        setIsOpen(false);
      }
    };

    const handleEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setIsOpen(false);
      }
    };

    if (isOpen) {
      document.addEventListener("mousedown", handleClickOutside);
      document.addEventListener("keydown", handleEscape);
      return () => {
        document.removeEventListener("mousedown", handleClickOutside);
        document.removeEventListener("keydown", handleEscape);
      };
    }
  }, [isOpen, setIsOpen]);

  useLayoutEffect(() => {
    if (isOpen) {
      updatePosition();
    } else {
      setPosition(null);
    }
  }, [isOpen, updatePosition, children]);

  useEffect(() => {
    if (!isOpen) {
      return;
    }

    const handleReposition = () => updatePosition();
    const resizeObserver =
      typeof ResizeObserver !== "undefined" ? new ResizeObserver(handleReposition) : null;

    window.addEventListener("resize", handleReposition);
    window.addEventListener("scroll", handleReposition, true);

    if (dropdownRef.current) {
      resizeObserver?.observe(dropdownRef.current);
    }
    if (contentRef.current) {
      resizeObserver?.observe(contentRef.current);
    }

    return () => {
      window.removeEventListener("resize", handleReposition);
      window.removeEventListener("scroll", handleReposition, true);
      resizeObserver?.disconnect();
    };
  }, [isOpen, updatePosition]);

  const handleToggle = () => {
    if (!disabled) {
      setIsOpen(!isOpen);
    }
  };

  const defaultTrigger = (
    <button
      type="button"
      onClick={handleToggle}
      disabled={disabled}
      className={cn(
        "flex items-center gap-1 rounded border-2 transition-colors font-semibold",
        variant === "chat" && [
          "bg-[var(--chat-button-bg)] text-[var(--chat-button-text)] border-[var(--chat-border)]",
          !disabled && "cursor-pointer hover:bg-[var(--chat-button-hover)]",
        ],
        variant === "form" && [
          "bg-background text-foreground border-border",
          !disabled && "cursor-pointer hover:border-primary/50",
        ],
        compact ? "px-1.5 py-0.5 text-[10px] h-6" : "px-2 py-1 text-xs h-7",
        disabled && "opacity-60 cursor-not-allowed",
        triggerClassName
      )}
    >
      <span>Select</span>
      <ChevronDown
        className={cn(
          "transition-transform flex-shrink-0",
          compact ? "w-3 h-3" : "w-3.5 h-3.5",
          isOpen && "rotate-180"
        )}
      />
    </button>
  );

  const portalledContent =
    isOpen && typeof document !== "undefined"
      ? createPortal(
          <div
            ref={contentRef}
            className={cn(
              "fixed z-[9999] rounded-md elevation-4 border-2 min-w-64 overflow-hidden",
              variant === "chat" && "bg-[var(--chat-dropdown-bg)] border-[var(--chat-border)]",
              variant === "form" && "bg-background border-border",
              contentClassName
            )}
            style={{
              top: position?.top ?? 0,
              left: position?.left ?? 0,
              visibility: position ? "visible" : "hidden",
              maxWidth: `calc(100vw - ${VIEWPORT_PADDING * 2}px)`,
            }}
          >
            {children}
          </div>,
          document.body
        )
      : null;

  const dropdownContent = (
    <>
      <div className={cn("relative", className)} ref={dropdownRef} data-dropdown-open={isOpen}>
        {trigger || defaultTrigger}
      </div>
      {portalledContent}
    </>
  );

  return tooltip ? (
    <Tooltip content={tooltip} placement="top">
      {dropdownContent}
    </Tooltip>
  ) : (
    dropdownContent
  );
}
