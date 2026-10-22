import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { GitBranch, FolderGit2, FolderSync } from "lucide-react";
import { cn } from "../../lib/utils";

export interface BranchOptionsMenuProps {
  position: { x: number; y: number };
  onClose: () => void;
  onBranchChat: () => void;
  onBranchToWorkspace: () => void;
  onBranchToExistingWorkspace: () => void;
}

export function BranchOptionsMenu({
  position,
  onClose,
  onBranchChat,
  onBranchToWorkspace,
  onBranchToExistingWorkspace,
}: BranchOptionsMenuProps) {
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
    }, 50);

    return () => {
      clearTimeout(timer);
      document.removeEventListener("mousedown", handleClickOutside);
      document.removeEventListener("keydown", handleEscape);
    };
  }, [onClose]);

  // Adjust position to keep menu on screen
  useEffect(() => {
    if (menuRef.current) {
      const rect = menuRef.current.getBoundingClientRect();
      const newPosition = { ...position };
      const padding = 8;

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
        newPosition.y = Math.max(padding, position.y - rect.height);
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

  const handleBranchChat = (e: React.MouseEvent) => {
    e.stopPropagation();
    onBranchChat();
    onClose();
  };

  const handleBranchToWorkspace = (e: React.MouseEvent) => {
    e.stopPropagation();
    onBranchToWorkspace();
    onClose();
  };

  const handleBranchToExistingWorkspace = (e: React.MouseEvent) => {
    e.stopPropagation();
    onBranchToExistingWorkspace();
    onClose();
  };

  return createPortal(
    <div
      ref={menuRef}
      className={cn(
        "fixed z-[9999] min-w-[180px] rounded-lg border border-border/80 bg-background shadow-2xl backdrop-blur-sm transition-opacity",
        isVisible ? "opacity-100" : "opacity-0"
      )}
      style={{ left: adjustedPosition.x, top: adjustedPosition.y }}
    >
      <div className="py-1">
        <button
          onClick={handleBranchChat}
          className="w-full flex items-center gap-2 px-3 py-2 text-sm transition-colors text-left hover:bg-muted/80 hover:text-foreground"
        >
          <GitBranch className="w-4 h-4 text-muted-foreground" />
          <span className="flex-1">Branch to New Chat</span>
        </button>
        <button
          onClick={handleBranchToWorkspace}
          className="w-full flex items-center gap-2 px-3 py-2 text-sm transition-colors text-left hover:bg-muted/80 hover:text-foreground"
        >
          <FolderGit2 className="w-4 h-4 text-muted-foreground" />
          <span className="flex-1">Branch to New Workspace</span>
        </button>
        <button
          onClick={handleBranchToExistingWorkspace}
          className="w-full flex items-center gap-2 px-3 py-2 text-sm transition-colors text-left hover:bg-muted/80 hover:text-foreground"
        >
          <FolderSync className="w-4 h-4 text-muted-foreground" />
          <span className="flex-1">Branch to Existing Workspace</span>
        </button>
      </div>
    </div>,
    document.body
  );
}
