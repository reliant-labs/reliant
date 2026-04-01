import { useState, useRef, useEffect, useCallback, type ReactNode } from "react";
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
  const dropdownRef = useRef<HTMLDivElement>(null);
  
  // Use controlled state if provided, otherwise use internal state
  const isOpen = controlledIsOpen ?? uncontrolledIsOpen;
  const setIsOpen = useCallback((open: boolean) => {
    if (onOpenChange) {
      onOpenChange(open);
    } else {
      setUncontrolledIsOpen(open);
    }
  }, [onOpenChange]);

  // Close dropdown when clicking outside - STANDARD PATTERN
  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(event.target as Node)) {
        setIsOpen(false);
      }
    };

    if (isOpen) {
      document.addEventListener("mousedown", handleClickOutside);
      return () => document.removeEventListener("mousedown", handleClickOutside);
    }
  }, [isOpen, setIsOpen]);

  const handleToggle = () => {
    if (!disabled) {
      setIsOpen(!isOpen);
    }
  };

  const defaultTrigger = (
    <button
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

  const dropdownContent = (
    <div className={cn("relative", className)} ref={dropdownRef}>
      {trigger || defaultTrigger}

      {isOpen && (
        <div
          className={cn(
            "absolute z-50 rounded-md elevation-4 border-2 min-w-64 overflow-hidden",
            align === "left" ? "left-0" : "right-0",
            direction === "up" ? "bottom-full mb-1" : "top-full mt-1",
            variant === "chat" && "bg-[var(--chat-dropdown-bg)] border-[var(--chat-border)]",
            variant === "form" && "bg-background border-border",
            contentClassName
          )}
        >
          {children}
        </div>
      )}
    </div>
  );

  return tooltip ? (
    <Tooltip content={tooltip} placement="top">
      {dropdownContent}
    </Tooltip>
  ) : (
    dropdownContent
  );
}
