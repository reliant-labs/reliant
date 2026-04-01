import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { cn } from "../../lib/utils";
import { createPortal } from "react-dom";
import { focusChatInput, isInputFocused } from "../../hooks/useFocusManager";

export interface ContextMenuItem {
  label: string;
  icon?: ReactNode;
  onClick: () => void;
  danger?: boolean;
  separator?: boolean;
  disabled?: boolean;
}

interface ContextMenuProps {
  items: ContextMenuItem[];
  position: { x: number; y: number };
  onClose: () => void;
}

export function ContextMenu({ items, position, onClose }: ContextMenuProps) {
  const menuRef = useRef<HTMLDivElement>(null);
  const [adjustedPosition, setAdjustedPosition] = useState(position);
  const [isVisible, setIsVisible] = useState(false);

  useEffect(() => {
    const handleClickOutside = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        onClose();
      }
    };

    const handleEscape = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        onClose();
      }
    };

    // Add listeners after a small delay to prevent immediate close
    const timer = setTimeout(() => {
      document.addEventListener("mousedown", handleClickOutside);
      document.addEventListener("keydown", handleEscape);
    }, 100);

    return () => {
      clearTimeout(timer);
      document.removeEventListener("mousedown", handleClickOutside);
      document.removeEventListener("keydown", handleEscape);
      // Restore focus to chat input when menu closes, but only if no other input is focused
      // (e.g., don't steal focus from inline rename inputs)
      // Use requestAnimationFrame to check after React has committed any new inputs with autoFocus
      requestAnimationFrame(() => {
        if (!isInputFocused()) {
          focusChatInput();
        }
      });
    };
  }, [onClose]);

  // Adjust position to keep menu on screen
  useEffect(() => {
    if (menuRef.current) {
      const rect = menuRef.current.getBoundingClientRect();
      const newPosition = { ...position };
      const padding = 8; // Padding from edge of screen

      // Adjust horizontal position if menu goes off right edge
      if (position.x + rect.width > window.innerWidth) {
        newPosition.x = Math.max(padding, window.innerWidth - rect.width - padding);
      }
      
      // Ensure menu doesn't go off left edge
      if (newPosition.x < padding) {
        newPosition.x = padding;
      }

      // Adjust vertical position if menu goes off bottom edge
      if (position.y + rect.height > window.innerHeight) {
        // Try to position above the cursor
        newPosition.y = Math.max(padding, position.y - rect.height);
        
        // If still off screen, position at bottom with padding
        if (newPosition.y < padding) {
          newPosition.y = Math.max(padding, window.innerHeight - rect.height - padding);
        }
      }
      
      // Ensure menu doesn't go off top edge
      if (newPosition.y < padding) {
        newPosition.y = padding;
      }

      setAdjustedPosition(newPosition);
      setIsVisible(true);
    }
  }, [position]);

  return createPortal(
    <div
      ref={menuRef}
      className={cn(
        "fixed z-[9999] min-w-[200px] rounded-lg border border-border/80 bg-background shadow-2xl backdrop-blur-sm transition-opacity",
        isVisible ? "opacity-100" : "opacity-0"
      )}
      style={{ left: adjustedPosition.x, top: adjustedPosition.y }}
    >
      <div className="py-1">
        {items.map((item, index) => {
          if (item.separator) {
            return (
              <div
                key={`separator-${index}`}
                className="my-1 h-px bg-border/60"
              />
            );
          }

          return (
            <button
              key={index}
              onClick={(e) => {
                if (!item.disabled) {
                  // Stop event propagation
                  e.stopPropagation();
                  // Execute action immediately
                  item.onClick();
                  // Close menu after React has flushed the state updates
                  // Use queueMicrotask to ensure the menu closes after the onClick state updates are committed
                  queueMicrotask(() => {
                    onClose();
                  });
                }
              }}
              disabled={item.disabled}
              className={cn(
                "w-full flex items-center gap-2 px-3 py-2 text-sm font-mono transition-colors text-left",
                item.disabled
                  ? "opacity-50 cursor-not-allowed"
                  : item.danger
                  ? "hover:bg-destructive/10 hover:text-destructive"
                  : "hover:bg-muted/80 hover:text-foreground",
                "disabled:pointer-events-none"
              )}
            >
              {item.icon && (
                <span className="w-4 h-4 flex items-center justify-center">
                  {item.icon}
                </span>
              )}
              <span className="flex-1">{item.label}</span>
            </button>
          );
        })}
      </div>
    </div>,
    document.body
  );
}
